//go:build helium

// End-to-end sync between two throwaway Helium user data folders on this
// machine ("mac" and "win"), through the in-memory server.
// Run: go test -tags helium ./internal/engine/
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/imrishabhchauhan/helium-syncer/internal/cdp"
	"github.com/imrishabhchauhan/helium-syncer/internal/deviceid"
	"github.com/imrishabhchauhan/helium-syncer/internal/profiles"
	"github.com/imrishabhchauhan/helium-syncer/internal/testkit"
)

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

type snapshot struct {
	Windows []struct {
		Tabs []struct {
			URL     string `json:"url"`
			Pinned  bool   `json:"pinned"`
			GroupID int    `json:"groupId"`
		} `json:"tabs"`
	} `json:"windows"`
	Groups []struct {
		ID    int    `json:"id"`
		Title string `json:"title"`
		Color string `json:"color"`
	} `json:"groups"`
	Bookmarks []map[string]any `json:"bookmarks"`
}

func (s snapshot) tabs(prefix string) []string {
	g := map[int]string{}
	for _, gr := range s.Groups {
		g[gr.ID] = gr.Title + "/" + gr.Color
	}
	var out []string
	for _, w := range s.Windows {
		for _, t := range w.Tabs {
			if strings.HasPrefix(t.URL, prefix) {
				out = append(out, fmt.Sprintf("%s|pinned=%v|%s", strings.TrimPrefix(t.URL, prefix), t.Pinned, g[t.GroupID]))
			}
		}
	}
	sort.Strings(out)
	return out
}

func (s snapshot) bookmarkTitles() []string {
	var out []string
	var walk func(n map[string]any)
	walk = func(n map[string]any) {
		if u, _ := n["url"].(string); u != "" {
			out = append(out, n["title"].(string))
		}
		kids, _ := n["children"].([]any)
		for _, k := range kids {
			walk(k.(map[string]any))
		}
	}
	for _, n := range s.Bookmarks {
		walk(n)
	}
	sort.Strings(out)
	return out
}

type side struct {
	name string
	ud   string
	cfg  Config
	opts testkit.Options
}

func (sd *side) helium(t *testing.T, ctx context.Context, profileDir string, noURL bool, do func(in *testkit.Instance)) {
	t.Helper()
	o := sd.opts
	o.ProfileDir, o.NoURL = profileDir, noURL
	in, err := testkit.Start(ctx, o)
	if err != nil {
		t.Fatalf("%s: start Helium: %v", sd.name, err)
	}
	if do != nil {
		do(in)
	}
	if err := in.Close(ctx); err != nil {
		t.Fatalf("%s: quit Helium: %v", sd.name, err)
	}
}

func (sd *side) sync(t *testing.T, ctx context.Context) Report {
	t.Helper()
	mem := sd.cfg.Transport.(*Memory)
	before := mem.Ops()
	defer func() {
		mem.mu.Lock()
		var kinds []string
		for _, op := range mem.log[before:] {
			_, typ := splitScope(op.Type)
			k := op.Key
			if len(k) > 40 {
				k = k[:40]
			}
			kinds = append(kinds, fmt.Sprintf("%s:%s del=%v", typ, k, op.Deleted))
		}
		mem.mu.Unlock()
		if len(kinds) > 0 && len(kinds) < 8 {
			t.Logf("   %s sent: %v", sd.name, kinds)
		}
	}()
	rep, err := Run(ctx, sd.cfg)
	if err != nil {
		t.Fatalf("%s: sync: %v", sd.name, err)
	}
	if rep.Paused != "" {
		t.Fatalf("%s: sync paused: %s", sd.name, rep.Paused)
	}
	t.Logf("%s synced: sent %d, received %d, profiles added %v", sd.name, rep.Sent, rep.Received, rep.ProfilesAdded)
	return rep
}

func eval(t *testing.T, ctx context.Context, in *testkit.Instance, expr string, out any) {
	t.Helper()
	if err := in.Eval(ctx, expr, out); err != nil {
		in.Kill()
		t.Fatal(err)
	}
}

