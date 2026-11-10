package engine

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// countingTransport counts the version checks a sync asks the server for.
type countingTransport struct {
	Transport
	versions, announces int
}

func (c *countingTransport) Versions(ctx context.Context, except string) (map[string]string, error) {
	c.versions++
	return c.Transport.Versions(ctx, except)
}

func (c *countingTransport) Announce(ctx context.Context, d, n, v string) error {
	c.announces++
	return c.Transport.Announce(ctx, d, n, v)
}

// While Helium is open, a sync is one request: a version check that passed
// in the last half hour holds. Closed syncs always check.
func TestLiveSyncReusesVersionCheck(t *testing.T) {
	ctx := context.Background()
	ud := fakeUserData(t, ver)
	cfg := cfgFor(t, "mac", ud, NewMemory())
	ct := &countingTransport{Transport: cfg.Transport}
	cfg.Transport = ct
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	cfg.Now = func() time.Time { return now }
	LinkProfile(cfg.StateDir, cfg.DeviceID, "Default")
	if _, err := Run(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if ct.versions != 1 || ct.announces != 1 {
		t.Fatalf("closed sync: %d checks, %d announces, want 1 and 1", ct.versions, ct.announces)
	}
	open := cfg
	open.WhileOpen = true
	open.Running = func(string) (bool, error) { return true, nil }
	open.LiveTabs = &browserTabs{dir: filepath.Join(ud, "Default"), sess: session("https://a.example/")}
	for i := 0; i < 3; i++ {
		now = now.Add(time.Minute)
		if _, err := Run(ctx, open); err != nil {
			t.Fatal(err)
		}
	}
	if ct.versions != 1 || ct.announces != 1 {
		t.Fatalf("live syncs asked again: %d checks, %d announces", ct.versions, ct.announces)
	}
	now = now.Add(GateReuse)
	if _, err := Run(ctx, open); err != nil {
		t.Fatal(err)
	}
	if ct.versions != 2 {
		t.Fatalf("after %s the check must run again: %d checks", GateReuse, ct.versions)
	}
	if _, err := Run(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if ct.versions != 3 {
		t.Fatalf("a closed sync must always check: %d checks", ct.versions)
	}
}
