//go:build helium

// The companion extension's tab handling, in a real throwaway Helium. The
// extension is loaded from a copy pointed at a private test port, so it
// never talks to the Helium Syncer app running on this computer.
// Run: go test -tags helium -run TabsInRealHelium -v ./internal/live/
package live

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/imrishabhchauhan/helium-syncer/extension"
	"github.com/imrishabhchauhan/helium-syncer/internal/sessions"
	"github.com/imrishabhchauhan/helium-syncer/internal/testkit"
)

const testPort = 47899

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

// waitTabs polls the server until ok(report) holds.
func waitTabs(t *testing.T, s *Server, profile, what string, ok func(*TabsReport) bool) *TabsReport {
	t.Helper()
	deadline := time.Now().Add(25 * time.Second)
	var last *TabsReport
	for time.Now().Before(deadline) {
		s.mu.Lock()
		if c := s.conns[profile]; c != nil && c.tabs != nil {
			last = c.tabs
		}
		s.mu.Unlock()
		if last != nil && ok(last) {
			return last
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("%s: never happened; last report: %s", what, tabsShape(last))
	return nil
}

func find(r *TabsReport, pred func(LiveTab) bool) (LiveTab, bool) {
	for _, w := range r.Windows {
		for _, t := range w.Tabs {
			if pred(t) {
				return t, true
			}
		}
	}
	return LiveTab{}, false
}

func hasURL(r *TabsReport, sub string) bool {
	_, ok := find(r, func(t LiveTab) bool { return strings.Contains(t.URL, sub) })
	return ok
}

func countTabs(r *TabsReport) int {
	n := 0
	for _, w := range r.Windows {
		n += len(w.Tabs)
	}
	return n
}

func TestTabsInRealHelium(t *testing.T) {
	exe := heliumExe(t)
	ctx := context.Background()
	root, _ := filepath.Abs("../../test-profiles")
	ud := filepath.Join(root, fmt.Sprintf("it-livetabs-%d", time.Now().Unix()))
	profile := filepath.Join(ud, "Default")
	os.MkdirAll(profile, 0o700)
	// Two bookmarks, so the app can tell which profile the browser is.
	os.WriteFile(filepath.Join(profile, "Bookmarks"), []byte(`{"roots":{"bookmark_bar":{"children":[
		{"guid":"aaaaaaaa-0000-4000-8000-000000000001","id":"10","name":"One","type":"url","url":"https://one.example/","date_added":"13300000000000000"},
		{"guid":"aaaaaaaa-0000-4000-8000-000000000002","id":"11","name":"Two","type":"url","url":"https://two.example/","date_added":"13300000000000000"}],
		"date_added":"0","date_modified":"0","guid":"0bc5d13f-2cba-5d74-951f-3f233fe6c908","id":"1","name":"Bookmarks bar","type":"folder"},
		"other":{"children":[],"date_added":"0","date_modified":"0","guid":"82b081ec-3dd3-529c-8475-ab6c344590dd","id":"2","name":"Other bookmarks","type":"folder"},
		"synced":{"children":[],"date_added":"0","date_modified":"0","guid":"4cf2e351-0e85-532b-bb37-df045d8f8d0f","id":"3","name":"Mobile bookmarks","type":"folder"}},"version":1}`), 0o600)

	// The extension, pointed at the test port.
	extDir := t.TempDir()
	for _, name := range extension.Names {
		b, err := extension.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if name == "background.js" {
			b = []byte(strings.Replace(string(b), "ws://127.0.0.1:47811/live", fmt.Sprintf("ws://127.0.0.1:%d/live", testPort), 1))
		}
		os.WriteFile(filepath.Join(extDir, name), b, 0o600)
	}

	// The app's end, plus two small pages: one that moves to the other.
	srv := New(ud)
	srv.AllowOrigin = OriginOf("mhealbgfgbmpmodihicdniadpbkdbkkl")
	mux := http.NewServeMux()
	mux.Handle(Path, srv.Handler())
	mux.HandleFunc("/moves", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/landed", http.StatusFound) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "<title>%s</title>page %s", r.URL.Path, r.URL.Path)
	})
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", testPort))
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(ln, mux)
	defer ln.Close()
	page := func(p string) string { return fmt.Sprintf("http://127.0.0.1:%d%s", testPort, p) }

	probe, _ := filepath.Abs("../../testing/probe-extension")
	in, err := testkit.Start(ctx, testkit.Options{Exe: exe, UserDataDir: ud, Port: 9341, ProbeDir: probe + "," + extDir, NoURL: true})
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close(ctx)

	// 1. The browser reports its tabs once it settled.
	in.Eval(ctx, fmt.Sprintf(`probe.openTabs([%q, %q])`, page("/a"), page("/b")), nil)
	r := waitTabs(t, srv, profile, "report of two opened tabs", func(r *TabsReport) bool { return hasURL(r, "/a") && hasURL(r, "/b") })
	t.Logf("first report: %s", tabsShape(r))

	// 2. The app asks: close /b, open a page that moves, pin /a.
	sess, fresh, ok := srv.ReadTabs(profile)
	if !ok {
		t.Fatal("ReadTabs: not connected")
	}
	if !fresh {
		t.Fatal("the first report after the browser started should be marked fresh")
	}
	w := sess.Windows[0]
	var kept []*sessions.Tab
	for _, tb := range w.Tabs {
		u := tb.Current().URL
		if strings.HasSuffix(u, "/b") {
			continue
		}
		if strings.HasSuffix(u, "/a") {
			tb.Pinned = true
		}
		kept = append(kept, tb)
	}
	kept = append(kept, &sessions.Tab{Navigations: []sessions.Navigation{{URL: page("/moves")}}})
	w.Tabs = kept
	if err := srv.WriteTabs(profile, sess); err != nil {
		t.Fatal(err)
	}
	if _, fresh, _ := srv.ReadTabs(profile); fresh {
		t.Fatal("after a merge used it, the start is dealt with; fresh must clear")
	}
	r = waitTabs(t, srv, profile, "close, open and pin", func(r *TabsReport) bool {
		a, okA := find(r, func(t LiveTab) bool { return strings.HasSuffix(t.URL, "/a") })
		_, okLanded := find(r, func(t LiveTab) bool { return strings.HasSuffix(t.URL, "/landed") && t.Origin == page("/moves") })
		return okA && a.Pinned && !hasURL(r, "/b") && okLanded
	})
	t.Logf("after apply: %s", tabsShape(r))

	// 3. The moved page keeps its origin after it settled...
	time.Sleep(5 * time.Second)
	in.Eval(ctx, `chrome.tabs.query({}).then(() => 1)`, nil) // a tab event wakes the report
	r = waitTabs(t, srv, profile, "origin kept after settling", func(r *TabsReport) bool {
		_, ok := find(r, func(t LiveTab) bool { return strings.HasSuffix(t.URL, "/landed") && t.Origin == page("/moves") })
		return ok
	})
	// ...and loses it when the person goes elsewhere in that tab.
	landed, _ := find(r, func(t LiveTab) bool { return strings.HasSuffix(t.URL, "/landed") })
	in.Eval(ctx, fmt.Sprintf(`chrome.tabs.update(%d, {url: %q}).then(() => 1)`, landed.ID, page("/elsewhere")), nil)
	waitTabs(t, srv, profile, "origin dropped after the person navigated", func(r *TabsReport) bool {
		_, ok := find(r, func(t LiveTab) bool { return strings.HasSuffix(t.URL, "/elsewhere") && t.Origin == "" })
		return ok
	})

	// 4. A new group, and a new window.
	sess, _, _ = srv.ReadTabs(profile)
	tok := [2]uint64{7, 7}
	sess.Groups[tok] = &sessions.Group{High: 7, Low: 7, Title: "Work", Color: 1}
	for _, tb := range sess.Windows[0].Tabs {
		if strings.HasSuffix(tb.Current().URL, "/elsewhere") {
			tb.Group = &tok
		}
	}
	sess.Windows = append(sess.Windows, &sessions.Window{Tabs: []*sessions.Tab{
		{Navigations: []sessions.Navigation{{URL: page("/w2-one")}}},
		{Navigations: []sessions.Navigation{{URL: page("/w2-two")}}},
	}})
	if err := srv.WriteTabs(profile, sess); err != nil {
		t.Fatal(err)
	}
	r = waitTabs(t, srv, profile, "group and new window", func(r *TabsReport) bool {
		g, ok := find(r, func(t LiveTab) bool { return strings.HasSuffix(t.URL, "/elsewhere") })
		named := false
		for _, gr := range r.Groups {
			named = named || (gr.ID == g.GroupID && gr.Title == "Work" && gr.Color == "blue")
		}
		return ok && named && len(r.Windows) == 2 && hasURL(r, "/w2-one") && hasURL(r, "/w2-two")
	})
	t.Logf("group and window: %s", tabsShape(r))

	// 5. Nothing changed: the same session again sends nothing.
	sess, _, _ = srv.ReadTabs(profile)
	if a, changed := planTabs(r, sess, func() string { srv.mu.Lock(); defer srv.mu.Unlock(); return srv.conns[profile].nonce }()); changed {
		t.Fatalf("the browser's own tabs read back as a change: %+v", a)
	}

	// 6. Every tab of every window closes on the other computer: one window
	// stays open with a new tab page, and Helium keeps running.
	sess, _, _ = srv.ReadTabs(profile)
	for _, w := range sess.Windows {
		w.Tabs = nil
	}
	if err := srv.WriteTabs(profile, sess); err != nil {
		t.Fatal(err)
	}
	r = waitTabs(t, srv, profile, "last window kept", func(r *TabsReport) bool {
		return len(r.Windows) == 1 && countTabs(r) == 1 && hasURL(r, "newtab")
	})
	t.Logf("after closing everything: %s", tabsShape(r))
}
