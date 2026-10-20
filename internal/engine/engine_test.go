package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imrishabhchauhan/helium-syncer/internal/bookmarks"
)

// fakeUserData builds a minimal closed Helium folder with one profile.
func fakeUserData(t *testing.T, version string) string {
	ud := t.TempDir()
	os.WriteFile(filepath.Join(ud, "Last Version"), []byte(version), 0o600)
	os.WriteFile(filepath.Join(ud, "Local State"), []byte(`{"profile":{"info_cache":{"Default":{"name":"Personal"}}}}`), 0o600)
	os.MkdirAll(filepath.Join(ud, "Default"), 0o700)
	os.WriteFile(filepath.Join(ud, "Default", "Preferences"), []byte(`{"profile":{"name":"Personal"}}`), 0o600)
	os.WriteFile(filepath.Join(ud, "Default", "Secure Preferences"), []byte(`{"protection":{"macs":{}}}`), 0o600)
	os.WriteFile(filepath.Join(ud, "Default", "Bookmarks"), []byte(emptyBookmarks), 0o600)
	return ud
}

func cfgFor(t *testing.T, name, ud string, tr Transport) Config {
	return Config{UserData: ud, StateDir: filepath.Join(t.TempDir(), "syncer"), DeviceID: name, DeviceName: name,
		MachineID: "S-1-5-21-1-2-3", Transport: tr, RestoreTabs: true}
}

func addBookmark(t *testing.T, ud, guid, title string) {
	path := filepath.Join(ud, "Default", "Bookmarks")
	f, err := bookmarks.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	bar := f.Root("bookmark_bar")
	bar["children"] = append(bar["children"].([]any), map[string]any{"id": "100", "guid": guid, "name": title,
		"type": "url", "url": "https://" + guid + ".example/", "date_added": "1"})
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}
}

func titles(t *testing.T, ud, dir string) string {
	f, err := bookmarks.Load(filepath.Join(ud, dir, "Bookmarks"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	f.Walk(func(n map[string]any, d int) {
		if d > 0 {
			out = append(out, n["name"].(string))
		}
	})
	return strings.Join(out, ",")
}

const ver = "155.0.8059.84"

func TestRecoversFromInterruptedSync(t *testing.T) {
	ctx := context.Background()
	ud := fakeUserData(t, ver)
	cfg := cfgFor(t, "mac", ud, NewMemory())
	if _, err := LinkProfile(cfg.StateDir, cfg.DeviceID, "Default"); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	good, _ := os.ReadFile(filepath.Join(ud, "Default", "Bookmarks"))

	// Simulate a sync cut off by a power loss: backup + journal written,
	// Bookmarks half-written, a new session file and a new profile created.
	bk, err := backup(cfg.StateDir, ud, []string{"Default"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	strayProfile := filepath.Join(ud, "Profile 9")
	straySession := filepath.Join(ud, "Default", "Sessions", "Session_99999999999999999")
	os.MkdirAll(strayProfile, 0o700)
	os.MkdirAll(filepath.Dir(straySession), 0o700)
	os.WriteFile(straySession, []byte("partial"), 0o600)
	if err := writeJournal(cfg.StateDir, journal{Backup: bk, Created: []string{strayProfile, straySession}}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(ud, "Default", "Bookmarks"), []byte(`{"roots": {"bookmark_ba`), 0o600)

	if _, err := Run(ctx, cfg); err != nil {
		t.Fatalf("run after the interruption: %v", err)
	}
	if now, _ := os.ReadFile(filepath.Join(ud, "Default", "Bookmarks")); !bytes.Equal(now, good) {
		t.Fatalf("Bookmarks not restored:\n%s", now)
	}
	if exists(strayProfile) || exists(straySession) {
		t.Fatal("files created by the interrupted sync were not removed")
	}
	if exists(filepath.Join(cfg.StateDir, journalFile)) {
		t.Fatal("journal left behind")
	}
}

func TestFailedApplyRollsBack(t *testing.T) {
	ctx := context.Background()
	ud := fakeUserData(t, ver)
	cfg := cfgFor(t, "mac", ud, NewMemory())
	LinkProfile(cfg.StateDir, cfg.DeviceID, "Default")
	secureBefore, _ := os.ReadFile(filepath.Join(ud, "Default", "Secure Preferences"))
	// Settings are written first (RestoreTabs changes them); then a broken
	// Bookmarks file makes the run fail. Everything must be put back.
	os.WriteFile(filepath.Join(ud, "Default", "Bookmarks"), []byte(`not json`), 0o600)
	if _, err := Run(ctx, cfg); err == nil {
		t.Fatal("expected the run to fail on a broken Bookmarks file")
	}
	if now, _ := os.ReadFile(filepath.Join(ud, "Default", "Secure Preferences")); !bytes.Equal(now, secureBefore) {
		t.Fatalf("Secure Preferences not rolled back:\n%s", now)
	}
	if now, _ := os.ReadFile(filepath.Join(ud, "Default", "Bookmarks")); string(now) != "not json" {
		t.Fatal("Bookmarks must be left exactly as found")
	}
}

func TestOfflineChangesWaitAndAreSentOnce(t *testing.T) {
	ctx := context.Background()
	server := NewMemory()
	macUD, winUD := fakeUserData(t, ver), fakeUserData(t, ver)
	mac, win := cfgFor(t, "mac", macUD, server), cfgFor(t, "win", winUD, server)
	LinkProfile(mac.StateDir, mac.DeviceID, "Default")
	Run(ctx, mac)
	addBookmark(t, macUD, "offline", "Made while offline")
	server.FailPush = true
	rep, err := Run(ctx, mac)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Sent != 0 {
		t.Fatalf("nothing should be sent while offline, sent %d", rep.Sent)
	}
	before := server.Ops()
	if _, err := Run(ctx, mac); err != nil { // back online
		t.Fatal(err)
	}
	sent := server.Ops() - before
	if sent != 1 {
		t.Fatalf("the queued change should be sent exactly once, server got %d ops", sent)
	}
	if _, err := Run(ctx, win); err != nil {
		t.Fatal(err)
	}
	if got := titles(t, winUD, "Profile 1"); !strings.Contains(got, "Made while offline") {
		t.Fatalf("Windows did not get the offline change: %s", got)
	}
}

func TestVersionGatePausesWithoutTouchingFiles(t *testing.T) {
	ctx := context.Background()
	server := NewMemory()
	server.Announce(ctx, "mac", "Rishabh's MacBook Air", "155.9.9999.1")
	ud := fakeUserData(t, ver)
	cfg := cfgFor(t, "win", ud, server)
	snap := func() string {
		var b strings.Builder
		filepath.Walk(ud, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				data, _ := os.ReadFile(p)
				b.WriteString(p + "=" + string(data) + "\n")
			}
			return nil
		})
		return b.String()
	}
	before := snap()
	rep, err := Run(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep.Paused, "Rishabh's MacBook Air") {
		t.Fatalf("pause message should name the other device: %q", rep.Paused)
	}
	if snap() != before {
		t.Fatal("a paused sync changed Helium's files")
	}
}
