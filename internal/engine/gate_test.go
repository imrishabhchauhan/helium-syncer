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

// While Helium is open, a sync is one request: this computer's version is
// told to the server at most every half hour. Nothing asks for the other
// computers' versions any more (there is no version gate).
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
	if ct.versions != 0 || ct.announces != 1 {
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
	if ct.versions != 0 || ct.announces != 1 {
		t.Fatalf("live syncs asked again: %d checks, %d announces", ct.versions, ct.announces)
	}
	now = now.Add(GateReuse)
	if _, err := Run(ctx, open); err != nil {
		t.Fatal(err)
	}
	if ct.announces != 2 || ct.versions != 0 {
		t.Fatalf("after %s the version must be told again: %d announces, %d checks", GateReuse, ct.announces, ct.versions)
	}
	if _, err := Run(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if ct.announces != 3 || ct.versions != 0 {
		t.Fatalf("a closed sync always tells its version, and nothing asks for others': %d announces, %d checks", ct.announces, ct.versions)
	}
}