func TestEndToEndTwoDevices(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	pages := map[string]string{"/gmail": "Inbox", "/docs": "Docs", "/pinned": "Pinned", "/wiki": "विकी", "/news": "News", "/yt": "YouTube"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<!doctype html><title>%s</title>%s", pages[r.URL.Path], r.URL.Path)
	}))
	defer srv.Close()
	u := srv.URL

	root, _ := filepath.Abs("../..")
	run := time.Now().Unix()
	machine, err := deviceid.Get()
	if err != nil {
		t.Fatal(err)
	}
	server := NewMemory()
	mk := func(name string, port int) *side {
		ud := filepath.Join(root, "test-profiles", fmt.Sprintf("e2e-%s-%d", name, run))
		return &side{name: name, ud: ud,
			cfg: Config{UserData: ud, StateDir: filepath.Join(root, "test-profiles", fmt.Sprintf("e2e-%s-%d-syncer", name, run)),
				DeviceID: name + "-device", DeviceName: name, MachineID: machine, Transport: server, RestoreTabs: true},
			opts: testkit.Options{Exe: heliumExe(t), UserDataDir: ud, Port: port, ProbeDir: filepath.Join(root, "testing", "probe-extension")}}
	}
	mac, win := mk("mac", 9361), mk("win", 9362)

	// --- The "Mac": profile "Personal" with bookmarks, a group, a pin, history.
	mac.helium(t, ctx, "", false, func(in *testkit.Instance) {
		eval(t, ctx, in, `probe.addBookmark("Work", "Company wiki", "https://wiki.example/")`, nil)
		eval(t, ctx, in, `probe.addBookmark("", "हिंदी समाचार", "https://news.example/hi")`, nil)
		eval(t, ctx, in, fmt.Sprintf(`probe.openTabs([%q,%q], {group:{title:"Work 🚀", color:"blue"}})`, u+"/gmail", u+"/docs"), nil)
		eval(t, ctx, in, fmt.Sprintf(`probe.openTabs([%q], {pinned:true})`, u+"/pinned"), nil)
		eval(t, ctx, in, fmt.Sprintf(`probe.openTabs([%q])`, u+"/wiki"), nil)
		eval(t, ctx, in, `chrome.history.addUrl({url:"https://visited.example/on-mac"})`, nil)
		time.Sleep(3 * time.Second) // let tabs load so Helium records them
	})
	if err := profiles.Rename(mac.ud, "Default", "Personal"); err != nil {
		t.Fatal(err)
	}
	if _, err := LinkProfile(mac.cfg.StateDir, mac.cfg.DeviceID, "Default"); err != nil {
		t.Fatal(err)
	}
	mac.sync(t, ctx)

	// --- "Windows": its own profile with its own bookmark, not linked.
	win.helium(t, ctx, "", false, func(in *testkit.Instance) {
		eval(t, ctx, in, `probe.addBookmark("", "Windows only", "https://win.example/")`, nil)
	})
	winDefaultBefore, _ := os.ReadFile(filepath.Join(win.ud, "Default", "Bookmarks"))
	rep := win.sync(t, ctx)
	if len(rep.ProfilesAdded) != 1 {
		t.Fatalf("Windows should get exactly one new profile, got %v", rep.ProfilesAdded)
	}
	newDir := rep.ProfilesAdded[0]
	list, _ := profiles.List(win.ud)
	t.Logf("Windows profiles now: %v", list)
	if after, _ := os.ReadFile(filepath.Join(win.ud, "Default", "Bookmarks")); !bytes.Equal(after, winDefaultBefore) {
		t.Fatal("Windows' own profile was changed")
	}

	// --- Open Helium on Windows in the new profile: bookmarks, tabs, group, pin.
	var got snapshot
	win.helium(t, ctx, newDir, true, func(in *testkit.Instance) {
		time.Sleep(2 * time.Second)
		raw := json.RawMessage{}
		eval(t, ctx, in, "probe.snapshot()", &raw)
		json.Unmarshal(raw, &got)
	})
	t.Logf("Windows sees tabs %v, bookmarks %v", got.tabs(u), got.bookmarkTitles())
	for _, want := range []string{"/docs|pinned=false|Work 🚀/blue", "/gmail|pinned=false|Work 🚀/blue", "/pinned|pinned=true|", "/wiki|pinned=false|"} {
		if !contains(got.tabs(u), want) {
			t.Errorf("Windows is missing tab %q", want)
		}
	}
	for _, want := range []string{"Company wiki", "हिंदी समाचार"} {
		if !contains(got.bookmarkTitles(), want) {
			t.Errorf("Windows is missing bookmark %q", want)
		}
	}
	if contains(got.bookmarkTitles(), "Windows only") {
		t.Error("the Windows-only bookmark leaked into the synced profile")
	}

	// --- History arrives once Helium has created the profile's database.
	win.sync(t, ctx)
	var hist []struct {
		URL string `json:"url"`
	}
	win.helium(t, ctx, newDir, true, func(in *testkit.Instance) {
		eval(t, ctx, in, "probe.history(1000)", &hist)
	})
	foundVisit := false
	for _, h := range hist {
		foundVisit = foundVisit || h.URL == "https://visited.example/on-mac"
	}
	if !foundVisit {
		t.Errorf("Windows history is missing the Mac's visit (%d entries)", len(hist))
	}

	// --- On Windows: close the Wikipedia tab, add a bookmark. The Mac gets both.
	win.sync(t, ctx) // settle after the history run
	win.helium(t, ctx, newDir, true, func(in *testkit.Instance) {
		eval(t, ctx, in, `chrome.tabs.query({}).then(ts => chrome.tabs.remove(ts.filter(t => (t.url||t.pendingUrl||"").endsWith("/wiki")).map(t => t.id)))`, nil)
		eval(t, ctx, in, `probe.addBookmark("", "Added on Windows", "https://added.example/")`, nil)
	})
	win.sync(t, ctx)
	mac.sync(t, ctx)
	var macNow snapshot
	mac.helium(t, ctx, "", true, func(in *testkit.Instance) {
		time.Sleep(2 * time.Second)
		raw := json.RawMessage{}
		eval(t, ctx, in, "probe.snapshot()", &raw)
		json.Unmarshal(raw, &macNow)
	})
	t.Logf("Mac sees tabs %v, bookmarks %v", macNow.tabs(u), macNow.bookmarkTitles())
	if contains(macNow.tabs(u), "/wiki|pinned=false|") {
		t.Error("the tab closed on Windows is still open on the Mac")
	}
	if !contains(macNow.bookmarkTitles(), "Added on Windows") {
		t.Error("the bookmark added on Windows did not reach the Mac")
	}
	if len(macNow.tabs(u)) != 3 {
		t.Errorf("Mac should keep Gmail, Docs and the pinned tab, has %v", macNow.tabs(u))
	}
}

