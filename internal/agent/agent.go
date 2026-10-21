// Package agent is Helium Syncer's background loop: it watches Helium and
// syncs a few seconds after Helium quits, and every few minutes while
// Helium is closed (so the next start is already up to date). While Helium
// is open it never touches anything.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/imrishabhchauhan/helium-syncer/internal/app"
	"github.com/imrishabhchauhan/helium-syncer/internal/engine"
	"github.com/imrishabhchauhan/helium-syncer/internal/helium"
	"github.com/imrishabhchauhan/helium-syncer/internal/power"
)

// Timing of the loop. Fields left at zero fall back to a sensible value.
type Timing struct {
	Poll               time.Duration // how often to check whether Helium runs (plugged in)
	PollBattery        time.Duration // the same on battery
	AfterQuit          time.Duration // wait after Helium quits (it flushes files)
	WhileClosed        time.Duration // pull interval while Helium is closed (plugged in)
	WhileClosedBattery time.Duration // the same on battery
	MaxBackoff         time.Duration
}

// DefaultTiming is used by the real agent. A check is one cheap file test;
// a pull is one small download when nothing changed.
var DefaultTiming = Timing{Poll: 5 * time.Second, PollBattery: 15 * time.Second, AfterQuit: 4 * time.Second,
	WhileClosed: 5 * time.Minute, WhileClosedBattery: 30 * time.Minute, MaxBackoff: 30 * time.Minute}

// Battery modes (app.Config.Battery).
const (
	BatteryBalanced = "balanced" // default: on battery pull less; on low battery only send after quit
	BatteryPlugged  = "plugged"  // on battery do nothing until plugged in (Sync now still works)
	BatteryAlways   = "always"   // on battery the same as plugged in
)

// LockAddr is a local port held while an agent runs (one agent at a time).
const LockAddr = "127.0.0.1:47811"

// ErrNotSetUp means the first-sync preview has not been approved yet.
var ErrNotSetUp = errors.New("setup is not finished: approve the first-sync preview in Helium Syncer")

// AutostartArgs are the arguments start-at-login and StartDetached pass to
// this program. The command line runs "agent"; the app runs itself hidden.
var AutostartArgs = []string{"agent"}

// ErrAlreadyRunning means another agent holds the lock.
var ErrAlreadyRunning = errors.New("Helium Syncer is already running in the background")

// Status is what the agent last did, for status lines.
type Status struct {
	mu         sync.Mutex
	HeliumOpen bool
	LastSync   time.Time
	LastReport engine.Report
	LastError  string
	Paused     string
	Syncing    bool
	PowerNote  string // why a sync waits for power, if it does
}

// Snapshot copies the status.
func (s *Status) Snapshot() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{HeliumOpen: s.HeliumOpen, LastSync: s.LastSync, LastReport: s.LastReport, LastError: s.LastError, Paused: s.Paused, Syncing: s.Syncing, PowerNote: s.PowerNote}
}

// Agent runs the loop.
type Agent struct {
	Timing Timing
	Status *Status
	// Sync runs one sync; replaceable in tests.
	Sync func(ctx context.Context) (engine.Report, error)
	// Running reports whether Helium is open; replaceable in tests.
	Running func() (bool, error)
	// Now replaceable in tests.
	Now func() time.Time
	// Trigger asks for a sync as soon as possible (e.g. "Sync now").
	Trigger chan struct{}
	Log     *log.Logger
	// Power reads the power supply; nil means always plugged in.
	Power func() power.State
	// Mode is the battery mode (BatteryBalanced when nil or empty).
	Mode func() string

	pw     power.State
	pwRead time.Time
}

