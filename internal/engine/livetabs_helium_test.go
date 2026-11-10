//go:build helium

package engine

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/imrishabhchauhan/helium-syncer/extension"
	"github.com/imrishabhchauhan/helium-syncer/internal/deviceid"
	"github.com/imrishabhchauhan/helium-syncer/internal/live"
	"github.com/imrishabhchauhan/helium-syncer/internal/sessions"
	"github.com/imrishabhchauhan/helium-syncer/internal/testkit"
)

// liveSide is one "computer" with Helium open and the companion extension
// connected to its own copy of the app's live end, on its own port.
type liveSide struct {
	*side
	srv     *live.Server
	in      *testkit.Instance
	profile string
	open    Config
}

func companionCopy(t *testing.T, port int) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range extension.Names {
		b, err := extension.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if name == "background.js" {
			b = []byte(strings.Replace(string(b), "ws://127.0.0.1:47811/live", fmt.Sprintf("ws://127.0.0.1:%d/live", port), 1))
		}
		os.WriteFile(filepath.Join(dir, name), b, 0o600)
	}
	return dir
}

func (ls *liveSide) tabs() []string {
	s, _, ok := ls.srv.ReadTabs(ls.profile)
	if !ok {
		return nil
	}
	var out []string
	for _, w := range s.Windows {
		for _, t := range w.Tabs {
			if u := t.Current().URL; strings.HasPrefix(u, "http") {
				out = append(out, u[strings.LastIndex(u, "/"):])
			}
		}
	}
	return out
}

