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
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/imrishabhchauhan/helium-syncer/internal/app"
	"github.com/imrishabhchauhan/helium-syncer/internal/engine"
	"github.com/imrishabhchauhan/helium-syncer/internal/helium"
)

// Timing of the loop.
type Timing struct {
	Poll        time.Duration // how often to check whether Helium runs
	AfterQuit   time.Duration // wait after Helium quits (it flushes files)
	WhileClosed time.Duration // pull interval while Helium is closed
	MaxBackoff  time.Duration
}

// DefaultTiming is used by the real agent.
var DefaultTiming = Timing{Poll: 2 * time.Second, AfterQuit: 4 * time.Second, WhileClosed: 5 * time.Minute, MaxBackoff: 30 * time.Minute}

// LockAddr is a local port held while an agent runs (one agent at a time).
const LockAddr = "127.0.0.1:47811"

// ErrNotSetUp means the first-sync preview has not been approved yet.
var ErrNotSetUp = errors.New("setup is not finished: approve the first-sync preview in Helium Syncer")

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
}

// Snapshot copies the status.
func (s *Status) Snapshot() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{HeliumOpen: s.HeliumOpen, LastSync: s.LastSync, LastReport: s.LastReport, LastError: s.LastError, Paused: s.Paused, Syncing: s.Syncing}
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
	a.Sync = func(ctx context.Context) (engine.Report, error) {
		c, err := app.Load() // pick up changes made from the command line
		if err != nil {
			return engine.Report{}, err
		}
		if !c.SetupDone {
			// Never sync before the person approved the first-sync preview.
			return engine.Report{}, ErrNotSetUp
		}
		ec, err := c.EngineConfig()
		if err != nil {
			return engine.Report{}, err
		}
		rep, err := engine.Run(ctx, ec)
		if err == nil && rep.Paused == "" {
			c.LastSync, c.LastError = time.Now(), ""
		} else if err != nil {
			c.LastError = err.Error()
		}
		c.Save()
		return rep, err
	}
	return a, nil
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

// Run loops until ctx ends.
func (a *Agent) Run(ctx context.Context) error {
	t := a.Timing
	wasOpen := false
	quitAt := time.Time{}
	var lastAttempt time.Time
	backoff := time.Duration(0)
	first := true
	tick := time.NewTicker(t.Poll)
	defer tick.Stop()
	for {
		open, err := a.Running()
		if err != nil {
			a.logf("cannot tell whether Helium is open: %v", err)
			open = true // be safe: never sync when unsure
		}
		a.Status.mu.Lock()
		a.Status.HeliumOpen = open
		a.Status.mu.Unlock()
		now := a.Now()
		if open != wasOpen && !first {
			if open {
				a.logf("Helium opened; waiting until it quits")
			} else {
				a.logf("Helium quit; syncing in %s", t.AfterQuit)
			}
		}
		due := false
		reason := ""
		switch {
		case open:
			// Never touch anything while Helium is open.
		case wasOpen:
			quitAt = now
		case !quitAt.IsZero() && now.Sub(quitAt) >= t.AfterQuit:
			due, reason = true, "Helium quit"
			quitAt = time.Time{}
		case quitAt.IsZero() && (first || now.Sub(lastAttempt) >= t.WhileClosed+backoff):
			due, reason = true, "periodic"
		}
		select {
		case <-a.Trigger:
			if !open {
				due, reason = true, "requested"
			}
		default:
		}
		wasOpen, first = open, false
		if due {
			lastAttempt = now
			if err := a.syncOnce(ctx, reason); err != nil {
				backoff = min(max(2*backoff, time.Minute), t.MaxBackoff)
			} else {
				backoff = 0
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		case <-a.Trigger:
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
