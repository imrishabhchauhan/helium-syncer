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
	macBase, out, _, err := SyncProfiles(mac, macLinks, map[string]json.RawMessage{}, macState, crdt.NewClock("mac", nil), nil, avatar)
	if err != nil {
		t.Fatal(err)
	}
	s.log = append(s.log, out...)
	winBase, _, plan, err := SyncProfiles(win, winLinks, map[string]json.RawMessage{}, winState, crdt.NewClock("win", nil), s.log, avatar)
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
	_, out, _, _ = SyncProfiles(mac, macLinks, macBase, macState, macClock, nil, avatar)
	s.log = append(s.log, out...)
	winBase, _, plan, _ = SyncProfiles(win, winLinks, winBase, winState, crdt.NewClock("win", nil), out, avatar)
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
	_, out, _, _ = SyncProfiles(mac, macLinks, macBase, macState, macClock, nil, avatar)
	_, _, plan, _ = SyncProfiles(win, winLinks, winBase, winState, crdt.NewClock("win", nil), out, avatar)
	if len(names(t, win)) != 3 {
		t.Fatalf("a profile deleted elsewhere must not be deleted here: %v", names(t, win))
	}
	if len(plan.Unlinked) != 1 {
		t.Fatalf("the profile should be unlinked, plan %+v", plan)
	}
}
