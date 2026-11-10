package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/imrishabhchauhan/helium-syncer/internal/extensions"
)

func companionProfiles(t *testing.T, n int) []string {
	t.Helper()
	root := t.TempDir()
	var dirs []string
	for i := 0; i < n; i++ {
		d := filepath.Join(root, "Profile "+string(rune('1'+i)))
		os.MkdirAll(d, 0o700)
		os.WriteFile(filepath.Join(d, "Secure Preferences"), []byte(`{"protection":{"macs":{}}}`), 0o600)
		dirs = append(dirs, d)
	}
	return dirs
}

// After a quit, every synced profile gets the extension, and a second quit
// changes nothing.
func TestCompanionInstallsOnceInEveryProfile(t *testing.T) {
	dirs := companionProfiles(t, 2)
	c := Companion{Dir: filepath.Join(t.TempDir(), "extension"), MachineID: "TEST", GOOS: runtime.GOOS,
		Running: func() (bool, error) { return false, nil }}

	done, err := c.Ensure(dirs)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 2 {
		t.Fatalf("installed in %d profile(s), want 2", len(done))
	}
	for _, d := range dirs {
		if !extensions.LocalInstalled(d, ExtensionID, c.Dir, c.GOOS) {
			t.Errorf("%s: not installed so that Helium runs it", filepath.Base(d))
		}
	}
	if _, err := os.Stat(filepath.Join(c.Dir, "manifest.json")); err != nil {
		t.Error("the extension's files were not written")
	}
	again, err := c.Ensure(dirs)
	if err != nil || len(again) != 0 {
		t.Errorf("second run installed %d again (err %v); it must leave a done profile alone", len(again), err)
	}
}

// If Helium opens part-way, nothing more is written: Helium would throw it
// away, and could be mid-write itself.
func TestCompanionStopsWhenHeliumOpens(t *testing.T) {
	dirs := companionProfiles(t, 2)
	calls := 0
	c := Companion{Dir: filepath.Join(t.TempDir(), "extension"), MachineID: "TEST", GOOS: runtime.GOOS,
		Running: func() (bool, error) { calls++; return calls > 1, nil }}

	done, err := c.Ensure(dirs)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 {
		t.Fatalf("installed in %d profile(s) with Helium opening after the first, want 1", len(done))
	}
	if extensions.LocalInstalled(dirs[1], ExtensionID, c.Dir, c.GOOS) {
		t.Error("wrote to a profile while Helium was open")
	}
}

// Helium keeps running a cached background script after the file changes, so
// each version's script must have its own name, and the manifest must point
// at it. Older scripts are removed.
func TestCompanionScriptIsNamedForItsVersion(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "extension")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "background.js"), []byte("old"), 0o600)
	os.WriteFile(filepath.Join(dir, "background-0.0.9.js"), []byte("older"), 0o600)
	c := Companion{Dir: dir, MachineID: "TEST", GOOS: runtime.GOOS, Running: func() (bool, error) { return false, nil }}
	if err := c.WriteFiles(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Version    string `json:"version"`
		Key        string `json:"key"`
		Background struct {
			ServiceWorker string `json:"service_worker"`
		} `json:"background"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	want := "background-" + m.Version + ".js"
	if m.Background.ServiceWorker != want {
		t.Fatalf("manifest service_worker %q, want %q", m.Background.ServiceWorker, want)
	}
	if m.Key == "" {
		t.Error("the manifest lost its key, so the extension id would change")
	}
	if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
		t.Errorf("%s was not written", want)
	}
	left, _ := filepath.Glob(filepath.Join(dir, "background*.js"))
	if len(left) != 1 {
		t.Errorf("scripts left: %v, want only %s", left, want)
	}
}

// A new version that asks for more permissions (0.2.0 added tabs) is
// registered again after the next quit, so Helium grants them without
// asking the person.
func TestCompanionReinstallsForNewPermissions(t *testing.T) {
	dirs := companionProfiles(t, 1)
	c := Companion{Dir: filepath.Join(t.TempDir(), "extension"), MachineID: "TEST", GOOS: runtime.GOOS,
		Running: func() (bool, error) { return false, nil }}
	if _, err := c.Ensure(dirs); err != nil {
		t.Fatal(err)
	}
	// An older version had fewer permissions.
	p := filepath.Join(c.Dir, "manifest.json")
	raw, _ := os.ReadFile(p)
	var m map[string]any
	json.Unmarshal(raw, &m)
	full := m["permissions"]
	m["permissions"] = []any{"alarms", "bookmarks"}
	old, _ := json.Marshal(m)
	os.WriteFile(p, old, 0o600)
	if err := extensions.InstallLocal(dirs[0], ExtensionID, c.Dir, c.MachineID, c.GOOS); err != nil {
		t.Fatal(err)
	}
	m["permissions"] = full
	cur, _ := json.Marshal(m)
	os.WriteFile(p, cur, 0o600)
	if extensions.LocalInstalled(dirs[0], ExtensionID, c.Dir, c.GOOS) {
		t.Fatal("an install missing the new permissions counts as installed")
	}
	done, err := c.Ensure(dirs)
	if err != nil || len(done) != 1 {
		t.Fatalf("reinstalled %d (err %v), want 1", len(done), err)
	}
	if !extensions.LocalInstalled(dirs[0], ExtensionID, c.Dir, c.GOOS) {
		t.Fatal("still not installed with every permission")
	}
}