// Two Heliums open at once sync tabs both ways, without one sync undoing
// another or changes going back and forth.
func TestEndToEndLiveTabs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	var flipMu sync.Mutex
	flips := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/moves", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/landed", http.StatusFound) })
	// /flip moves to /flipped from its second visit on: the first computer
	// stays on /flip, the second one lands on /flipped.
	mux.HandleFunc("/flip", func(w http.ResponseWriter, r *http.Request) {
		flipMu.Lock()
		flips++
		n := flips
		flipMu.Unlock()
		if n > 1 {
			http.Redirect(w, r, "/flipped", http.StatusFound)
			return
		}
		fmt.Fprint(w, "<title>flip</title>flip")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "<title>%s</title>%s", r.URL.Path, r.URL.Path)
	})
	pages, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(pages, mux)
	defer pages.Close()
	u := "http://" + pages.Addr().String()

	root, _ := filepath.Abs("../..")
	run := time.Now().Unix()
	machine, err := deviceid.Get()
	if err != nil {
		t.Fatal(err)
	}
	server := NewMemory()
	probe := filepath.Join(root, "testing", "probe-extension")
	mk := func(name string, port int) *side {
		ud := filepath.Join(root, "test-profiles", fmt.Sprintf("e2e-live-%s-%d", name, run))
		return &side{name: name, ud: ud,
			cfg: Config{UserData: ud, StateDir: ud + "-syncer", DeviceID: name + "-device", DeviceName: name,
				MachineID: machine, Transport: server, RestoreTabs: true},
			opts: testkit.Options{Exe: heliumExe(t), UserDataDir: ud, Port: port, ProbeDir: probe}}
	}
	mac, win := mk("mac", 9371), mk("win", 9372)

	// Set up as two computers that synced while Helium was closed.
	mac.helium(t, ctx, "", false, func(in *testkit.Instance) {
		eval(t, ctx, in, `probe.addBookmark("", "One", "https://one.example/")`, nil)
		eval(t, ctx, in, `probe.addBookmark("", "Two", "https://two.example/")`, nil)
		eval(t, ctx, in, fmt.Sprintf(`probe.openTabs([%q,%q,%q])`, u+"/a", u+"/b", u+"/c"), nil)
		time.Sleep(3 * time.Second)
	})
	if _, err := LinkProfile(mac.cfg.StateDir, mac.cfg.DeviceID, "Default"); err != nil {
		t.Fatal(err)
	}
	mac.sync(t, ctx)
	win.helium(t, ctx, "", false, nil)
	rep := win.sync(t, ctx)
	if len(rep.ProfilesAdded) != 1 {
		t.Fatalf("Windows should get one profile, got %v", rep.ProfilesAdded)
	}
	winProfile := rep.ProfilesAdded[0]

	// Open both, each with the companion extension on its own port.
	start := func(sd *side, port int, profileDir string) *liveSide {
		ls := &liveSide{side: sd, srv: live.New(sd.ud)}
		ls.srv.AllowOrigin = live.OriginOf("mhealbgfgbmpmodihicdniadpbkdbkkl")
		m := http.NewServeMux()
		m.Handle(live.Path, ls.srv.Handler())
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Fatal(err)
		}
		go http.Serve(ln, m)
		t.Cleanup(func() { ln.Close() })
		o := sd.opts
		o.ProbeDir = probe + "," + companionCopy(t, port)
		o.ProfileDir, o.NoURL = profileDir, true
		in, err := testkit.Start(ctx, o)
		if err != nil {
			t.Fatalf("%s: start Helium: %v", sd.name, err)
		}
		t.Cleanup(func() { in.Close(context.Background()) })
		ls.in = in
		if profileDir == "" {
			profileDir = "Default"
		}
		ls.profile = filepath.Join(sd.ud, profileDir)
		ls.open = sd.cfg
		ls.open.WhileOpen = true
		ls.open.Running = func(string) (bool, error) { return true, nil }
		ls.open.Live, ls.open.LiveTabs = ls.srv, ls.srv
		deadline := time.Now().Add(40 * time.Second)
		for ls.tabs() == nil && time.Now().Before(deadline) {
			time.Sleep(300 * time.Millisecond)
		}
		if ls.tabs() == nil {
			t.Fatalf("%s: the extension never reported tabs", sd.name)
		}
		return ls
	}
	lm := start(mac, 47911, "")
	lw := start(win, 47912, winProfile)

	// round syncs both until a round sends nothing; it returns the changes
	// sent in all rounds, and fails if they never stop.
	round := func(what string) int {
		t.Helper()
		total := 0
		for i := 0; i < 8; i++ {
			before := server.Ops()
			for _, ls := range []*liveSide{lm, lw} {
				rep, err := Run(ctx, ls.open)
				if err != nil {
					t.Fatalf("%s: %s live sync: %v", what, ls.name, err)
				}
				if rep.Paused != "" || rep.Sent+rep.Received > 0 {
					t.Logf("   %s: %s sent %v received %v paused %q", what, ls.name, rep.SentByType, rep.ReceivedByType, rep.Paused)
				}
			}
			time.Sleep(3500 * time.Millisecond) // the extension applies and reports back
			n := server.Ops() - before
			total += n
			if n == 0 {
				t.Logf("%s: settled after %d round(s), %d change(s); mac %v, win %v", what, i+1, total, lm.tabs(), lw.tabs())
				return total
			}
		}
		t.Fatalf("%s: changes never stopped going back and forth; mac %v, win %v", what, lm.tabs(), lw.tabs())
		return 0
	}
	// seen waits until a side reports what it just did, as the app does: it
	// syncs when the extension reports a change.
	seen := func(ls *liveSide, what string, ok func(string) bool) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if ok(strings.Join(ls.tabs(), " ")) {
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
		t.Fatalf("%s: %s never reported it: %v", what, ls.name, ls.tabs())
	}
	same := func(what string) {
		t.Helper()
		if m, w := strings.Join(lm.tabs(), " "), strings.Join(lw.tabs(), " "); m != w {
			t.Fatalf("%s: tabs differ\nmac: %s\nwin: %s", what, m, w)
		}
	}

	// 1. Both just started: nothing to send, nothing closed.
	if n := round("start"); n != 0 {
		t.Errorf("start: %d change(s) sent by two Heliums that were already in sync", n)
	}
	same("start")

	// 2. Windows opens three tabs, one of which moves when opened on the Mac.
	eval(t, ctx, lw.in, fmt.Sprintf(`probe.openTabs([%q,%q,%q])`, u+"/d", u+"/moves", u+"/flip"), nil)
	seen(lw, "Windows opens tabs", func(v string) bool {
		return strings.Contains(v, "/d") && strings.Contains(v, "/landed") && strings.Contains(v, "/flip")
	})
	round("Windows opens tabs")
	if got := strings.Join(lm.tabs(), " "); !strings.Contains(got, "/d") || !strings.Contains(got, "/landed") || !strings.Contains(got, "/flipped") {
		t.Fatalf("the Mac should show /d, /landed and /flipped: %s", got)
	}
	if got := strings.Join(lw.tabs(), " "); !strings.Contains(got, "/flip") || strings.Contains(got, "/flipped") {
		t.Fatalf("Windows must keep its own /flip tab: %s", got)
	}

	// 3. The Mac closes /b: Windows closes it too.
	eval(t, ctx, lm.in, `chrome.tabs.query({}).then(ts => chrome.tabs.remove(ts.filter(t => (t.url||"").endsWith("/b")).map(t => t.id)))`, nil)
	seen(lm, "Mac closes /b", func(v string) bool { return !strings.Contains(v, "/b") })
	round("Mac closes /b")
	if got := strings.Join(lw.tabs(), " "); strings.Contains(got, "/b") || !strings.Contains(got, "/a") {
		t.Fatalf("Windows should have closed /b only: %s", got)
	}

	// 4. Windows pins /a: the Mac pins it.
	eval(t, ctx, lw.in, `chrome.tabs.query({}).then(ts => Promise.all(ts.filter(t => (t.url||"").endsWith("/a")).map(t => chrome.tabs.update(t.id, {pinned: true}))))`, nil)
	time.Sleep(4 * time.Second) // pinning moves no page; give the report time
	round("Windows pins /a")
	ms, _, _ := lm.srv.ReadTabs(lm.profile)
	pinned := false
	for _, w := range ms.Windows {
		for _, tb := range w.Tabs {
			pinned = pinned || (strings.HasSuffix(tb.Current().URL, "/a") && tb.Pinned)
		}
	}
	if !pinned {
		t.Fatalf("the Mac did not pin /a: %v", lm.tabs())
	}

	// 5. Quit both; the files say the same as the live browsers did, so the
	// syncs after the quit send nothing.
	lm.in.Close(ctx)
	lw.in.Close(ctx)
	// History waits for the quit by design; tabs must not change again.
	for _, sd := range []*side{mac, win} {
		before := server.Ops()
		sd.sync(t, ctx)
		server.mu.Lock()
		tabOps := 0
		for _, op := range server.log[before:] {
			if _, typ := splitScope(op.Type); typ == "tab" || typ == "group" {
				tabOps++
				t.Logf("      %s %s del=%v %s", typ, op.Key, op.Deleted, op.Value)
			}
		}
		server.mu.Unlock()
		if tabOps != 0 {
			t.Errorf("%s: the sync after quitting sent %d tab change(s); the live sync already had them", sd.name, tabOps)
		}
	}
	for _, sd := range []struct {
		s       *side
		profile string
	}{{mac, "Default"}, {win, winProfile}} {
		p, err := sessions.LatestFile(filepath.Join(sd.s.ud, sd.profile))
		if err == nil {
			if s, err := sessions.ReadFile(p); err == nil {
				t.Logf("%s session after quit: %d window(s)", sd.s.name, len(s.Windows))
			}
		}
	}
}
