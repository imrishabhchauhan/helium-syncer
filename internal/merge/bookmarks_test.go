package merge

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/imrishabhchauhan/helium-syncer/internal/bookmarks"
	"github.com/imrishabhchauhan/helium-syncer/internal/crdt"
)

const emptyBookmarks = `{"checksum":"","roots":{
 "bookmark_bar":{"children":[],"date_added":"0","date_modified":"0","guid":"0bc5d13f-2cba-5d74-951f-3f233fe6c908","id":"1","name":"Bookmarks bar","type":"folder"},
 "other":{"children":[],"date_added":"0","date_modified":"0","guid":"82b081ec-3dd3-529c-8475-ab6c344590dd","id":"2","name":"Other bookmarks","type":"folder"},
 "synced":{"children":[],"date_added":"0","date_modified":"0","guid":"4cf2e351-0e85-532b-bb37-df045d8f8d0f","id":"3","name":"Mobile bookmarks","type":"folder"}},"version":1}`

// server is the shared op log (what the Cloudflare backend will hold).
type server struct{ log []crdt.Op }

type device struct {
	name  string
	file  *bookmarks.File
	base  map[string]json.RawMessage
	state *crdt.Map
	clock *crdt.Clock
	seen  int
	// guids this device deleted (whole subtrees, as it saw them)
	deleted map[string]bool
}

func newDevice(t *testing.T, name string) *device {
	f, err := bookmarks.Parse([]byte(emptyBookmarks))
	if err != nil {
		t.Fatal(err)
	}
	return &device{name: name, file: f, base: map[string]json.RawMessage{}, state: crdt.NewMap(),
		clock: crdt.NewClock(name, nil), deleted: map[string]bool{}}
}

func liveValues(m *crdt.Map) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, k := range m.Live() {
		v, _ := m.Get(k)
		out[k] = v
	}
	return out
}

// sync is one quit/open cycle: upload local changes, apply everything.
func (d *device) sync(s *server) int {
	incoming := append([]crdt.Op(nil), s.log[d.seen:]...)
	projected, base, out := SyncBookmarks(d.file, d.base, d.state, d.clock, incoming)
	s.log = append(s.log, out...)
	d.seen = len(s.log)
	// Round-trip through bytes like a real file write.
	f, err := bookmarks.Parse(projected.Encode("linux"))
	if err != nil {
		panic(err)
	}
	d.file = f
	d.base = base
	return len(out)
}

// --- random local edits, like a user would make in Helium ---

func folders(f *bookmarks.File) []map[string]any {
	var out []map[string]any
	f.Walk(func(n map[string]any, _ int) {
		if n["type"] == "folder" {
			out = append(out, n)
		}
	})
	return out
}

func nonRoot(f *bookmarks.File) []map[string]any {
	var out []map[string]any
	f.Walk(func(n map[string]any, depth int) {
		if depth > 0 {
			out = append(out, n)
		}
	})
	return out
}

func parentOf(f *bookmarks.File, target map[string]any) (map[string]any, int) {
	var parent map[string]any
	idx := -1
	f.Walk(func(n map[string]any, _ int) {
		kids, _ := n["children"].([]any)
		for i, k := range kids {
			if reflect.ValueOf(k).Pointer() == reflect.ValueOf(target).Pointer() {
				parent, idx = n, i
			}
		}
	})
	return parent, idx
}

func inSubtree(root, n map[string]any) bool {
	if reflect.ValueOf(root).Pointer() == reflect.ValueOf(n).Pointer() {
		return true
	}
	kids, _ := root["children"].([]any)
	for _, k := range kids {
		if inSubtree(k.(map[string]any), n) {
			return true
		}
	}
	return false
}

func collectGUIDs(n map[string]any, into map[string]bool) {
	into[str(n["guid"])] = true
	kids, _ := n["children"].([]any)
	for _, k := range kids {
		collectGUIDs(k.(map[string]any), into)
	}
}

var seq int

