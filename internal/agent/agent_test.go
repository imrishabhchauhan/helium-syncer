package agent

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/imrishabhchauhan/helium-syncer/internal/engine"
	"github.com/imrishabhchauhan/helium-syncer/internal/power"
)

// fake drives the loop with a scripted "is Helium open" timeline.
type fake struct {
	mu    sync.Mutex
	open  bool
	syncs []string // when each sync happened, as "open/closed" at that moment
}

func (f *fake) setOpen(v bool) { f.mu.Lock(); f.open = v; f.mu.Unlock() }

func newTestAgent(f *fake) *Agent {
	return &Agent{
		Timing:  Timing{Poll: 5 * time.Millisecond, AfterQuit: 30 * time.Millisecond, WhileClosed: 200 * time.Millisecond, MaxBackoff: time.Second},
		Status:  &Status{},
		Now:     time.Now,
		Trigger: make(chan struct{}, 1),
		Running: func() (bool, error) { f.mu.Lock(); defer f.mu.Unlock(); return f.open, nil },
		Sync: func(context.Context) (engine.Report, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			state := "closed"
			if f.open {
				state = "OPEN"
			}
			f.syncs = append(f.syncs, state)
			return engine.Report{}, nil
		},
	}
}

func TestAgentSyncsAfterQuitAndNeverWhileOpen(t *testing.T) {
	f := &fake{open: true}
	a := newTestAgent(f)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()

	time.Sleep(150 * time.Millisecond) // Helium open: nothing may happen
	f.mu.Lock()
	n := len(f.syncs)
	f.mu.Unlock()
	if n != 0 {
		t.Fatalf("synced %d time(s) while Helium was open", n)
	}
	select {
	case a.Trigger <- struct{}{}: // "Sync now" while open must also wait
	default:
	}
	time.Sleep(50 * time.Millisecond)
	f.setOpen(false) // user quits Helium
	time.Sleep(100 * time.Millisecond)
	f.mu.Lock()
	n = len(f.syncs)
	f.mu.Unlock()
	if n < 1 {
		t.Fatal("no sync after Helium quit")
	}
	time.Sleep(500 * time.Millisecond) // periodic syncs while closed
	f.setOpen(true)
	time.Sleep(100 * time.Millisecond)
	f.mu.Lock()
	before := len(f.syncs)
	f.mu.Unlock()
	time.Sleep(300 * time.Millisecond)
	cancel()
	<-done
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, s := range f.syncs {
		if s == "OPEN" {
			t.Fatalf("sync %d ran while Helium was open: %v", i, f.syncs)
		}
	}
	if len(f.syncs) < 2 {
		t.Fatalf("expected periodic syncs while closed, got %v", f.syncs)
	}
	if len(f.syncs) != before {
		t.Fatalf("syncs continued after Helium reopened: %d -> %d", before, len(f.syncs))
	}
}

func TestOnlyOneAgent(t *testing.T) {
	l, err := Lock()
	if err != nil {
		t.Skip("lock port busy on this machine:", err)
	}
	defer l.Close()
	if _, err := Lock(); err != ErrAlreadyRunning {
		t.Fatalf("a second agent must be refused, got %v", err)
	}
}

// Before the first-sync preview is approved, the agent must do nothing.
func TestAgentWaitsForSetup(t *testing.T) {
	f := &fake{open: false}
	a := newTestAgent(f)
	approved := false
	inner := a.Sync
	a.Sync = func(ctx context.Context) (engine.Report, error) {
		if !approved {
			return engine.Report{}, ErrNotSetUp
		}
		return inner(ctx)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	time.Sleep(300 * time.Millisecond)
	f.mu.Lock()
	n := len(f.syncs)
	f.mu.Unlock()
	if n != 0 {
		t.Fatalf("synced %d time(s) before setup was approved", n)
	}
	if st := a.Status.Snapshot(); st.LastError != "" {
		t.Fatalf("waiting for setup is not an error: %q", st.LastError)
	}
	cancel()
	<-done
}

// A second copy of the app asks the first to show its window; websites
// and other requests cannot.
func TestShowHandoff(t *testing.T) {
	l, err := Lock()
	if err != nil {
		t.Skip("lock port busy on this machine:", err)
	}
	defer l.Close()
	shown := make(chan struct{}, 4)
	go ServeShow(l, func() { shown <- struct{}{} })
	if err := AskToShow(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-shown:
	case <-time.After(2 * time.Second):
		t.Fatal("window was not shown")
	}
	for _, h := range []map[string]string{{}, {"X-Helium-Syncer": "nope"}} {
		req, _ := http.NewRequest(http.MethodPost, "http://"+LockAddr+"/show", nil)
		for k, v := range h {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden {
			t.Fatalf("request without the header got %d", res.StatusCode)
		}
	}
	req, _ := http.NewRequest(http.MethodPost, "http://"+LockAddr+"/show", nil)
	req.Header.Set("X-Helium-Syncer", "show")
	req.Host = "evil.example"
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != http.StatusForbidden {
		t.Fatalf("other host name must be refused: %v %v", err, res)
	}
	if len(shown) != 0 {
		t.Fatal("a refused request showed the window")
	}
}

type powerBox struct {
	mu sync.Mutex
	s  power.State
}

func (p *powerBox) set(s power.State) { p.mu.Lock(); p.s = s; p.mu.Unlock() }
func (p *powerBox) get() power.State  { p.mu.Lock(); defer p.mu.Unlock(); return p.s }

var (
	pluggedIn  = power.State{HasBattery: true, Percent: 80}
	onBattery  = power.State{HasBattery: true, OnBattery: true, Percent: 70}
	lowBattery = power.State{HasBattery: true, OnBattery: true, Percent: 15}
)

func count(f *fake) int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.syncs) }