// New builds the real agent for the saved config.
func New() (*Agent, error) {
	cfg, err := app.Load()
	if err != nil {
		return nil, err
	}
	if !cfg.Connected() {
		return nil, app.ErrNotConnected
	}
	lf, err := openLog()
	if err != nil {
		return nil, err
	}
	a := &Agent{Timing: DefaultTiming, Status: &Status{}, Now: time.Now, Trigger: make(chan struct{}, 1), Log: log.New(lf, "", log.LstdFlags)}
	a.Running = func() (bool, error) { return helium.IsRunning(cfg.UserData) }
	a.Power = power.Read
	a.Mode = configMode()
	a.Sync = func(ctx context.Context) (engine.Report, error) {
		c, err := app.Load() // pick up changes made from the command line
		if err != nil {
			return engine.Report{}, err
		}
		if !c.Connected() || !c.SetupDone {
			// Never sync before the person approved the first-sync preview.
			return engine.Report{}, ErrNotSetUp
		}
		ec, err := c.EngineConfig()
		if err != nil {
			return engine.Report{}, err
		}
		rep, err := runLow(ctx, ec)
		// Load again: settings may have changed while the sync ran.
		if c, lerr := app.Load(); lerr == nil {
			if err == nil && rep.Paused == "" {
				c.LastSync, c.LastError = time.Now(), ""
			} else if err != nil {
				c.LastError = err.Error()
			}
			c.Save()
		}
		return rep, err
	}
	return a, nil
}

// runLow runs a sync at low priority, so the computer stays quiet and cool.
func runLow(ctx context.Context, ec engine.Config) (engine.Report, error) {
	restore := power.Background()
	defer restore()
	return engine.Run(ctx, ec)
}

// configMode reads the battery mode from the settings file.
func configMode() func() string {
	return func() string {
		if c, err := app.LoadCached(); err == nil {
			return c.Battery
		}
		return ""
	}
}

func openLog() (*os.File, error) {
	p := filepath.Join(app.Dir(), "agent.log")
	if info, err := os.Stat(p); err == nil && info.Size() > 1<<20 {
		os.Rename(p, p+".1")
	}
	if err := os.MkdirAll(app.Dir(), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}

// Lock takes the single-instance lock; keep the listener open while running.
func Lock() (net.Listener, error) {
	l, err := net.Listen("tcp", LockAddr)
	if err != nil {
		return nil, ErrAlreadyRunning
	}
	return l, nil
}

// ServeShow answers "show your window" and "quit" from another copy of the
// app, on the lock port. Websites cannot send the custom header without a
// CORS preflight, which is refused, so they cannot trigger either.
func ServeShow(l net.Listener, show, quit func()) error {
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		what := r.Header.Get("X-Helium-Syncer")
		if r.Method != http.MethodPost || r.Host != LockAddr || r.URL.Path != "/"+what || (what != "show" && what != "quit") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		if what == "show" {
			show()
		} else if quit != nil {
			go quit()
		}
	})}
	return srv.Serve(l)
}

// AskToShow asks the copy that holds the lock to show its window. It fails
// when that copy is the command-line agent, which has no window.
func AskToShow() error { return ask("show") }

// AskToQuit asks the running app to quit (for scripts and updates).
func AskToQuit() error { return ask("quit") }

func ask(what string) error {
	req, err := http.NewRequest(http.MethodPost, "http://"+LockAddr+"/"+what, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Helium-Syncer", what)
	c := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	res, err := c.Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		return fmt.Errorf("the running copy answered %d", res.StatusCode)
	}
	return nil
}

// power returns the power state, read at most once a minute (on macOS a
// reading starts a small program).
func (a *Agent) power(now time.Time) power.State {
	if a.Power == nil {
		return power.State{Percent: -1}
	}
	if a.pwRead.IsZero() || now.Sub(a.pwRead) >= time.Minute || now.Before(a.pwRead) {
		a.pw, a.pwRead = a.Power(), now
	}
	return a.pw
}

func (a *Agent) mode() string {
	if a.Mode != nil {
		switch m := a.Mode(); m {
		case BatteryPlugged, BatteryAlways:
			return m
		}
	}
	return BatteryBalanced
}

func or(d, fallback time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return fallback
}

