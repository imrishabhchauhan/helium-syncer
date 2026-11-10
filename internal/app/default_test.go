package app

import (
	"os"
	"path/filepath"
	"testing"
)

// A new computer starts with history off; a computer that already has
// settings keeps them, history included.
func TestHistoryOffOnNewComputerOnly(t *testing.T) {
	t.Setenv("HELIUM_SYNCER_HOME", t.TempDir())
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !c.IsOff("history") || c.IsOff("bookmarks") || c.IsOff("tabs") || c.IsOff("extensions") {
		t.Fatalf("new computer: off = %v, want only history", c.Off)
	}
	// Turned on during setup: stays on.
	c.Off = nil
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if c, _ := Load(); c.IsOff("history") {
		t.Fatal("history switched on was switched off again")
	}
	// Settings from before this default keep syncing what they synced.
	os.WriteFile(filepath.Join(Dir(), "config.json"), []byte(`{"server":"https://x.example"}`), 0o600)
	if c, _ := Load(); c.IsOff("history") {
		t.Fatal("existing settings were changed by the new default")
	}
}
