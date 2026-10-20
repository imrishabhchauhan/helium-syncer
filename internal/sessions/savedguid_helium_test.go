//go:build helium

package sessions

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/imrishabhchauhan/helium-syncer/internal/testkit"
)

// Helium 0.19.4.1 facts the tab merge relies on (see findings doc):
//   - a group written WITHOUT a saved-group guid is restored, and Helium
//     gives it a saved guid that then stays the same across restarts;
//   - a group written with a saved guid Helium does not know is treated as
//     deleted on another device and its tabs are CLOSED. So the writer must
//     only ever reuse saved guids that the local profile already has.
func TestGroupWithoutSavedGUIDGetsStableGUID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "<title>%s</title>", r.URL.Path)
	}))
	defer srv.Close()
	root, _ := filepath.Abs("../..")
	ud := filepath.Join(root, "test-profiles", fmt.Sprintf("it-savedguid-%d", time.Now().Unix()))
	opts := testkit.Options{Exe: heliumExe(t), UserDataDir: ud, Port: 9356, ProbeDir: filepath.Join(root, "testing", "probe-extension")}
	profile := filepath.Join(ud, "Default")

	in, err := testkit.Start(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := in.Close(ctx); err != nil {
		t.Fatal(err)
	}
	key := [2]uint64{0xabc, 0xdef}
	s := &Session{Groups: map[[2]uint64]*Group{key: {High: key[0], Low: key[1], Title: "Synced group", Color: 4}}}
	s.Windows = []*Window{{Tabs: []*Tab{
		{Group: &key, Navigations: []Navigation{{URL: srv.URL + "/one", Transition: 0x30000000}}},
		{Group: &key, Navigations: []Navigation{{URL: srv.URL + "/two", Transition: 0x30000000}}},
	}}}
	if _, err := WriteNew(profile, s); err != nil {
		t.Fatal(err)
	}
	setRestoreLastSession(t, profile)

	opts.NoURL = true
	first := ""
	for run := 1; run <= 2; run++ { // twice: Helium rewrites the session each run
		in, err = testkit.Start(ctx, opts)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
		if err := in.Close(ctx); err != nil {
			t.Fatal(err)
		}
		latest, err := LatestFile(profile)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ReadFile(latest)
		if err != nil {
			t.Fatal(err)
		}
		grouped := 0
		for _, w := range got.Windows {
			for _, tb := range w.Tabs {
				if tb.Group != nil {
					grouped++
				}
			}
		}
		if len(got.Groups) != 1 || grouped != 2 {
			t.Fatalf("run %d: want 1 group with 2 tabs, got %d groups, %d grouped tabs", run, len(got.Groups), grouped)
		}
		for _, g := range got.Groups {
			if g.SavedGUID == "" {
				t.Fatalf("run %d: Helium did not assign a saved guid", run)
			}
			if first == "" {
				first = g.SavedGUID
			} else if g.SavedGUID != first {
				t.Errorf("saved guid not stable: %s then %s", first, g.SavedGUID)
			}
		}
	}
}