// Run loops until ctx ends.
//
// Plugged in, it checks Helium every few seconds, syncs after Helium quits
// and pulls every few minutes while Helium is closed. On battery it does
// less (see the Battery modes). While Helium is open it never syncs.
func (a *Agent) Run(ctx context.Context) error {
	t := a.Timing
	pollBattery := or(t.PollBattery, t.Poll)
	closedBattery := or(t.WhileClosedBattery, 6*t.WhileClosed)
	wasOpen := false
	pendingQuit := false // Helium quit and its changes are not sent yet
	quitAt := time.Time{}
	var lastAttempt time.Time
	backoff := time.Duration(0)
	first := true
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		open, err := a.Running()
		if err != nil {
			a.logf("cannot tell whether Helium is open: %v", err)
			open = true // be safe: never sync when unsure
		}
		now := a.Now()
		pw, mode := a.power(now), a.mode()
		pluggedOnly := mode == BatteryPlugged && pw.OnBattery
		lowPower := mode != BatteryAlways && pw.Low()
		if open != wasOpen && !first {
			if open {
				a.logf("Helium opened; waiting until it quits")
			} else {
				a.logf("Helium quit; syncing in %s", t.AfterQuit)
			}
		}
		due, reason, note := false, "", ""
		switch {
		case open:
			// Never touch anything while Helium is open.
		case wasOpen:
			quitAt, pendingQuit = now, true
		case pendingQuit && now.Sub(quitAt) >= t.AfterQuit:
			if pluggedOnly {
				note = "Your changes sync when you plug in."
			} else {
				due, reason = true, "Helium quit"
			}
		case pendingQuit:
			// Wait until Helium has written its files.
		case pluggedOnly:
			note = "On battery: syncs when you plug in."
		case lowPower && !first:
			// Low battery or saver mode: pull nothing now. The next quit of
			// Helium still sends its changes.
		default:
			interval := t.WhileClosed
			if pw.OnBattery && mode == BatteryBalanced {
				interval = closedBattery
			}
			if first || now.Sub(lastAttempt) >= interval+backoff {
				due, reason = true, "periodic"
			}
		}
		select {
		case <-a.Trigger:
			if !open {
				due, reason = true, "requested" // a person asked: always do it
			}
		default:
		}
		a.Status.mu.Lock()
		a.Status.HeliumOpen, a.Status.PowerNote = open, note
		a.Status.mu.Unlock()
		wasOpen, first = open, false
		if due {
			lastAttempt = now
			if reason == "Helium quit" || reason == "requested" {
				pendingQuit = false
			}
			if err := a.syncOnce(ctx, reason); err != nil {
				backoff = min(max(2*backoff, time.Minute), t.MaxBackoff)
			} else {
				backoff = 0
			}
		}
		wait := t.Poll
		if pw.OnBattery {
			wait = pollBattery
		}
		if pendingQuit && !pluggedOnly {
			if left := t.AfterQuit - a.Now().Sub(quitAt); left < wait {
				wait = max(left, 10*time.Millisecond)
			}
		}
		timer.Reset(wait)
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		case <-a.Trigger:
			if !timer.Stop() {
				<-timer.C
			}
			// Re-queue so the next loop iteration sees it.
			select {
			case a.Trigger <- struct{}{}:
			default:
			}
		}
	}
}

func (a *Agent) syncOnce(ctx context.Context, reason string) error {
	a.Status.mu.Lock()
	a.Status.Syncing = true
	a.Status.mu.Unlock()
	rep, err := a.Sync(ctx)
	a.Status.mu.Lock()
	defer a.Status.mu.Unlock()
	a.Status.Syncing = false
	switch {
	case errors.Is(err, ErrNotSetUp):
		return nil // quietly wait for the person
	case errors.Is(err, engine.ErrBusy):
		a.logf("%s: another sync is running; will try again", reason)
		return nil
	case errors.Is(err, engine.ErrHeliumRunning):
		a.logf("%s: Helium opened before the sync started; will retry after it quits", reason)
		return nil
	case err != nil:
		a.Status.LastError = err.Error()
		a.logf("%s: sync failed: %v", reason, err)
		return err
	case rep.Paused != "":
		a.Status.Paused = rep.Paused
		a.logf("%s: paused: %s", reason, rep.Paused)
		return nil
	}
	a.Status.LastSync, a.Status.LastReport, a.Status.LastError, a.Status.Paused = a.Now(), rep, "", ""
	if rep.Sent+rep.Received > 0 || reason != "periodic" {
		a.logf("%s: sent %d, received %d, wrote %v", reason, rep.Sent, rep.Received, rep.Written)
	}
	return nil
}

func (a *Agent) logf(format string, args ...any) {
	if a.Log != nil {
		a.Log.Output(2, fmt.Sprintf(format, args...))
	}
}
