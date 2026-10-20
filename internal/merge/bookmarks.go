// Package merge turns Helium data into synced items and back.
//
// Each data type has two halves:
//   - Extract: local file -> map[key]canonical JSON (what this device has)
//   - Project: merged crdt.Map -> local file (what this device should have)
//
// A device keeps the snapshot extracted right after its last apply ("base").
// Local changes are crdt.Diff(base, Extract(now)); after merging, Project is
// written and Extract(written) becomes the new base. Extract(Project(state))
// must not produce new ops (fixpoint), or devices would ping-pong forever.
package merge

import (
	"encoding/json"
	"sort"
	"strconv"

	"github.com/imrishabhchauhan/helium-syncer/internal/bookmarks"
	"github.com/imrishabhchauhan/helium-syncer/internal/crdt"
)

// Permanent bookmark folders have the same guid on every device
// (components/bookmarks/browser/bookmark_node.cc).
var bookmarkRoots = map[string]string{
	"bookmark_bar": "0bc5d13f-2cba-5d74-951f-3f233fe6c908",
	"other":        "82b081ec-3dd3-529c-8475-ab6c344590dd",
	"synced":       "4cf2e351-0e85-532b-bb37-df045d8f8d0f",
}

// OtherBookmarksGUID is where orphans are rescued to.
var OtherBookmarksGUID = bookmarkRoots["other"]

// Bookmark is the synced form of one bookmark or folder. Device-only fields
// (local id, last used time, folder modified time) are not part of it.
type Bookmark struct {
	Parent    string `json:"p"`
	Order     string `json:"o"`
	Folder    bool   `json:"f,omitempty"`
	Title     string `json:"n"`
	URL       string `json:"u,omitempty"`
	DateAdded string `json:"a,omitempty"`
}

// ExtractBookmarks reads every non-root node. Order keys are reused from
// prev (the base snapshot or merged state) where the order is unchanged.
func ExtractBookmarks(f *bookmarks.File, prev map[string]json.RawMessage) map[string]json.RawMessage {
	prevOrder := map[string]Bookmark{}
	for k, v := range prev {
		var b Bookmark
		if json.Unmarshal(v, &b) == nil {
			prevOrder[k] = b
		}
	}
	out := map[string]json.RawMessage{}
	var visit func(parentGUID string, node map[string]any)
	visit = func(parentGUID string, node map[string]any) {
		kids, _ := node["children"].([]any)
		var nodes []map[string]any
		var known []Positioned
		for _, k := range kids {
			km, ok := k.(map[string]any)
			if !ok {
				continue
			}
			g := str(km["guid"])
			if g == "" {
				continue
			}
			nodes = append(nodes, km)
			pos := Positioned{ID: g}
			if p, ok := prevOrder[g]; ok && p.Parent == parentGUID {
				pos.Known = p.Order
			}
			known = append(known, pos)
		}
		orders := AssignOrder(known)
		for i, km := range nodes {
			g := str(km["guid"])
			b := Bookmark{Parent: parentGUID, Order: orders[i], Title: str(km["name"]), DateAdded: str(km["date_added"])}
			if str(km["type"]) == "folder" {
				b.Folder = true
				visit(g, km)
			} else {
				b.URL = str(km["url"])
			}
			raw, _ := json.Marshal(b)
			out[g] = raw
		}
	}
	for root, guid := range bookmarkRoots {
		if n := f.Root(root); n != nil {
			visit(guid, n)
		}
	}
	return out
}

