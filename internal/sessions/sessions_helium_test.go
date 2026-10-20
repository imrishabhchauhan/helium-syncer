//go:build helium

// End-to-end test against a real Helium install, on a throwaway profile.
// Run: go test -tags helium ./internal/sessions/
package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/imrishabhchauhan/helium-syncer/internal/chromejson"
	"github.com/imrishabhchauhan/helium-syncer/internal/deviceid"
	"github.com/imrishabhchauhan/helium-syncer/internal/prefhash"
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

type snap struct {
	Windows []struct {
		Tabs []struct {
			URL     string `json:"url"`
			Title   string `json:"title"`
			Pinned  bool   `json:"pinned"`
			GroupID int    `json:"groupId"`
		} `json:"tabs"`
	} `json:"windows"`
	Groups []struct {
		ID    int    `json:"id"`
		Title string `json:"title"`
		Color string `json:"color"`
	} `json:"groups"`
}

// tabsOf flattens a snapshot into "url|pinned|group title|color" lines.
func tabsOf(s snap, srv string) []string {
	groups := map[int]string{}
	for _, g := range s.Groups {
		groups[g.ID] = g.Title + "|" + g.Color
	}
	var out []string
	for _, w := range s.Windows {
		for _, t := range w.Tabs {
			if !strings.HasPrefix(t.URL, srv) {
				continue
			}
			out = append(out, fmt.Sprintf("%s|%v|%s", strings.TrimPrefix(t.URL, srv), t.Pinned, groups[t.GroupID]))
		}
	}
	return out
}

