//go:build helium

// End-to-end test against a real Helium install, on throwaway profiles.
// Needs the fixture profile test-profiles/ext-src with Dark Reader installed
// from the Web Store (see docs/02-phase0-findings.md); skipped without it.
// Run: go test -tags helium ./internal/extensions/
package extensions

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/imrishabhchauhan/helium-syncer/internal/cdp"
	"github.com/imrishabhchauhan/helium-syncer/internal/deviceid"
	"github.com/imrishabhchauhan/helium-syncer/internal/testkit"
)

const darkReader = "eimadpbcbfnmbkopoojfekhnkhdbieeh"

func heliumExe(t *testing.T) string {
	if p := os.Getenv("HELIUM_EXE"); p != "" {
		return p
	}
	switch runtime.GOOS {
	case "windows":
		return `C:\Program Files\imput\Helium\Application\chrome.exe`
	case "darwin":
		return "/Applications/Helium.app/Contents/MacOS/Helium"
	}
	t.Skip("no Helium for this OS")
	return ""
}

func TestTransferExtensionWithHelium(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	root, _ := filepath.Abs("../..")
	src := filepath.Join(root, "test-profiles", "ext-src", "Default")
	if _, err := os.Stat(filepath.Join(src, "Extensions", darkReader)); err != nil {
		t.Skip("fixture test-profiles/ext-src with Dark Reader not present")
	}
	exts, err := List(src)
	if err != nil {
		t.Fatal(err)
	}
	var syncable []string
	for _, e := range exts {
		if e.Syncable() {
			syncable = append(syncable, e.Name+" "+e.Version)
		}
	}
	t.Logf("source extensions to sync: %v", syncable)

	// 1. A fresh target profile, created by Helium itself.
	ud := filepath.Join(root, "test-profiles", fmt.Sprintf("it-ext-dst-%d", time.Now().Unix()))
	opts := testkit.Options{Exe: heliumExe(t), UserDataDir: ud, Port: 9355, ProbeDir: filepath.Join(root, "testing", "probe-extension")}
	in, err := testkit.Start(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := in.Close(ctx); err != nil {
		t.Fatal(err)
	}

	// 2. Move Dark Reader with its data.
	dev, err := deviceid.Get()
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(ud, "Default")
	if err := Transfer(src, dst, darkReader, dev, runtime.GOOS); err != nil {
		t.Fatal(err)
	}

	// 3. Helium must load it, enabled, with its data.
	in, err = testkit.Start(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	var list []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Version string `json:"version"`
		Enabled bool   `json:"enabled"`
	}
	if err := in.Eval(ctx, "probe.extensions()", &list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range list {
		if e.ID == darkReader {
			found = true
			if !e.Enabled {
				t.Errorf("Dark Reader is installed but disabled")
			}
			t.Logf("target has %s %s enabled=%v", e.Name, e.Version, e.Enabled)
		}
	}
	if !found {
		t.Fatalf("Dark Reader missing in target; Helium has %v", list)
	}
	worker := waitWorker(ctx, t, opts.Port, darkReader)
	defer worker.Close()
	var local, sync map[string]any
	if err := worker.Eval(ctx, "chrome.storage.local.get(['hsTest','hsNum'])", &local); err != nil {
		t.Fatal(err)
	}
	if err := worker.Eval(ctx, "chrome.storage.sync.get('hsSync')", &sync); err != nil {
		t.Fatal(err)
	}
	if local["hsTest"] != "हिंदी ✓ 🚀" || fmt.Sprint(local["hsNum"]) != "42" {
		t.Errorf("chrome.storage.local not carried over: %v", local)
	}
	if sync["hsSync"] != "sync ✓" {
		t.Errorf("chrome.storage.sync not carried over: %v", sync)
	}
	// Give Helium's background content verification time to run, then
	// restart once more: the extension must still be there and enabled.
	time.Sleep(8 * time.Second)
	worker.Close()
	if err := in.Close(ctx); err != nil {
		t.Fatal(err)
	}
	in, err = testkit.Start(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Second)
	list = nil
	if err := in.Eval(ctx, "probe.extensions()", &list); err != nil {
		t.Fatal(err)
	}
	ok := false
	for _, e := range list {
		if e.ID == darkReader && e.Enabled {
			ok = true
		}
	}
	if !ok {
		t.Errorf("after a second restart Dark Reader is gone or disabled: %v", list)
	}
	if err := in.Close(ctx); err != nil {
		t.Fatal(err)
	}
	after, _ := List(dst)
	for _, e := range after {
		if e.ID == darkReader && e.Disabled {
			t.Errorf("Helium recorded disable reasons for Dark Reader: %v", e.settingsRaw["disable_reasons"])
		}
	}
}

func waitWorker(ctx context.Context, t *testing.T, port int, id string) *cdp.Client {
	for i := 0; i < 80; i++ {
		ts, _ := cdp.Targets(ctx, port)
		for _, tg := range ts {
			if tg.Type == "service_worker" && strings.Contains(tg.URL, id) {
				c, err := cdp.Dial(ctx, tg.WSURL)
				if err != nil {
					t.Fatal(err)
				}
				// The worker target appears before extension APIs are bound.
				for j := 0; j < 40; j++ {
					var kind string
					if c.Eval(ctx, "typeof chrome !== 'undefined' && chrome.storage ? 'ready' : 'wait'", &kind) == nil && kind == "ready" {
						return c
					}
					time.Sleep(250 * time.Millisecond)
				}
				c.Close()
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("extension service worker did not start")
	return nil
}