// ProjectBookmarks builds the bookmark tree from merged state onto local.
// Local node ids and unknown fields are kept for nodes that already exist;
// new nodes get fresh ids. Live items whose parent is gone (deleted on
// another device, or a move cycle) are rescued into Other bookmarks, so a
// bookmark is never lost because of a concurrent folder delete.
func ProjectBookmarks(state *crdt.Map, local *bookmarks.File) *bookmarks.File {
	items := map[string]Bookmark{}
	for _, k := range state.Live() {
		v, _ := state.Get(k)
		var b Bookmark
		if json.Unmarshal(v, &b) == nil {
			items[k] = b
		}
	}
	existing := map[string]map[string]any{}
	local.Walk(func(n map[string]any, _ int) {
		if g := str(n["guid"]); g != "" {
			existing[g] = n
		}
	})
	children := map[string][]string{}
	for g, b := range items {
		children[b.Parent] = append(children[b.Parent], g)
	}
	sortKids := func(list []string) {
		sort.Slice(list, func(i, j int) bool {
			a, b := items[list[i]], items[list[j]]
			if a.Order != b.Order {
				return a.Order < b.Order
			}
			return list[i] < list[j]
		})
	}
	// Reachability from the roots decides who is an orphan.
	reached := map[string]bool{}
	var mark func(parent string)
	mark = func(parent string) {
		for _, g := range children[parent] {
			if !reached[g] {
				reached[g] = true
				if items[g].Folder {
					mark(g)
				}
			}
		}
	}
	for _, guid := range bookmarkRoots {
		mark(guid)
	}
	// Rescue unreachable items into Other bookmarks. First the tops of
	// orphaned subtrees (parent deleted or unknown); then, for move cycles
	// (A in B, B in A), the smallest guid of each cycle. The choice depends
	// only on the merged state, so every device rescues the same items.
	rescued := map[string]bool{}
	var orphans []string
	rescue := func(g string) {
		rescued[g] = true
		reached[g] = true
		orphans = append(orphans, g)
		if items[g].Folder {
			mark(g)
		}
	}
	var unreached []string
	for g := range items {
		if !reached[g] {
			unreached = append(unreached, g)
		}
	}
	sort.Strings(unreached)
	for _, g := range unreached {
		if _, parentLive := items[items[g].Parent]; !parentLive && !reached[g] {
			rescue(g)
		}
	}
	for _, g := range unreached {
		if !reached[g] {
			rescue(g) // part of a cycle
		}
	}
	sortKids(orphans)

	nextID := local.MaxID() + 1
	var build func(g string, onPath map[string]bool) map[string]any
	build = func(g string, onPath map[string]bool) map[string]any {
		b := items[g]
		n := existing[g]
		if n == nil {
			n = map[string]any{"id": strconv.FormatInt(nextID, 10), "guid": g, "date_last_used": "0"}
			nextID++
		}
		n["name"] = b.Title
		if b.DateAdded != "" {
			n["date_added"] = b.DateAdded
		} else if _, ok := n["date_added"]; !ok {
			n["date_added"] = "0"
		}
		if b.Folder {
			n["type"] = "folder"
			delete(n, "url")
			if _, ok := n["date_modified"]; !ok {
				n["date_modified"] = "0"
			}
			onPath[g] = true
			n["children"] = buildList(children[g], sortKids, func(c string) map[string]any {
				if onPath[c] || rescued[c] {
					return nil
				}
				return build(c, onPath)
			})
			delete(onPath, g)
		} else {
			n["type"] = "url"
			n["url"] = b.URL
			delete(n, "children")
			delete(n, "date_modified")
		}
		return n
	}
	for root, guid := range bookmarkRoots {
		rn := local.Root(root)
		if rn == nil {
			continue
		}
		list := append([]string(nil), children[guid]...)
		var tail []any
		if guid == OtherBookmarksGUID {
			for _, g := range orphans {
				tail = append(tail, build(g, map[string]bool{}))
			}
		}
		kids := buildList(list, sortKids, func(c string) map[string]any {
			if rescued[c] {
				return nil
			}
			return build(c, map[string]bool{})
		})
		rn["children"] = append(kids, tail...)
	}
	return local
}

// SyncBookmarks runs one sync step for a device:
//  1. local changes since base become ops (out),
//  2. local + incoming ops are merged into state,
//  3. the merged tree is projected onto the local file,
//  4. any decision the projection had to make on its own (rescuing a
//     bookmark whose folder was deleted elsewhere, breaking a move cycle) is
//     turned into ops too, so the shared state matches what devices show.
//
// It returns the new file, the new base and every op this device produced.
func SyncBookmarks(local *bookmarks.File, base map[string]json.RawMessage, state *crdt.Map,
	clock *crdt.Clock, incoming []crdt.Op) (*bookmarks.File, map[string]json.RawMessage, []crdt.Op) {
	cur := ExtractBookmarks(local, base)
	out := crdt.Diff(TypeBookmark, base, cur, clock)
	for _, op := range incoming {
		clock.Observe(op.Stamp)
		state.Apply(op)
	}
	for _, op := range out {
		state.Apply(op)
	}
	projected := ProjectBookmarks(state, local)
	newBase := ExtractBookmarks(projected, liveMap(state))
	repairs := crdt.Diff(TypeBookmark, liveMap(state), newBase, clock)
	for _, op := range repairs {
		state.Apply(op)
	}
	return projected, newBase, append(out, repairs...)
}

// TypeBookmark is the op type for bookmarks.
const TypeBookmark = "bookmark"

func liveMap(m *crdt.Map) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, k := range m.Live() {
		v, _ := m.Get(k)
		out[k] = v
	}
	return out
}

func buildList(list []string, sortKids func([]string), build func(string) map[string]any) []any {
	list = append([]string(nil), list...)
	sortKids(list)
	out := []any{}
	for _, g := range list {
		if n := build(g); n != nil {
			out = append(out, n)
		}
	}
	return out
}

func str(v any) string { s, _ := v.(string); return s }