func (d *device) edit(r *rand.Rand, allowDelete bool) {
	f := d.file
	fs := folders(f)
	nodes := nonRoot(f)
	insert := func(folder map[string]any, n map[string]any) {
		kids, _ := folder["children"].([]any)
		i := r.Intn(len(kids) + 1)
		folder["children"] = append(kids[:i], append([]any{n}, kids[i:]...)...)
	}
	seq++
	switch op := r.Intn(10); {
	case op < 3: // add a bookmark
		insert(fs[r.Intn(len(fs))], map[string]any{"guid": fmt.Sprintf("g-%s-%d", d.name, seq), "id": fmt.Sprint(f.MaxID() + 1),
			"type": "url", "name": fmt.Sprintf("Link %d हिंदी", seq), "url": fmt.Sprintf("https://ex.com/%d", seq), "date_added": "1"})
	case op < 5: // add a folder
		insert(fs[r.Intn(len(fs))], map[string]any{"guid": fmt.Sprintf("f-%s-%d", d.name, seq), "id": fmt.Sprint(f.MaxID() + 1),
			"type": "folder", "name": fmt.Sprintf("Folder %d 📁", seq), "children": []any{}, "date_added": "1", "date_modified": "0"})
	case op < 6 && len(nodes) > 0: // rename
		nodes[r.Intn(len(nodes))]["name"] = fmt.Sprintf("Renamed %d by %s", seq, d.name)
	case op < 8 && len(nodes) > 0: // move (never into its own subtree, like Helium)
		n := nodes[r.Intn(len(nodes))]
		to := fs[r.Intn(len(fs))]
		if inSubtree(n, to) {
			return
		}
		p, i := parentOf(f, n)
		kids := p["children"].([]any)
		p["children"] = append(kids[:i:i], kids[i+1:]...)
		insert(to, n)
	case op < 9 && allowDelete && len(nodes) > 0: // delete (with subtree)
		n := nodes[r.Intn(len(nodes))]
		collectGUIDs(n, d.deleted)
		p, i := parentOf(f, n)
		kids := p["children"].([]any)
		p["children"] = append(kids[:i:i], kids[i+1:]...)
	default: // reorder inside a folder
		fo := fs[r.Intn(len(fs))]
		kids, _ := fo["children"].([]any)
		r.Shuffle(len(kids), func(i, j int) { kids[i], kids[j] = kids[j], kids[i] })
	}
}

func treeOf(f *bookmarks.File) string {
	var b strings.Builder
	f.Walk(func(n map[string]any, depth int) {
		if depth == 0 {
			fmt.Fprintf(&b, "[%s]\n", n["guid"])
			return
		}
		fmt.Fprintf(&b, "%s%s %q %v\n", strings.Repeat("  ", depth), n["guid"], n["name"], n["url"])
	})
	return b.String()
}

func liveGUIDs(f *bookmarks.File) map[string]bool {
	out := map[string]bool{}
	f.Walk(func(n map[string]any, depth int) {
		if depth > 0 {
			out[str(n["guid"])] = true
		}
	})
	return out
}

func TestBookmarksConvergeRandomized(t *testing.T) {
	for _, nDevices := range []int{2, 3} {
		for _, withDeletes := range []bool{false, true} {
			for round := 0; round < 400; round++ {
				convergeRound(t, nDevices, withDeletes, round)
			}
		}
	}
}

func convergeRound(t *testing.T, nDevices int, withDeletes bool, round int) {
	r := rand.New(rand.NewSource(int64(round*10 + nDevices)))
	s := &server{}
	devs := []*device{newDevice(t, "mac"), newDevice(t, "win"), newDevice(t, "laptop")}[:nDevices]
	created := map[string]bool{}
	for step := 0; step < 30; step++ {
		d := devs[r.Intn(nDevices)]
		for i := r.Intn(4); i >= 0; i-- {
			d.edit(r, withDeletes)
		}
		for g := range liveGUIDs(d.file) {
			created[g] = true
		}
		if r.Intn(3) == 0 {
			// When nothing new arrived from another device, syncing must
			// leave this device's tree exactly as the user left it.
			nothingPending := d.seen == len(s.log)
			before := treeOf(d.file)
			d.sync(s)
			if nothingPending && treeOf(d.file) != before {
				t.Fatalf("devices=%d deletes=%v round %d: sync rearranged %s's own bookmarks\nbefore:\n%s\nafter:\n%s",
					nDevices, withDeletes, round, d.name, before, treeOf(d.file))
			}
		}
	}
	for i := 0; i < 2; i++ {
		for _, d := range devs {
			d.sync(s)
		}
	}
	first := treeOf(devs[0].file)
	for _, d := range devs[1:] {
		if other := treeOf(d.file); other != first {
			t.Fatalf("devices=%d deletes=%v round %d: devices diverged\nmac:\n%s\n%s:\n%s", nDevices, withDeletes, round, first, d.name, other)
		}
	}
	n := 0
	for _, d := range devs {
		n += d.sync(s)
	}
	if n != 0 {
		t.Fatalf("devices=%d deletes=%v round %d: %d ops after convergence (ping-pong)", nDevices, withDeletes, round, n)
	}
	final := liveGUIDs(devs[0].file)
	for g := range created {
		deletedByAnyone := false
		for _, d := range devs {
			deletedByAnyone = deletedByAnyone || d.deleted[g]
		}
		if !final[g] && !deletedByAnyone {
			t.Fatalf("devices=%d deletes=%v round %d: %s was lost without anyone deleting it\n%s", nDevices, withDeletes, round, g, first)
		}
	}
	seen := map[string]bool{}
	devs[0].file.Walk(func(n map[string]any, _ int) {
		id := str(n["id"])
		if seen[id] {
			t.Fatalf("round %d: duplicate local id %s", round, id)
		}
		seen[id] = true
	})
}

