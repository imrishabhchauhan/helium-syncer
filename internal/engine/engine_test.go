package engine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imrishabhchauhan/helium-syncer/internal/bookmarks"
	"github.com/imrishabhchauhan/helium-syncer/internal/chromejson"
	"github.com/imrishabhchauhan/helium-syncer/internal/extensions"
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

func TestDryRunTouchesNothing(t *testing.T) {
	ctx := context.Background()
	server := NewMemory()
	macUD, winUD := fakeUserData(t, ver), fakeUserData(t, ver)
	mac, win := cfgFor(t, "mac", macUD, server), cfgFor(t, "win", winUD, server)
	LinkProfile(mac.StateDir, mac.DeviceID, "Default")
	addBookmark(t, macUD, "dry", "Only in a dry run")

	snap := func(dir string) string {
		var b strings.Builder
		filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				data, _ := os.ReadFile(p)
				b.WriteString(p + "=" + string(data) + "\n")
			}
			return nil
		})
		return b.String()
	}
	before, stateBefore := snap(macUD), snap(mac.StateDir)
	rep, err := DryRun(ctx, mac)
	if err != nil {
		t.Fatal(err)
	}
	if snap(macUD) != before || snap(mac.StateDir) != stateBefore {
		t.Fatal("a dry run changed real files")
	}
	if server.Ops() != 0 {
		t.Fatalf("a dry run sent %d ops", server.Ops())
	}
	if rep.SentByType["bookmark"] == 0 {
		t.Fatalf("the dry run should report the bookmark it would send: %+v", rep.Report)
	}
	t.Logf("mac dry run: would change %v, would create %v, would send %v", rep.WouldChange, rep.WouldCreate, rep.SentByType)

	// Real Mac sync, then a dry run on Windows shows the new profile.
	if _, err := Run(ctx, mac); err != nil {
		t.Fatal(err)
	}
	wrep, err := DryRun(ctx, win)
	if err != nil {
		t.Fatal(err)
	}
	if len(wrep.ProfilesAdded) != 1 || len(wrep.WouldCreate) == 0 {
		t.Fatalf("Windows dry run should show one new profile: %+v", wrep)
	}
	if exists(filepath.Join(winUD, "Profile 1")) {
		t.Fatal("the dry run created a real profile")
	}
}

// The first sync of an already set-up device must leave its files alone.
func TestFirstSyncOfSetUpDeviceWritesNothing(t *testing.T) {
	ctx := context.Background()
	ud := fakeUserData(t, ver)
	addBookmark(t, ud, "keep", "Existing bookmark")
	cfg := cfgFor(t, "mac", ud, NewMemory())
	cfg.RestoreTabs = false
	LinkProfile(cfg.StateDir, cfg.DeviceID, "Default")
	rep, err := Run(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Written) != 0 {
		t.Fatalf("first sync of the Mac must not write any Helium file, wrote %v", rep.Written)
	}
}

const darkReader = "eimadpbcbfnmbkopoojfekhnkhdbieeh"

// installFakeExtension registers a Web Store extension with some data.
func installFakeExtension(t *testing.T, profile, version, data string) {
	ver := filepath.Join(profile, "Extensions", darkReader, version+"_0")
	os.MkdirAll(ver, 0o700)
	os.WriteFile(filepath.Join(ver, "manifest.json"), []byte(`{"name":"Dark Reader","version":"`+version+`"}`), 0o600)
	d := filepath.Join(profile, "Local Extension Settings", darkReader)
	os.MkdirAll(d, 0o700)
	os.WriteFile(filepath.Join(d, "000003.log"), []byte(data), 0o600)
	path := filepath.Join(profile, "Secure Preferences")
	doc, _ := readDoc(path, `{"protection":{"macs":{}}}`)
	m, _ := chromejson.Decode([]byte(`{"location":1,"from_webstore":true,"path":"` + darkReader + `\\` + version + `_0",` +
		`"manifest":{"name":"Dark Reader","version":"` + version + `"},"disable_reasons":[]}`))
	chromejson.Set(doc, "extensions.settings."+darkReader, m)
	os.WriteFile(path, []byte(chromejson.Compact(doc)), 0o600)
}