func waitTitles(ctx context.Context, t *testing.T, in *testkit.Instance, srv string, want int) snap {
	var s snap
	for i := 0; i < 60; i++ {
		raw := json.RawMessage{}
		if err := in.Eval(ctx, "probe.snapshot()", &raw); err != nil {
			t.Fatal(err)
		}
		json.Unmarshal(raw, &s)
		n := 0
		for _, w := range s.Windows {
			for _, tb := range w.Tabs {
				if strings.HasPrefix(tb.URL, srv) && !strings.HasPrefix(tb.Title, "127.0.0.1") && tb.Title != "" {
					n++
				}
			}
		}
		if n >= want {
			return s
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("tabs did not finish loading")
	return s
}

func TestSessionsWithHelium(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	pages := map[string]string{"/a": "Page A", "/b": "Page B", "/pinned": "Pinned page", "/hindi": "हिंदी पेज", "/mac": "From Mac 🍎", "/mac-pin": "Mac pinned"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<!doctype html><title>%s</title><p>%s", pages[r.URL.Path], r.URL.Path)
	}))
	defer srv.Close()

	root, _ := filepath.Abs("../..")
	ud := filepath.Join(root, "test-profiles", fmt.Sprintf("it-sessions-%d", time.Now().Unix()))
	opts := testkit.Options{Exe: heliumExe(t), UserDataDir: ud, Port: 9353, ProbeDir: filepath.Join(root, "testing", "probe-extension")}
	profile := filepath.Join(ud, "Default")

	// 1. Helium opens tabs: a group, a pinned tab, a Hindi page.
	in, err := testkit.Start(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	u := srv.URL
	steps := []string{
		fmt.Sprintf(`probe.openTabs([%q,%q], {group:{title:"Work 🚀", color:"blue"}})`, u+"/a", u+"/b"),
		fmt.Sprintf(`probe.openTabs([%q], {pinned:true})`, u+"/pinned"),
		fmt.Sprintf(`probe.openTabs([%q])`, u+"/hindi"),
	}
	for _, s := range steps {
		if err := in.Eval(ctx, s, nil); err != nil {
			in.Kill()
			t.Fatal(err)
		}
	}
	before := tabsOf(waitTitles(ctx, t, in, u, 4), u)
	if err := in.Close(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("Helium had: %v", before)

	// 2. Our reader must see the same tabs, pins and groups.
	latest, err := LatestFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ReadFile(latest)
	if err != nil {
		t.Fatal(err)
	}
	colors := []string{"grey", "blue", "red", "yellow", "green", "pink", "purple", "cyan", "orange"}
	var ours []string
	var titles []string
	for _, w := range s.Windows {
		for _, tb := range w.Tabs {
			cur := tb.Current()
			if !strings.HasPrefix(cur.URL, u) {
				continue
			}
			g := ""
			if tb.Group != nil {
				if meta := s.Groups[*tb.Group]; meta != nil {
					g = meta.Title + "|" + colors[meta.Color]
				}
			}
			ours = append(ours, fmt.Sprintf("%s|%v|%s", strings.TrimPrefix(cur.URL, u), tb.Pinned, g))
			titles = append(titles, cur.Title)
		}
	}
	if !reflect.DeepEqual(ours, before) {
		t.Fatalf("reader mismatch\n ours:   %v\n helium: %v", ours, before)
	}
	// Helium 0.19.4.1 does not persist page titles in Session_* files (only
	// in Tabs_*); titles for synced tabs come from History instead.
	t.Logf("titles stored in session file: %q", titles)

	// 3. Encode -> parse must give the same state.
	again := Build(mustCommands(t, Encode(s)))
	if !sameTabs(s, again) {
		t.Fatal("Encode/Build round trip changed the session")
	}

	// 4. Add what another device had open: a new group and a pinned tab.
	w := s.Windows[0]
	gkey := [2]uint64{0x1122334455667788, 0x99aabbccddeeff00}
	s.Groups[gkey] = &Group{High: gkey[0], Low: gkey[1], Title: "Mac 🍎", Color: 2}
	now := time.Now().UnixMicro() + 11644473600*1_000_000
	newTab := func(path string, pinned bool, group *[2]uint64) *Tab {
		return &Tab{Pinned: pinned, Group: group, Navigations: []Navigation{{
			URL: u + path, Title: pages[path], Transition: 0x30000000, Timestamp: now, HTTPStatus: 200,
		}}}
	}
	w.Tabs = append([]*Tab{newTab("/mac-pin", true, nil)}, w.Tabs...)
	w.Tabs = append(w.Tabs, newTab("/mac", false, &gkey))
	if _, err := WriteNew(profile, s); err != nil {
		t.Fatal(err)
	}
	setRestoreLastSession(t, profile)

	// 5. Helium restores everything on its next start.
	opts.NoURL = true
	in, err = testkit.Start(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	after := tabsOf(waitTitles(ctx, t, in, u, 0), u)
	in.Close(ctx)
	t.Logf("Helium restored: %v", after)
	for _, want := range append(before, "/mac-pin|true|", "/mac|false|Mac 🍎|red") {
		if !contains(after, want) {
			t.Errorf("after restart Helium is missing tab %q", want)
		}
	}
}

// setRestoreLastSession turns on "Continue where you left off" the way the
// product will: write the protected pref and re-sign Secure Preferences.
func setRestoreLastSession(t *testing.T, profile string) {
	path := filepath.Join(profile, "Secure Preferences")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	v, err := chromejson.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	doc := v.(map[string]any)
	one, _ := chromejson.Decode([]byte("1"))
	chromejson.Set(doc, "session.restore_on_startup", one)
	dev, err := deviceid.Get()
	if err != nil {
		t.Fatal(err)
	}
	prefhash.Resign(doc, prefhash.Calculator{DeviceID: dev}, true, "session.restore_on_startup")
	if err := os.WriteFile(path, []byte(chromejson.Compact(doc)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustCommands(t *testing.T, data []byte) []Command {
	cmds, ok, err := ParseCommands(data)
	if err != nil || !ok {
		t.Fatalf("encoded file invalid: marker=%v err=%v", ok, err)
	}
	return cmds
}

func sameTabs(a, b *Session) bool {
	flat := func(s *Session) []string {
		var out []string
		for _, w := range s.Windows {
			for _, t := range w.Tabs {
				g := ""
				if t.Group != nil {
					g = s.Groups[*t.Group].Title
				}
				out = append(out, fmt.Sprintf("%v|%s|%d|%v", t.Pinned, g, t.SelectedNav, t.Navigations))
			}
		}
		return out
	}
	return reflect.DeepEqual(flat(a), flat(b))
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
