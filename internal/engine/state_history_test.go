package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imrishabhchauhan/helium-syncer/internal/crdt"
	"github.com/imrishabhchauhan/helium-syncer/internal/sessions"
)

func visit(key string) crdt.Op {
	return crdt.Op{Type: "visit", Key: key, Value: json.RawMessage(`{"tr":1}`), Stamp: crdt.Stamp{Wall: 1, Device: "x"}}
}

// History is saved in its own file, and read back only when asked for.
func TestStateKeepsHistoryApart(t *testing.T) {
	dir := t.TempDir()
	st := newState("me")
	p := st.profile("p1")
	p.Visits.Apply(visit("1|https://a.example/"))
	p.changedHistory()
	p.Tabs.Tabs["https://a.example/#0"] = json.RawMessage(`{"w":0,"o":"m"}`)
	if err := st.Save(dir); err != nil {
		t.Fatal(err)
	}
	main, _ := os.ReadFile(filepath.Join(dir, stateFile))
	if strings.Contains(string(main), "a.example/\"") && strings.Contains(string(main), `"visits"`) {
		t.Fatalf("state.json still holds history: %s", main)
	}
	if _, err := os.Stat(historyFile(dir, "p1")); err != nil {
		t.Fatalf("no history file: %v", err)
	}
	back, err := LoadState(dir, "me")
	if err != nil {
		t.Fatal(err)
	}
	bp := back.Profiles["p1"]
	if bp.histLoaded || bp.Visits != nil {
		t.Fatal("history was read before anything asked for it")
	}
	if err := bp.loadHistory(dir); err != nil {
		t.Fatal(err)
	}
	if _, ok := bp.Visits.Get("1|https://a.example/"); !ok {
		t.Fatal("the visit did not come back from the history file")
	}
	if _, ok := bp.Tabs.Tabs["https://a.example/#0"]; !ok {
		t.Fatal("tabs did not come back from state.json")
	}
	// Saving without a history change leaves the history file alone.
	before, _ := os.Stat(historyFile(dir, "p1"))
	os.WriteFile(historyFile(dir, "p1"), []byte("not json"), 0o600)
	if err := back.Save(dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(historyFile(dir, "p1")); string(b) != "not json" {
		t.Fatalf("an unchanged history was written again (was %d bytes)", before.Size())
	}
}

// A state.json from before the split keeps its history, and the next save
// moves it into the history file.
func TestStateMovesOldHistoryOut(t *testing.T) {
	dir := t.TempDir()
	old := `{"device_id":"me","cursor":"7","links":{"Default":"p1"},"profiles":{"p1":{` +
		`"history_base":{"visits":{"1|https://a.example/":{"tr":1}},"titles":{}},` +
		`"visits":{"e":{"1|https://a.example/":{"v":{"tr":1},"s":{"w":1,"c":0,"d":"x"}}}},` +
		`"titles":{"e":{"https://a.example/":{"v":"A","s":{"w":1,"c":0,"d":"x"}}}},` +
		`"tab_map":{"e":{}}}}}`
	os.WriteFile(filepath.Join(dir, stateFile), []byte(old), 0o600)
	st, err := LoadState(dir, "me")
	if err != nil {
		t.Fatal(err)
	}
	p := st.Profiles["p1"]
	if _, ok := p.Titles.Get("https://a.example/"); !ok || len(p.HistoryBase.Visits) != 1 {
		t.Fatal("old history lost on load")
	}
	if err := st.Save(dir); err != nil {
		t.Fatal(err)
	}
	if main, _ := os.ReadFile(filepath.Join(dir, stateFile)); strings.Contains(string(main), `"visits"`) {
		t.Fatalf("state.json still holds history after the move: %s", main)
	}
	again, err := LoadState(dir, "me")
	if err != nil {
		t.Fatal(err)
	}
	ap := again.Profiles["p1"]
	if err := ap.loadHistory(dir); err != nil {
		t.Fatal(err)
	}
	if _, ok := ap.Titles.Get("https://a.example/"); !ok || len(ap.HistoryBase.Visits) != 1 || again.Cursor != "7" {
		t.Fatal("history or cursor lost after the move")
	}
}

// A sync while Helium is open never reads history: the history file can be
// unreadable and tab sync still works.
func TestWhileOpenNeverReadsHistory(t *testing.T) {
	ctx := context.Background()
	server := NewMemory()
	macUD, winUD := fakeUserData(t, ver), fakeUserData(t, ver)
	mac, win := cfgFor(t, "mac", macUD, server), cfgFor(t, "win", winUD, server)
	LinkProfile(mac.StateDir, mac.DeviceID, "Default")
	sessions.WriteNew(filepath.Join(macUD, "Default"), session("https://claude.ai/a"))
	if _, err := Run(ctx, mac); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, win); err != nil {
		t.Fatal(err)
	}
	st, _ := LoadState(win.StateDir, win.DeviceID)
	for id := range st.Profiles {
		os.WriteFile(historyFile(win.StateDir, id), []byte("broken on purpose"), 0o600)
	}
	open := win
	open.WhileOpen = true
	open.Running = func(string) (bool, error) { return true, nil }
	b := &browserTabs{dir: filepath.Join(winUD, "Profile 1"), sess: session("https://claude.ai/a", "https://vosm.com/")}
	open.LiveTabs = b
	rep, err := Run(ctx, open)
	if err != nil {
		t.Fatalf("a live sync read the history state: %v", err)
	}
	if rep.SentByType["tab"] == 0 {
		t.Fatalf("the new tab was not sent: %+v", rep.SentByType)
	}
	// The check can fail: once another computer's history arrives, the
	// history state is needed, and the broken file shows.
	var pid string
	for id := range st.Profiles {
		pid = id
	}
	op := visit("2|https://b.example/")
	op.Type = scoped(pid, "visit")
	op.Stamp = crdt.Stamp{Wall: 9_999_999_999_999, Device: "mac"}
	server.Push(ctx, []crdt.Op{op})
	if _, err := Run(ctx, open); err == nil || !strings.Contains(err.Error(), "history-") {
		t.Fatalf("history arriving should have needed the broken history file, got %v", err)
	}
}
