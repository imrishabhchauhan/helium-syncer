package merge

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/imrishabhchauhan/helium-syncer/internal/crdt"
	"github.com/imrishabhchauhan/helium-syncer/internal/history"
)

type histDevice struct {
	name           string
	db             *history.DB
	base           History
	visits, titles *crdt.Map
	clock          *crdt.Clock
	seen           int
	deleted        map[string]bool // visit keys this device deleted
	now            int64
}

func newHistDevice(t *testing.T, name string) *histDevice {
	path := filepath.Join(t.TempDir(), "History")
	if err := history.CreateEmptyForTests(path); err != nil {
		t.Fatal(err)
	}
	db, err := history.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &histDevice{name: name, db: db, visits: crdt.NewMap(), titles: crdt.NewMap(),
		clock: crdt.NewClock(name, nil), deleted: map[string]bool{}, now: 13_400_000_000_000_000}
}

func (d *histDevice) sync(t *testing.T, s *server, cutoff int64) int {
	incoming := append([]crdt.Op(nil), s.log[d.seen:]...)
	base, out, err := SyncHistory(d.db, d.base, d.visits, d.titles, d.clock, incoming, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	s.log = append(s.log, out...)
	d.seen = len(s.log)
	d.base = base
	return len(out)
}

var pages = []string{"https://a.example/", "https://b.example/हिंदी", "https://c.example/?q=1&r=2", "https://d.example/😀", "https://e.example/x"}

// edit makes one local change like browsing in Helium would.
func (d *histDevice) edit(t *testing.T, r *rand.Rand, seq int) {
	d.now += int64(1 + r.Intn(5_000_000))
	switch op := r.Intn(10); {
	case op < 6: // visit a page (sometimes with a new title)
		u := pages[r.Intn(len(pages))]
		title := fmt.Sprintf("Title %d by %s", seq, d.name)
		if _, err := d.db.AddVisits([]history.Visit{{URL: u, Title: title, VisitTime: d.now, Transition: 0x30000000 | int64(r.Intn(2))}}); err != nil {
			t.Fatal(err)
		}
	case op < 8: // delete one visit
		vs, _ := d.db.Visits(0)
		if len(vs) == 0 {
			return
		}
		v := vs[r.Intn(len(vs))]
		d.deleted[visitKey(v.URL, v.VisitTime)] = true
		d.db.DeleteVisits([][2]any{{v.URL, v.VisitTime}})
	default: // "remove this page from history" (all its visits)
		vs, _ := d.db.Visits(0)
		if len(vs) == 0 {
			return
		}
		u := vs[r.Intn(len(vs))].URL
		var keys [][2]any
		for _, v := range vs {
			if v.URL == u {
				keys = append(keys, [2]any{v.URL, v.VisitTime})
				d.deleted[visitKey(v.URL, v.VisitTime)] = true
			}
		}
		d.db.DeleteVisits(keys)
	}
}

func snapshotHistory(t *testing.T, d *histDevice) (visits []string, titles map[string]string) {
	h, err := ExtractHistory(d.db, 0)
	if err != nil {
		t.Fatal(err)
	}
	for k := range h.Visits {
		visits = append(visits, k)
	}
	sort.Strings(visits)
	titles = map[string]string{}
	for u, raw := range h.Titles {
		var s string
		json.Unmarshal(raw, &s)
		titles[u] = s
	}
	return visits, titles
}

func TestHistoryConvergeRandomized(t *testing.T) {
	for _, n := range []int{2, 3} {
		for round := 0; round < rounds(40, 300); round++ {
			r := rand.New(rand.NewSource(int64(round*10 + n)))
			s := &server{}
			devs := []*histDevice{newHistDevice(t, "mac"), newHistDevice(t, "win"), newHistDevice(t, "laptop")}[:n]
			created := map[string]bool{}
			for step := 0; step < 25; step++ {
				d := devs[r.Intn(n)]
				for i := r.Intn(4); i >= 0; i-- {
					d.edit(t, r, step*10+i)
				}
				h, _ := ExtractHistory(d.db, 0)
				for k := range h.Visits {
					created[k] = true
				}
				if r.Intn(3) == 0 {
					d.sync(t, s, 0)
				}
			}
			for i := 0; i < 2; i++ {
				for _, d := range devs {
					d.sync(t, s, 0)
				}
			}
			v0, t0 := snapshotHistory(t, devs[0])
			for _, d := range devs[1:] {
				v, ti := snapshotHistory(t, d)
				if !reflect.DeepEqual(v, v0) {
					t.Fatalf("n=%d round %d: visits diverged\n%s: %v\n%s: %v", n, round, devs[0].name, v0, d.name, v)
				}
				if !reflect.DeepEqual(ti, t0) {
					t.Fatalf("n=%d round %d: titles diverged\n%v\n%v", n, round, t0, ti)
				}
			}
			ops := 0
			for _, d := range devs {
				ops += d.sync(t, s, 0)
			}
			if ops != 0 {
				t.Fatalf("n=%d round %d: %d ops after convergence (ping-pong)", n, round, ops)
			}
			final := map[string]bool{}
			for _, k := range v0 {
				final[k] = true
			}
			for k := range created {
				deleted := false
				for _, d := range devs {
					deleted = deleted || d.deleted[k]
				}
				if !final[k] && !deleted {
					t.Fatalf("n=%d round %d: visit %s lost without anyone deleting it", n, round, k)
				}
			}
		}
	}
}

// Visits that age out of the window must not become deletions elsewhere.
func TestHistoryExpiryIsNotDeletion(t *testing.T) {
	s := &server{}
	a, b := newHistDevice(t, "mac"), newHistDevice(t, "win")
	old := int64(1_000)
	a.db.AddVisits([]history.Visit{{URL: "https://old.example/", Title: "Old", VisitTime: old, Transition: 0x30000000}})
	a.sync(t, s, 0)
	b.sync(t, s, 0)
	// Time passes: the visit is now outside the window on both devices.
	if n := a.sync(t, s, old+1); n != 0 {
		t.Fatalf("expiry produced %d ops, want 0", n)
	}
	b.sync(t, s, old+1)
	vs, _ := b.db.Visits(0)
	if len(vs) != 1 {
		t.Fatalf("the other device lost an expired-but-present visit: %v", vs)
	}
}

// rounds returns quick unless HS_LONG=1 is set (release checks).
func rounds(quick, long int) int {
	if os.Getenv("HS_LONG") == "1" {
		return long
	}
	return quick
}