func TestExtensionsFollowAcrossDevices(t *testing.T) {
	ctx := context.Background()
	server := NewMemory()
	macUD, winUD := fakeUserData(t, ver), fakeUserData(t, ver)
	mac, win := cfgFor(t, "mac", macUD, server), cfgFor(t, "win", winUD, server)
	mac.GOOS, win.GOOS = "darwin", "windows"
	macP := filepath.Join(macUD, "Default")
	installFakeExtension(t, macP, "4.9.133", "theme=dark हिंदी")
	LinkProfile(mac.StateDir, mac.DeviceID, "Default")
	mustRun := func(c Config) Report {
		t.Helper()
		r, err := Run(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	mustRun(mac)
	puts := server.BlobPuts
	mustRun(mac)
	if server.BlobPuts != puts {
		t.Fatalf("a second sync re-uploaded %d blob(s)", server.BlobPuts-puts)
	}

	winP := filepath.Join(winUD, "Profile 1")
	mustRun(win)
	list, _ := extensions.List(winP)
	if len(list) != 1 || list[0].Version != "4.9.133" || list[0].Disabled {
		t.Fatalf("Windows should have Dark Reader 4.9.133 enabled: %+v", list)
	}
	if data, _ := os.ReadFile(filepath.Join(winP, "Local Extension Settings", darkReader, "000003.log")); string(data) != "theme=dark हिंदी" {
		t.Fatalf("extension data not carried: %q", data)
	}
	if n := mustRun(win).Sent; n != 0 {
		t.Fatalf("Windows should have nothing new to send after installing, sent %d", n)
	}

	// Disable on the Mac and change its data: both follow.
	extensions.SetEnabled(macP, darkReader, false, "x")
	os.WriteFile(filepath.Join(macP, "Local Extension Settings", darkReader, "000003.log"), []byte("theme=light"), 0o600)
	mustRun(mac)
	mustRun(win)
	list, _ = extensions.List(winP)
	if len(list) != 1 || !list[0].Disabled {
		t.Fatalf("disable did not follow: %+v", list)
	}
	if data, _ := os.ReadFile(filepath.Join(winP, "Local Extension Settings", darkReader, "000003.log")); string(data) != "theme=light" {
		t.Fatalf("data change did not follow: %q", data)
	}

	// Uninstall on the Mac: removed on Windows, with a copy in the backup.
	extensions.Remove(macP, darkReader, "x")
	mustRun(mac)
	rep := mustRun(win)
	if list, _ := extensions.List(winP); len(list) != 0 {
		t.Fatalf("uninstall did not follow: %+v", list)
	}
	found := false
	filepath.Walk(filepath.Join(win.StateDir, "backups"), func(p string, info os.FileInfo, err error) error {
		if err == nil && strings.Contains(p, darkReader) && strings.HasSuffix(p, "manifest.json") {
			found = true
		}
		return nil
	})
	if !found {
		t.Fatal("the removed extension should be kept in the sync backup")
	}
	t.Logf("last Windows run wrote %v", rep.Written)
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

func TestOnlyOneSyncAtATime(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockState(dir); err != ErrBusy {
		t.Fatalf("a second sync must wait, got %v", err)
	}
	unlock()
	u2, err := lockState(dir)
	if err != nil {
		t.Fatalf("after unlock the lock must be free: %v", err)
	}
	u2()
	// A lock left by a crash long ago does not block forever.
	os.WriteFile(filepath.Join(dir, "run.lock"), []byte("1"), 0o600)
	old := time.Now().Add(-2 * staleLock)
	os.Chtimes(filepath.Join(dir, "run.lock"), old, old)
	u3, err := lockState(dir)
	if err != nil {
		t.Fatalf("a stale lock must be taken over: %v", err)
	}
	u3()
}

// A kind switched off on one computer: its Helium file stays exactly as it
// is, its local changes stay local, and nothing is lost when it is switched
// on again.
func TestKindSwitchedOffKeepsChangesForLater(t *testing.T) {
	ctx := context.Background()
	server := NewMemory()
	macUD, winUD := fakeUserData(t, ver), fakeUserData(t, ver)
	mac, win := cfgFor(t, "mac", macUD, server), cfgFor(t, "win", winUD, server)
	LinkProfile(mac.StateDir, mac.DeviceID, "Default")
	if _, err := Run(ctx, mac); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, win); err != nil { // adds the Mac's profile as "Profile 1"
		t.Fatal(err)
	}
	winBM := filepath.Join(winUD, "Profile 1", "Bookmarks")

	win.Off = map[string]bool{KindBookmarks: true}
	addBookmark(t, macUD, "frommac", "From the Mac")
	if _, err := Run(ctx, mac); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(winBM)
	if _, err := Run(ctx, win); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(winBM); !bytes.Equal(before, after) {
		t.Fatal("bookmarks are off on Windows, but its Bookmarks file changed")
	}
	// A change made on Windows while off stays on Windows.
	f, _ := bookmarks.Load(winBM)
	bar := f.Root("bookmark_bar")
	bar["children"] = append(bar["children"].([]any), map[string]any{"id": "200", "guid": "fromwin", "name": "From Windows",
		"type": "url", "url": "https://fromwin.example/", "date_added": "1"})
	f.Save(winBM)
	if _, err := Run(ctx, win); err != nil {
		t.Fatal(err)
	}
	Run(ctx, mac)
	if got := titles(t, macUD, "Default"); strings.Contains(got, "From Windows") {
		t.Fatalf("a bookmark made while bookmarks were off was sent: %s", got)
	}

	// Switched on again: both changes arrive, nothing is lost.
	win.Off = nil
	if _, err := Run(ctx, win); err != nil {
		t.Fatal(err)
	}
	if got := titles(t, winUD, "Profile 1"); !strings.Contains(got, "From the Mac") || !strings.Contains(got, "From Windows") {
		t.Fatalf("after switching on, Windows has: %s", got)
	}
	if _, err := Run(ctx, mac); err != nil {
		t.Fatal(err)
	}
	if got := titles(t, macUD, "Default"); !strings.Contains(got, "From Windows") || !strings.Contains(got, "From the Mac") {
		t.Fatalf("after switching on, the Mac has: %s", got)
	}
}

// A person opens Helium in the middle of a sync, at each possible moment:
// the sync stops before the next write and puts back what it wrote.
func TestHeliumOpenedDuringSyncChangesNothing(t *testing.T) {
	ctx := context.Background()
	snapshot := func(ud string) map[string]string {
		out := map[string]string{}
		filepath.Walk(ud, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				b, _ := os.ReadFile(p)
				rel, _ := filepath.Rel(ud, p)
				out[rel] = string(b)
			}
			return nil
		})
		return out
	}
	covered := 0
	for openAt := 2; openAt <= 12; openAt++ {
		server := NewMemory()
		macUD, winUD := fakeUserData(t, ver), fakeUserData(t, ver)
		mac, win := cfgFor(t, "mac", macUD, server), cfgFor(t, "win", winUD, server)
		LinkProfile(mac.StateDir, mac.DeviceID, "Default")
		addBookmark(t, macUD, "frommac", "From the Mac")
		if _, err := Run(ctx, mac); err != nil {
			t.Fatal(err)
		}
		before := snapshot(winUD)
		calls := 0
		win.Running = func(string) (bool, error) { calls++; return calls >= openAt, nil }
		_, err := Run(ctx, win)
		if calls < openAt {
			break // every write point is covered
		}
		covered++
		if !errors.Is(err, ErrHeliumRunning) {
			t.Fatalf("Helium opened at check %d: got %v, want ErrHeliumRunning", openAt, err)
		}
		after := snapshot(winUD)
		for f, b := range before {
			if after[f] != b {
				t.Fatalf("Helium opened at check %d: %s changed", openAt, f)
			}
		}
		for f := range after {
			if _, ok := before[f]; !ok {
				t.Fatalf("Helium opened at check %d: %s was left behind", openAt, f)
			}
		}
		// Helium closes again: the next sync completes normally.
		win.Running = nil
		if _, err := Run(ctx, win); err != nil {
			t.Fatalf("after Helium closed again (check %d): %v", openAt, err)
		}
		if got := titles(t, winUD, "Profile 1"); !strings.Contains(got, "From the Mac") {
			t.Fatalf("after Helium closed again (check %d): %s", openAt, got)
		}
	}
	t.Logf("Helium opened at %d different moments", covered)
	if covered < 3 { // profiles, settings and bookmarks are written in this test
		t.Fatalf("only %d moments tested", covered)
	}
}
