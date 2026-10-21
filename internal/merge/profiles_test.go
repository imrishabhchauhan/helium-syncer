package merge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	// One clock per computer, kept between syncs, as the engine does. A new
	// clock in the same millisecond could give a rename an older stamp.
	macClock, winClock := crdt.NewClock("mac", nil), crdt.NewClock("win", nil)
	macBase, out, _, err := SyncProfiles(mac, macLinks, nil, map[string]json.RawMessage{}, macState, macClock, nil, avatar)
	if err != nil {
		t.Fatal(err)
	}
	s.log = append(s.log, out...)
	winBase, _, plan, err := SyncProfiles(win, winLinks, nil, map[string]json.RawMessage{}, winState, winClock, s.log, avatar)
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
	_, out, _, _ = SyncProfiles(mac, macLinks, nil, macBase, macState, macClock, nil, avatar)
	s.log = append(s.log, out...)
	winBase, _, plan, _ = SyncProfiles(win, winLinks, nil, winBase, winState, winClock, out, avatar)
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
	_, _, plan, _ = SyncProfiles(win, winLinks, nil, winBase, winState, winClock, out, avatar)
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

// The profile picture follows: when the profile is added on the other
// computer, and when it changes later.
func TestProfilePictureFollows(t *testing.T) {
	mac := userDataWith(t, map[string]string{"Default": "Work"})
	win := userDataWith(t, map[string]string{"Default": "Your Helium"})
	if err := profiles.SetAvatar(mac, "Default", 31, false); err != nil {
		t.Fatal(err)
	}
	macLinks, winLinks := ProfileLinks{}, ProfileLinks{}
	macLinks.LinkNew("Default")
	avatar := func(int) int { return 26 }
	macState, winState := crdt.NewMap(), crdt.NewMap()
	macClock, winClock := crdt.NewClock("mac", nil), crdt.NewClock("win", nil)
	macBase, out, _, err := SyncProfiles(mac, macLinks, nil, map[string]json.RawMessage{}, macState, macClock, nil, avatar)
	if err != nil {
		t.Fatal(err)
	}
	winBase, _, plan, err := SyncProfiles(win, winLinks, nil, map[string]json.RawMessage{}, winState, winClock, out, avatar)
	if err != nil || len(plan.Added) != 1 {
		t.Fatalf("add: %v %+v", err, plan)
	}
	added := ""
	for d := range plan.Added {
		added = d
	}
	pic := func(ud, dir string) (int, bool) {
		list, _ := profiles.List(ud)
		for _, p := range list {
			if p.Dir == dir {
				return p.Avatar, p.DefaultAvatar
			}
		}
		return -2, false
	}
	if a, _ := pic(win, added); a != 31 {
		t.Fatalf("added profile has picture %d, want 31 like the Mac", a)
	}
	prefs, _ := os.ReadFile(filepath.Join(win, added, "Preferences"))
	if !strings.Contains(string(prefs), `"avatar_index":31`) {
		t.Fatalf("Preferences of the added profile: %s", prefs)
	}
	// The Mac picks the default picture in the profile colour.
	if err := profiles.SetAvatar(mac, "Default", 26, true); err != nil {
		t.Fatal(err)
	}
	_, out, _, _ = SyncProfiles(mac, macLinks, nil, macBase, macState, macClock, nil, avatar)
	SyncProfiles(win, winLinks, nil, winBase, winState, winClock, out, avatar)
	if a, def := pic(win, added); a != 26 || !def {
		t.Fatalf("after the change: picture %d default %v, want 26 true", a, def)
	}
	// Your Helium, not synced, keeps its own picture.
	if a, _ := pic(win, "Default"); a != -1 {
		t.Fatalf("an unsynced profile was changed: %d", a)
	}
}

