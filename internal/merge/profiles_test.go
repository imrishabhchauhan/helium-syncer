package merge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/imrishabhchauhan/helium-syncer/internal/crdt"
	"github.com/imrishabhchauhan/helium-syncer/internal/profiles"
)

func userDataWith(t *testing.T, names map[string]string) string {
	ud := t.TempDir()
	cache := map[string]any{}
	for dir, name := range names {
		cache[dir] = map[string]any{"name": name}
		os.MkdirAll(filepath.Join(ud, dir), 0o700)
		os.WriteFile(filepath.Join(ud, dir, "Preferences"), []byte(`{"profile":{"name":"`+name+`"}}`), 0o600)
	}
	ls, _ := json.Marshal(map[string]any{"profile": map[string]any{"info_cache": cache}})
	os.WriteFile(filepath.Join(ud, "Local State"), ls, 0o600)
	return ud
}

func names(t *testing.T, ud string) map[string]string {
	list, err := profiles.List(ud)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, p := range list {
		out[p.Dir] = p.Name
	}
	return out
}

func TestProfilesFirstJoinAddsNeverMerges(t *testing.T) {
	s := &server{}
	mac := userDataWith(t, map[string]string{"Default": "Personal", "Profile 1": "Work"})
	win := userDataWith(t, map[string]string{"Default": "Your Helium"})
	macLinks := ProfileLinks{}
	macLinks.LinkNew("Default")
	macLinks.LinkNew("Profile 1")
	winLinks := ProfileLinks{} // the user chose not to sync "Your Helium"
	avatar := func(int) int { return 26 }
	macState, winState := crdt.NewMap(), crdt.NewMap()
	macBase, out, _, err := SyncProfiles(mac, macLinks, nil, map[string]json.RawMessage{}, macState, crdt.NewClock("mac", nil), nil, avatar)
	if err != nil {
		t.Fatal(err)
	}
	s.log = append(s.log, out...)
	winBase, _, plan, err := SyncProfiles(win, winLinks, nil, map[string]json.RawMessage{}, winState, crdt.NewClock("win", nil), s.log, avatar)
	if err != nil {
		t.Fatal(err)
	}
	got := names(t, win)
	if got["Default"] != "Your Helium" || len(got) != 3 || len(plan.Added) != 2 {
		t.Fatalf("Windows should keep Your Helium and add Personal + Work, got %v (plan %+v)", got, plan)
	}

	// Rename on the Mac follows to Windows.
	if err := profiles.Rename(mac, "Profile 1", "Work 💼"); err != nil {
		t.Fatal(err)
	}
	macClock := crdt.NewClock("mac", nil)
	_, out, _, _ = SyncProfiles(mac, macLinks, nil, macBase, macState, macClock, nil, avatar)
	s.log = append(s.log, out...)
	winBase, _, plan, _ = SyncProfiles(win, winLinks, nil, winBase, winState, crdt.NewClock("win", nil), out, avatar)
	found := false
	for _, n := range names(t, win) {
		found = found || n == "Work 💼"
	}
	if !found || len(plan.Renamed) != 1 {
		t.Fatalf("rename did not follow: %v", names(t, win))
	}

	// Mac deletes "Personal": Windows keeps it, just stops syncing it.
	delete(macLinks, "Default")
	ls, _ := json.Marshal(map[string]any{"profile": map[string]any{"info_cache": map[string]any{"Profile 1": map[string]any{"name": "Work 💼"}}}})
	os.WriteFile(filepath.Join(mac, "Local State"), ls, 0o600)
	macLinks["Default"] = "gone" // simulate the stale link the engine had
	_, out, _, _ = SyncProfiles(mac, macLinks, nil, macBase, macState, macClock, nil, avatar)
	_, _, plan, _ = SyncProfiles(win, winLinks, nil, winBase, winState, crdt.NewClock("win", nil), out, avatar)
	if len(names(t, win)) != 3 {
		t.Fatalf("a profile deleted elsewhere must not be deleted here: %v", names(t, win))
	}
	if len(plan.Unlinked) != 1 {
		t.Fatalf("the profile should be unlinked, plan %+v", plan)
	}
}

// Same name on both computers: the added profile is "Your Helium (2)" here
// only. The other computer keeps "Your Helium", renames still travel, and
// the ending goes away when the names no longer clash.
func TestSameProfileNameGetsLocalEnding(t *testing.T) {
	mac := userDataWith(t, map[string]string{"Default": "Your Helium"})
	win := userDataWith(t, map[string]string{"Default": "Your Helium"})
	macLinks, winLinks := ProfileLinks{}, ProfileLinks{}
	macLinks.LinkNew("Default")
	winLinks.LinkNew("Default")
	avatar := func(int) int { return 26 }
	macState, winState := crdt.NewMap(), crdt.NewMap()
	macClock, winClock := crdt.NewClock("mac", nil), crdt.NewClock("win", nil)
	macSuffix, winSuffix := map[string]string{}, map[string]string{}
	var log []crdt.Op
	sync := func(ud string, links ProfileLinks, suffix map[string]string, base map[string]json.RawMessage, st *crdt.Map, clock *crdt.Clock, seen *int) map[string]json.RawMessage {
		t.Helper()
		nb, out, _, err := SyncProfiles(ud, links, suffix, base, st, clock, log[*seen:], avatar)
		if err != nil {
			t.Fatal(err)
		}
		*seen = len(log)
		log = append(log, out...)
		*seen = len(log)
		return nb
	}
	macSeen, winSeen := 0, 0
	macBase := sync(mac, macLinks, macSuffix, map[string]json.RawMessage{}, macState, macClock, &macSeen)
	winBase := sync(win, winLinks, winSuffix, map[string]json.RawMessage{}, winState, winClock, &winSeen)
	macBase = sync(mac, macLinks, macSuffix, macBase, macState, macClock, &macSeen)
	winBase = sync(win, winLinks, winSuffix, winBase, winState, winClock, &winSeen)
	macBase = sync(mac, macLinks, macSuffix, macBase, macState, macClock, &macSeen)

	w, m := names(t, win), names(t, mac)
	if w["Default"] != "Your Helium" || w["Profile 1"] != "Your Helium (2)" {
		t.Fatalf("Windows: %v", w)
	}
	if m["Default"] != "Your Helium" || m["Profile 1"] != "Your Helium (2)" {
		t.Fatalf("Mac: %v", m)
	}
	// The ending never travels: every synced name is plain "Your Helium".
	for _, e := range winState.Entries {
		var p ProfileItem
		json.Unmarshal(e.Value, &p)
		if p.Name != "Your Helium" {
			t.Fatalf("a local ending was synced: %q", p.Name)
		}
	}
	// The Mac renames its own profile: Windows gets the new name, no ending.
	if err := profiles.Rename(mac, "Default", "Personal"); err != nil {
		t.Fatal(err)
	}
	macBase = sync(mac, macLinks, macSuffix, macBase, macState, macClock, &macSeen)
	sync(win, winLinks, winSuffix, winBase, winState, winClock, &winSeen)
	if w := names(t, win); w["Profile 1"] != "Personal" || w["Default"] != "Your Helium" {
		t.Fatalf("after the rename, Windows: %v", w)
	}
	if len(winSuffix) != 0 {
		t.Fatalf("ending kept after the clash went away: %v", winSuffix)
	}
	_ = macBase
}
