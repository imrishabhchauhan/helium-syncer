package engine

import (
	"context"
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imrishabhchauhan/helium-syncer/internal/bookmarks"
	"github.com/imrishabhchauhan/helium-syncer/internal/profiles"
)

// snapshot fingerprints every file under dir.
func snapshot(t *testing.T, dir string) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			out[p] = sha256.Sum256(b)
		}
		return nil
	})
	return out
}

func sameFiles(t *testing.T, what string, before, after map[string][32]byte) {
	t.Helper()
	for p, h := range after {
		if b, ok := before[p]; !ok {
			t.Errorf("%s: created %s", what, p)
		} else if b != h {
			t.Errorf("%s: changed %s", what, p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			t.Errorf("%s: removed %s", what, p)
		}
	}
}

// browserLive stands in for the companion extension: the browser holds the
// tree in memory, and an applied tree goes to the browser, not the file.
type browserLive struct {
	dir     string
	tree    *bookmarks.File
	applied *bookmarks.File
}

func (b *browserLive) ReadBookmarks(dir string) (*bookmarks.File, bool) {
	if dir != b.dir {
		return nil, false
	}
	return b.tree, true
}

func (b *browserLive) RestoreKeys(string, *bookmarks.File) int { return 0 }

func (b *browserLive) WriteBookmarks(dir string, f *bookmarks.File) error {
	b.applied, b.tree = f, f
	return nil
}

func fileTitles(f *bookmarks.File) string {
	var out []string
	f.Walk(func(n map[string]any, d int) {
		if d > 0 {
			out = append(out, n["name"].(string))
		}
	})
	return strings.Join(out, ",")
}

// While Helium is open, a sync must not change a single file in its folder:
// Helium would overwrite the change and the next sync would read that as the
// person undoing it. Bookmarks reach a connected browser through Live; the
// rest, and a profile whose browser is not connected, waits for the quit.
func TestWhileOpenChangesNoHeliumFile(t *testing.T) {
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

	addBookmark(t, macUD, "frommac", "From the Mac")
	if err := profiles.Rename(macUD, "Default", "Renamed on the Mac"); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, mac); err != nil {
		t.Fatal(err)
	}

	open := win
	open.WhileOpen = true
	open.Running = func(string) (bool, error) { return true, nil }

	// 1. No browser connected: nothing changes, and nothing is lost.
	before := snapshot(t, winUD)
	if _, err := Run(ctx, open); err != nil {
		t.Fatalf("sync while open: %v", err)
	}
	sameFiles(t, "no browser connected", before, snapshot(t, winUD))

	// 2. The browser is connected: the bookmark goes to it, files stay.
	tree, err := bookmarks.Load(filepath.Join(winUD, "Profile 1", "Bookmarks"))
	if err != nil {
		t.Fatal(err)
	}
	b := &browserLive{dir: filepath.Join(winUD, "Profile 1"), tree: tree}
	open.Live = b
	if _, err := Run(ctx, open); err != nil {
		t.Fatalf("live sync while open: %v", err)
	}
	sameFiles(t, "browser connected", before, snapshot(t, winUD))
	if b.applied == nil || !strings.Contains(fileTitles(b.applied), "From the Mac") {
		t.Fatalf("the Mac's bookmark did not reach the open browser")
	}

	// 3. Helium quits, writing the tree it holds (as Helium does), and the
	// next sync applies what was kept.
	if err := b.tree.Save(filepath.Join(winUD, "Profile 1", "Bookmarks")); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, win); err != nil {
		t.Fatal(err)
	}
	if got := titles(t, winUD, "Profile 1"); !strings.Contains(got, "From the Mac") {
		t.Errorf("after quit, Windows bookmarks: %s", got)
	}
	ls, _ := os.ReadFile(filepath.Join(winUD, "Local State"))
	if !strings.Contains(string(ls), "Renamed on the Mac") {
		t.Errorf("after quit, the profile rename kept while open did not arrive: %s", ls)
	}
}
