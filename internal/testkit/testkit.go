// Package testkit starts throwaway Helium instances for tests and drives them
// through the probe extension. It refuses to use any folder outside a
// "test-profiles" directory, so a real profile can never be opened by a test.
package testkit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/imrishabhchauhan/helium-syncer/internal/cdp"
)

// Instance is one running throwaway Helium.
type Instance struct {
	UserDataDir string
	Port        int
	cmd         *exec.Cmd
	probe       *cdp.Client
}

// Options for Start.
type Options struct {
	Exe         string // Helium executable
	UserDataDir string // must contain a "test-profiles" path element
	Port        int    // remote debugging port
	ProbeDir    string // unpacked probe extension; empty = don't load
	NoURL       bool   // don't pass about:blank, so the startup setting (e.g. restore tabs) decides
	ProfileDir  string // e.g. "Profile 1"; empty = Helium's last used profile
}

// Start launches Helium and waits until the probe service worker is reachable.
func Start(ctx context.Context, o Options) (*Instance, error) {
	if err := guard(o.UserDataDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(o.UserDataDir, 0o755); err != nil {
		return nil, err
	}
	args := []string{
		"--user-data-dir=" + o.UserDataDir,
		fmt.Sprintf("--remote-debugging-port=%d", o.Port),
		"--no-first-run", "--no-default-browser-check", "--window-size=900,600",
	}
	if o.ProbeDir != "" {
		args = append(args, "--load-extension="+o.ProbeDir)
	}
	if o.ProfileDir != "" {
		args = append(args, "--profile-directory="+o.ProfileDir)
	}
	if !o.NoURL {
		args = append(args, "about:blank")
	}
	cmd := exec.Command(o.Exe, args...)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	in := &Instance{UserDataDir: o.UserDataDir, Port: o.Port, cmd: cmd}
	if o.ProbeDir == "" {
		return in, in.waitDevTools(ctx)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		ts, err := cdp.Targets(ctx, o.Port)
		if err == nil {
			for _, t := range ts {
				if t.Type == "service_worker" && strings.HasSuffix(t.URL, "/probe.js") {
					c, err := cdp.Dial(ctx, t.WSURL)
					if err != nil {
						return nil, err
					}
					in.probe = c
					return in, in.Eval(ctx, "typeof probe", nil)
				}
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	in.Kill()
	return nil, errors.New("testkit: probe extension did not start")
}

func (in *Instance) waitDevTools(ctx context.Context) error {
	for i := 0; i < 100; i++ {
		if _, err := cdp.BrowserWSURL(ctx, in.Port); err == nil {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return errors.New("testkit: DevTools did not come up")
}

// Eval runs JavaScript in the probe service worker (e.g. "probe.snapshot()").
func (in *Instance) Eval(ctx context.Context, expr string, out any) error {
	if in.probe == nil {
		return errors.New("testkit: started without probe")
	}
	return in.probe.Eval(ctx, expr, out)
}

// Close quits Helium cleanly (it writes sessions, bookmarks, prefs) and waits.
func (in *Instance) Close(ctx context.Context) error {
	if in.probe != nil {
		in.probe.Close()
	}
	ws, err := cdp.BrowserWSURL(ctx, in.Port)
	if err == nil {
		if c, err := cdp.Dial(ctx, ws); err == nil {
			c.Call(ctx, "Browser.close", map[string]any{})
			c.Close()
		}
	}
	done := make(chan error, 1)
	go func() { done <- in.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		in.Kill()
		return errors.New("testkit: Helium did not quit within 30s")
	}
	// Helium child processes may hold files a moment longer.
	return waitUnlocked(in.UserDataDir, 15*time.Second)
}

// Kill force-stops the instance (only for cleanup after a failure).
func (in *Instance) Kill() {
	if in.cmd != nil && in.cmd.Process != nil {
		in.cmd.Process.Kill()
	}
}

// waitUnlocked waits until no Helium process holds the profile's lock.
func waitUnlocked(ud string, d time.Duration) error {
	lock := filepath.Join(ud, "lockfile")
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if err := os.Remove(lock); err == nil || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("testkit: %s still locked", ud)
}

func guard(ud string) error {
	abs, err := filepath.Abs(ud)
	if err != nil {
		return err
	}
	for _, part := range strings.Split(filepath.ToSlash(abs), "/") {
		if part == "test-profiles" {
			return nil
		}
	}
	return fmt.Errorf("testkit: refusing %q: test instances may only use a test-profiles folder", ud)
}