// Dark Reader installed from the Web Store on the "Mac" (fixture
// test-profiles/ext-src) arrives on "Windows" through a full sync, enabled
// and with its data, and keeps working after Helium starts.
func TestEndToEndExtension(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	root, _ := filepath.Abs("../..")
	fixture := filepath.Join(root, "test-profiles", "ext-src")
	if _, err := os.Stat(filepath.Join(fixture, "Default", "Extensions", "eimadpbcbfnmbkopoojfekhnkhdbieeh")); err != nil {
		t.Skip("fixture test-profiles/ext-src with Dark Reader not present")
	}
	run := time.Now().Unix()
	macUD := filepath.Join(root, "test-profiles", fmt.Sprintf("e2e-ext-mac-%d", run))
	if err := copyDir(fixture, macUD); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(macUD, "lockfile"))
	machine, _ := deviceid.Get()
	server := NewMemory()
	mk := func(name, ud string, port int) *side {
		return &side{name: name, ud: ud,
			cfg:  Config{UserData: ud, StateDir: ud + "-syncer", DeviceID: name + "-device", DeviceName: name, MachineID: machine, Transport: server},
			opts: testkit.Options{Exe: heliumExe(t), UserDataDir: ud, Port: port, ProbeDir: filepath.Join(root, "testing", "probe-extension")}}
	}
	mac := mk("mac", macUD, 9363)
	win := mk("win", filepath.Join(root, "test-profiles", fmt.Sprintf("e2e-ext-win-%d", run)), 9364)

	if _, err := LinkProfile(mac.cfg.StateDir, mac.cfg.DeviceID, "Default"); err != nil {
		t.Fatal(err)
	}
	mac.sync(t, ctx)
	win.helium(t, ctx, "", false, nil) // fresh Windows Helium
	rep := win.sync(t, ctx)
	if len(rep.ProfilesAdded) != 1 {
		t.Fatalf("want one new profile, got %v", rep.ProfilesAdded)
	}
	newDir := rep.ProfilesAdded[0]

	var enabled bool
	var stored map[string]any
	win.helium(t, ctx, newDir, false, func(in *testkit.Instance) {
		var list []struct {
			ID      string `json:"id"`
			Enabled bool   `json:"enabled"`
		}
		eval(t, ctx, in, "probe.extensions()", &list)
		for _, e := range list {
			if e.ID == "eimadpbcbfnmbkopoojfekhnkhdbieeh" {
				enabled = e.Enabled
			}
		}
		for i := 0; i < 80 && stored == nil; i++ {
			ts, _ := cdp.Targets(ctx, win.opts.Port)
			for _, tg := range ts {
				if tg.Type == "service_worker" && strings.Contains(tg.URL, "eimadpbcbfnmbkopoojfekhnkhdbieeh") {
					c, err := cdp.Dial(ctx, tg.WSURL)
					if err != nil {
						continue
					}
					for k := 0; k < 40 && stored == nil; k++ {
						var v map[string]any
						if c.Eval(ctx, "chrome.storage.local.get(['hsTest','hsNum'])", &v) == nil && v != nil {
							stored = v
						}
						time.Sleep(250 * time.Millisecond)
					}
					c.Close()
				}
			}
			time.Sleep(250 * time.Millisecond)
		}
	})
	if !enabled {
		t.Fatal("Dark Reader is missing or disabled on Windows after sync")
	}
	if stored["hsTest"] != "हिंदी ✓ 🚀" {
		t.Fatalf("Dark Reader's data did not arrive: %v", stored)
	}
	// After Helium ran once, it must still be installed and enabled.
	win.helium(t, ctx, newDir, false, func(in *testkit.Instance) {
		var list []struct {
			ID      string `json:"id"`
			Enabled bool   `json:"enabled"`
		}
		time.Sleep(4 * time.Second)
		eval(t, ctx, in, "probe.extensions()", &list)
		ok := false
		for _, e := range list {
			ok = ok || (e.ID == "eimadpbcbfnmbkopoojfekhnkhdbieeh" && e.Enabled)
		}
		if !ok {
			t.Errorf("Dark Reader disabled or gone after a second start: %+v", list)
		}
	})
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