// batteryAgent re-reads the power state on every check (no 1-minute cache).
func batteryAgent(f *fake, pb *powerBox, mode string) *Agent {
	a := newTestAgent(f)
	a.Timing.WhileClosed = 40 * time.Millisecond
	a.Timing.WhileClosedBattery = 10 * time.Second
	a.Power = pb.get
	a.Mode = func() string { return mode }
	a.Now = func() time.Time { a.pwRead = time.Time{}; return time.Now() }
	return a
}

func runFor(a *Agent, d time.Duration, during func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	if during != nil {
		during()
	}
	time.Sleep(d)
	cancel()
	<-done
}

func TestBatteryPullsLessOften(t *testing.T) {
	f, pb := &fake{}, &powerBox{s: onBattery}
	runFor(batteryAgent(f, pb, BatteryBalanced), 400*time.Millisecond, nil)
	if n := count(f); n != 1 {
		t.Fatalf("on battery: %d syncs in 400 ms, want only the first one", n)
	}
	f2, pb2 := &fake{}, &powerBox{s: pluggedIn}
	runFor(batteryAgent(f2, pb2, BatteryBalanced), 400*time.Millisecond, nil)
	if n := count(f2); n < 4 {
		t.Fatalf("plugged in: only %d syncs in 400 ms", n)
	}
	f3, pb3 := &fake{}, &powerBox{s: onBattery}
	runFor(batteryAgent(f3, pb3, BatteryAlways), 400*time.Millisecond, nil)
	if n := count(f3); n < 4 {
		t.Fatalf("mode always on battery: only %d syncs in 400 ms", n)
	}
}

func TestLowBatteryStillSendsAfterQuit(t *testing.T) {
	f, pb := &fake{open: true}, &powerBox{s: lowBattery}
	a := batteryAgent(f, pb, BatteryBalanced)
	a.Timing.WhileClosedBattery = 40 * time.Millisecond // so only the low-battery rule can stop pulls
	runFor(a, 500*time.Millisecond, func() {
		time.Sleep(60 * time.Millisecond)
		f.setOpen(false)
	})
	if n := count(f); n != 1 {
		t.Fatalf("low battery: %d syncs, want exactly the one after quit", n)
	}
}

func TestPluggedModeWaitsForPower(t *testing.T) {
	f, pb := &fake{open: true}, &powerBox{s: onBattery}
	a := batteryAgent(f, pb, BatteryPlugged)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	time.Sleep(40 * time.Millisecond)
	f.setOpen(false)
	time.Sleep(250 * time.Millisecond)
	if n := count(f); n != 0 {
		t.Fatalf("plugged-only mode on battery synced %d time(s)", n)
	}
	if st := a.Status.Snapshot(); st.PowerNote == "" {
		t.Fatal("no note that the sync waits for power")
	}
	select { // a person presses Sync now: do it anyway
	case a.Trigger <- struct{}{}:
	default:
	}
	time.Sleep(80 * time.Millisecond)
	if n := count(f); n != 1 {
		t.Fatalf("Sync now on battery: %d syncs, want 1", n)
	}
	f.setOpen(true)
	time.Sleep(40 * time.Millisecond)
	f.setOpen(false)
	time.Sleep(150 * time.Millisecond)
	if n := count(f); n != 1 {
		t.Fatalf("second quit on battery synced: %d", n)
	}
	pb.set(pluggedIn) // plugged in: the waiting changes go out
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done
	if n := count(f); n < 2 {
		t.Fatalf("after plugging in: %d syncs, want the waiting one", n)
	}
	for i, s := range f.syncs {
		if s == "OPEN" {
			t.Fatalf("sync %d ran while Helium was open", i)
		}
	}
}