// One small user edit must produce exactly one op (no churn on siblings).
func TestOneEditOneOp(t *testing.T) {
	s := &server{}
	a := newDevice(t, "mac")
	var kids []any
	for i := 0; i < 8; i++ {
		kids = append(kids, map[string]any{"guid": fmt.Sprint("b", i), "id": fmt.Sprint(10 + i), "type": "url",
			"name": fmt.Sprint("B", i), "url": fmt.Sprintf("https://b%d.example/", i), "date_added": "1"})
	}
	a.file.Root("bookmark_bar")["children"] = kids
	if n := a.sync(s); n != 8 {
		t.Fatalf("initial upload: %d ops, want 8", n)
	}
	bar := func() []any { return a.file.Root("bookmark_bar")["children"].([]any) }
	steps := []struct {
		name string
		do   func()
	}{
		{"rename", func() { bar()[3].(map[string]any)["name"] = "Renamed" }},
		{"move to front", func() {
			k := bar()
			x := k[5]
			a.file.Root("bookmark_bar")["children"] = append([]any{x}, append(k[:5:5], k[6:]...)...)
		}},
		{"insert in middle", func() {
			k := bar()
			n := map[string]any{"guid": "new", "id": "99", "type": "url", "name": "New", "url": "https://n.example/", "date_added": "1"}
			a.file.Root("bookmark_bar")["children"] = append(k[:4:4], append([]any{n}, k[4:]...)...)
		}},
		{"delete", func() { k := bar(); a.file.Root("bookmark_bar")["children"] = append(k[:2:2], k[3:]...) }},
	}
	for _, st := range steps {
		st.do()
		if n := a.sync(s); n != 1 {
			t.Errorf("%s: %d ops, want 1", st.name, n)
		}
		if n := a.sync(s); n != 0 {
			t.Errorf("%s: %d ops on the next sync, want 0", st.name, n)
		}
	}
}

func TestConcurrentFolderDeleteRescuesNewChild(t *testing.T) {
	s := &server{}
	a, b := newDevice(t, "mac"), newDevice(t, "win")
	bar := a.file.Root("bookmark_bar")
	bar["children"] = []any{map[string]any{"guid": "F", "id": "4", "type": "folder", "name": "Work", "children": []any{}, "date_added": "1"}}
	a.sync(s)
	b.sync(s)
	// Mac deletes the folder while Windows adds a bookmark into it.
	a.file.Root("bookmark_bar")["children"] = []any{}
	fb := b.file.Root("bookmark_bar")["children"].([]any)[0].(map[string]any)
	fb["children"] = []any{map[string]any{"guid": "NEW", "id": "9", "type": "url", "name": "Added on Windows", "url": "https://w.example/", "date_added": "1"}}
	a.sync(s)
	b.sync(s)
	a.sync(s)
	other := a.file.Root("other")["children"].([]any)
	if len(other) != 1 || str(other[0].(map[string]any)["guid"]) != "NEW" {
		t.Fatalf("bookmark added into a concurrently deleted folder must be rescued to Other bookmarks:\n%s", treeOf(a.file))
	}
	if treeOf(a.file) != treeOf(b.file) {
		t.Fatal("devices diverged")
	}
}
