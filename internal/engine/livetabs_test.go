package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imrishabhchauhan/helium-syncer/internal/sessions"
)

// browserTabs stands in for the companion extension's tabs: the open
// browser holds them, and a merged session goes to the browser.
type browserTabs struct {
	dir     string
	sess    *sessions.Session
	applied int
}

func (b *browserTabs) ReadTabs(dir string) (*sessions.Session, bool) {
	if dir != b.dir || b.sess == nil {
		return nil, false
	}
	return b.sess, true
}

func (b *browserTabs) WriteTabs(dir string, s *sessions.Session) error {
	b.applied++
	b.sess = s // the extension makes the browser match
	return nil
}

func session(urls ...string) *sessions.Session {
	w := &sessions.Window{ShowState: 1}
	for i, u := range urls {
		w.Tabs = append(w.Tabs, &sessions.Tab{ID: int32(100 + i), Navigations: []sessions.Navigation{{URL: u}}})
	}
	return &sessions.Session{Windows: []*sessions.Window{w}, Groups: map[[2]uint64]*sessions.Group{}}
}

func urlsOf(s *sessions.Session) string {
	var out []string
	for _, w := range s.Windows {
		for _, t := range w.Tabs {
			if c := t.Current(); c != nil {
				out = append(out, c.URL)
			}
		}
	}
	return strings.Join(out, " ")
}

func fileTabs(t *testing.T, profile string) string {
	t.Helper()
	p, err := sessions.LatestFile(profile)
	if err != nil {
		return ""
	}
	s, err := sessions.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return urlsOf(s)
}

// Tabs sync while Helium is open on Windows: what the Mac opened reaches
// the open browser, what Windows closes closes on the Mac, and no file in
// Helium's folder changes. A browser that shows no tab closes nothing.
func TestWhileOpenTabsLive(t *testing.T) {
	ctx := context.Background()
	server := NewMemory()
	macUD, winUD := fakeUserData(t, ver), fakeUserData(t, ver)
	mac, win := cfgFor(t, "mac", macUD, server), cfgFor(t, "win", winUD, server)
	LinkProfile(mac.StateDir, mac.DeviceID, "Default")
	if _, err := sessions.WriteNew(filepath.Join(macUD, "Default"), session("https://claude.ai/a", "https://www.16personalities.com/")); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, mac); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, win); err != nil { // adds the Mac's profile as "Profile 1"
		t.Fatal(err)
	}
	winProfile := filepath.Join(winUD, "Profile 1")
	if got := fileTabs(t, winProfile); !strings.Contains(got, "16personalities") {
		t.Fatalf("setup: Windows should have the Mac's tabs: %q", got)
	}

	// Helium opens on Windows, with the tabs the file had.
	open := win
	open.WhileOpen = true
	open.Running = func(string) (bool, error) { return true, nil }
	b := &browserTabs{dir: winProfile, sess: session("https://claude.ai/a", "https://www.16personalities.com/")}
	open.LiveTabs = b
	before := fileSnapshot(t, winUD)

	// 1. The Mac opens a tab while Windows is open: it reaches the browser.
	sessions.WriteNew(filepath.Join(macUD, "Default"), session("https://claude.ai/a", "https://www.16personalities.com/", "https://vosm.com/"))
	if _, err := Run(ctx, mac); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, open); err != nil {
		t.Fatalf("live tabs sync: %v", err)
	}
	if got := urlsOf(b.sess); !strings.Contains(got, "https://vosm.com/") {
		t.Fatalf("the Mac's new tab did not reach the open browser: %q", got)
	}

	// 2. Windows closes 16personalities in the open browser: the Mac's
	// tabs lose it at its next sync.
	var kept []*sessions.Tab
	for _, tb := range b.sess.Windows[0].Tabs {
		if !strings.Contains(tb.Current().URL, "16personalities") {
			kept = append(kept, tb)
		}
	}
	b.sess.Windows[0].Tabs = kept
	rep, err := Run(ctx, open)
	if err != nil {
		t.Fatal(err)
	}
	if rep.SentByType["tab"] == 0 {
		t.Fatalf("closing a tab in the open browser sent nothing: %+v", rep.SentByType)
	}
	if _, err := Run(ctx, mac); err != nil {
		t.Fatal(err)
	}
	if got := fileTabs(t, filepath.Join(macUD, "Default")); strings.Contains(got, "16personalities") || !strings.Contains(got, "vosm") {
		t.Fatalf("the Mac should have closed 16personalities and kept the rest: %q", got)
	}

	// 3. The browser shows no window: nothing is closed anywhere.
	b.sess = &sessions.Session{Groups: map[[2]uint64]*sessions.Group{}}
	rep, err = Run(ctx, open)
	if err != nil {
		t.Fatal(err)
	}
	if rep.SentByType["tab"] != 0 {
		t.Fatalf("a browser with no window sent %d tab changes", rep.SentByType["tab"])
	}
	if _, err := Run(ctx, mac); err != nil {
		t.Fatal(err)
	}
	if got := fileTabs(t, filepath.Join(macUD, "Default")); !strings.Contains(got, "vosm") || !strings.Contains(got, "claude.ai") {
		t.Fatalf("the Mac lost tabs when Windows had no window: %q", got)
	}

	sameFiles(t, "tabs while open", before, fileSnapshot(t, winUD))
}
