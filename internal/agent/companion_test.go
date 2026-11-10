package agent

import (
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