// Items from older versions carry no picture: the picture here stays.
func TestOldItemsKeepThePicture(t *testing.T) {
	if !sameAvatar(ProfileItem{Name: "A"}, ProfileItem{Name: "A"}) {
		t.Fatal("an item without a picture must not change the picture here")
	}
	a, b := 30, 31
	if sameAvatar(ProfileItem{Avatar: &a}, ProfileItem{Avatar: &b}) {
		t.Fatal("different pictures must be seen")
	}
}

// The profile colours follow, and go away when the other computer has none.
func TestProfileColorsFollow(t *testing.T) {
	mac := userDataWith(t, map[string]string{"Default": "Work"})
	win := userDataWith(t, map[string]string{"Default": "Your Helium"})
	blue := map[string]int64{"profile_highlight_color": -14802912, "default_avatar_fill_color": -14802912, "default_avatar_stroke_color": -3750202, "profile_color_seed": -4537345}
	if err := profiles.SetAvatar(mac, "Default", 26, false); err != nil {
		t.Fatal(err)
	}
	if err := profiles.SetColors(mac, "Default", blue); err != nil {
		t.Fatal(err)
	}
	macLinks, winLinks := ProfileLinks{}, ProfileLinks{}
	macLinks.LinkNew("Default")
	avatar := func(int) int { return 26 }
	macState, winState := crdt.NewMap(), crdt.NewMap()
	macClock, winClock := crdt.NewClock("mac", nil), crdt.NewClock("win", nil)
	macBase, out, _, err := SyncProfiles(mac, macLinks, nil, map[string]json.RawMessage{}, macState, macClock, nil, avatar)
	if err != nil {
		t.Fatal(err)
	}
	winBase, _, plan, err := SyncProfiles(win, winLinks, nil, map[string]json.RawMessage{}, winState, winClock, out, avatar)
	if err != nil || len(plan.Added) != 1 {
		t.Fatalf("add: %v %+v", err, plan)
	}
	added := ""
	for d := range plan.Added {
		added = d
	}
	colors := func(ud, dir string) map[string]int64 {
		list, _ := profiles.List(ud)
		for _, p := range list {
			if p.Dir == dir {
				return p.Colors
			}
		}
		return nil
	}
	if got := colors(win, added); !reflect.DeepEqual(got, blue) {
		t.Fatalf("added profile colours %v, want %v", got, blue)
	}
	// Existing profile, colours set later on the Mac.
	green := map[string]int64{"profile_highlight_color": -16744448, "profile_color_seed": -16711936}
	profiles.SetColors(mac, "Default", green)
	macBase, out, _, _ = SyncProfiles(mac, macLinks, nil, macBase, macState, macClock, nil, avatar)
	winBase, _, _, _ = SyncProfiles(win, winLinks, nil, winBase, winState, winClock, out, avatar)
	if got := colors(win, added); !reflect.DeepEqual(got, green) {
		t.Fatalf("after the change: %v, want %v", got, green)
	}
	// Only a shade differs (light or dark mode): Windows keeps its own.
	shade := map[string]int64{"profile_highlight_color": -1, "profile_color_seed": -16711936}
	profiles.SetColors(mac, "Default", shade)
	macBase, out, _, _ = SyncProfiles(mac, macLinks, nil, macBase, macState, macClock, nil, avatar)
	winBase, _, _, _ = SyncProfiles(win, winLinks, nil, winBase, winState, winClock, out, avatar)
	if got := colors(win, added); !reflect.DeepEqual(got, green) {
		t.Fatalf("a shade change was written: %v, want %v", got, green)
	}
	// The Mac goes back to no colours: Windows too.
	profiles.SetColors(mac, "Default", nil)
	_, out, _, _ = SyncProfiles(mac, macLinks, nil, macBase, macState, macClock, nil, avatar)
	SyncProfiles(win, winLinks, nil, winBase, winState, winClock, out, avatar)
	if got := colors(win, added); got != nil {
		t.Fatalf("colours kept after the Mac removed them: %v", got)
	}
	if got := colors(win, "Default"); got != nil {
		t.Fatalf("an unsynced profile got colours: %v", got)
	}
}
