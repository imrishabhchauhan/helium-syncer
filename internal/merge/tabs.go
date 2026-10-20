package merge

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/imrishabhchauhan/helium-syncer/internal/crdt"
	"github.com/imrishabhchauhan/helium-syncer/internal/sessions"
)

// Op types for open tabs.
const (
	TypeTab   = "tab"   // key "<url>#<n>": the n-th open tab with that URL
	TypeGroup = "group" // key: synced group id
)

// TabItem is the synced form of one open web tab.
type TabItem struct {
	Window int    `json:"w"`           // window number (0 = first window)
	Order  string `json:"o"`           // position in the window
	Pinned bool   `json:"p,omitempty"` // pinned tabs are never in a group
	Group  string `json:"g,omitempty"` // synced group id
}

// GroupItem is the synced form of one tab group.
type GroupItem struct {
	Title     string `json:"n"`
	Color     uint32 `json:"c"`
	Collapsed bool   `json:"x,omitempty"`
}

// TabSync is what a device keeps between syncs for its tabs.
type TabSync struct {
	Tabs   map[string]json.RawMessage `json:"tabs"`
	Groups map[string]json.RawMessage `json:"groups"`
	// Helium's saved-group guid on this device -> synced group id. Helium
	// assigns saved guids itself; a guid it does not know must never be
	// written (it would close the group's tabs).
	GroupIDs map[string]string `json:"group_ids"`
}

// NewTabSync returns empty state for a device that never synced tabs.
func NewTabSync() *TabSync {
	return &TabSync{Tabs: map[string]json.RawMessage{}, Groups: map[string]json.RawMessage{}, GroupIDs: map[string]string{}}
}

// Syncable reports whether a tab's page travels between devices. Local
// pages (settings, extensions, files, new tab) stay on their device.
func Syncable(url string) bool {
	return strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://")
}

func tabKey(url string, n int) string { return url + "#" + strconv.Itoa(n) }

// URLOfTabKey returns the URL part of a tab key.
func URLOfTabKey(k string) string {
	if i := strings.LastIndexByte(k, '#'); i >= 0 {
		return k[:i]
	}
	return k
}

func normalWindows(s *sessions.Session) []*sessions.Window {
	var out []*sessions.Window
	for _, w := range s.Windows {
		if w.Type == 0 {
			out = append(out, w)
		}
	}
	return out
}

// windowNumbers gives each normal window its synced number: only windows
// with at least one web tab are numbered (0, 1, ...), so a window holding
// only local pages on one device does not shift numbering on the others.
// Windows without web tabs get -1.
func windowNumbers(s *sessions.Session) []int {
	wins := normalWindows(s)
	out := make([]int, len(wins))
	n := 0
	for i, w := range wins {
		out[i] = -1
		for _, t := range w.Tabs {
			if cur := t.Current(); cur != nil && Syncable(cur.URL) {
				out[i] = n
				n++
				break
			}
		}
	}
	return out
}

// keyedTabs walks the session's numbered windows and keys every web tab.
func keyedTabs(s *sessions.Session, fn func(win int, key string, t *sessions.Tab)) {
	count := map[string]int{}
	nums := windowNumbers(s)
	for i, w := range normalWindows(s) {
		if nums[i] < 0 {
			continue
		}
		for _, t := range w.Tabs {
			cur := t.Current()
			if cur == nil || !Syncable(cur.URL) {
				continue
			}
			k := tabKey(cur.URL, count[cur.URL])
			count[cur.URL]++
			fn(nums[i], k, t)
		}
	}
}

// extractTabs reads the synced view of a session. tokenIDs (optional) names
// groups this device just wrote and Helium has not given a saved guid yet.
func extractTabs(s *sessions.Session, prev, stateTabs map[string]json.RawMessage, stateGroups map[string]json.RawMessage,
	groupIDs map[string]string, tokenIDs map[[2]uint64]string) (tabs, groups map[string]json.RawMessage) {
	// 1. Resolve each local group to a synced id.
	localKeysByGroup := map[[2]uint64][]string{}
	keyedTabs(s, func(_ int, k string, t *sessions.Tab) {
		if t.Group != nil && !t.Pinned {
			localKeysByGroup[*t.Group] = append(localKeysByGroup[*t.Group], k)
		}
	})
	idOf := map[[2]uint64]string{}
	used := map[string]bool{} // synced ids already claimed by a local group
	var tokens [][2]uint64
	for tok := range s.Groups {
		tokens = append(tokens, tok)
	}
	sort.Slice(tokens, func(i, j int) bool { return fmt.Sprint(tokens[i]) < fmt.Sprint(tokens[j]) })
	for _, tok := range tokens {
		g := s.Groups[tok]
		if id, ok := tokenIDs[tok]; ok {
			idOf[tok] = id
		} else if id, ok := groupIDs[g.SavedGUID]; ok && g.SavedGUID != "" {
			idOf[tok] = id
		}
	}
	for _, id := range idOf {
		used[id] = true
	}
	for _, tok := range tokens {
		if _, ok := idOf[tok]; ok {
			continue
		}
		g := s.Groups[tok]
		// A group Helium re-created from what we wrote: find the synced group
		// with the most of its tabs, then the same title and colour.
		best, bestScore := "", 0
		var cands []string
		for id := range stateGroups {
			if !used[id] {
				cands = append(cands, id)
			}
		}
		sort.Strings(cands)
		for _, id := range cands {
			var gi GroupItem
			json.Unmarshal(stateGroups[id], &gi)
			score := 0
			for _, k := range localKeysByGroup[tok] {
				var ti TabItem
				if raw, ok := stateTabs[k]; ok && json.Unmarshal(raw, &ti) == nil && ti.Group == id {
					score += 2
				}
			}
			if gi.Title == g.Title && gi.Color == g.Color {
				score++
			}
			if score > bestScore {
				best, bestScore = id, score
			}
		}
		switch {
		case best != "":
			idOf[tok] = best
		case g.SavedGUID != "":
			idOf[tok] = g.SavedGUID
		default:
			idOf[tok] = fmt.Sprintf("t-%016x%016x", tok[0], tok[1])
		}
		used[idOf[tok]] = true
	}
	for tok, id := range idOf {
		if g := s.Groups[tok]; g.SavedGUID != "" {
			groupIDs[g.SavedGUID] = id
		}
	}

	// 2. Tabs, with order keys reused where the order is unchanged.
	prevItems := map[string]TabItem{}
	for k, v := range prev {
		var ti TabItem
		if json.Unmarshal(v, &ti) == nil {
			prevItems[k] = ti
		}
	}
	tabs = map[string]json.RawMessage{}
	groups = map[string]json.RawMessage{}
	type entry struct {
		key string
		t   *sessions.Tab
	}
	perWindow := map[int][]entry{}
	keyedTabs(s, func(wi int, k string, t *sessions.Tab) { perWindow[wi] = append(perWindow[wi], entry{k, t}) })
	for wi, list := range perWindow {
		pos := make([]Positioned, len(list))
		for i, e := range list {
			pos[i].ID = e.key
			if p, ok := prevItems[e.key]; ok && p.Window == wi {
				pos[i].Known = p.Order
			}
		}
		orders := AssignOrder(pos)
		for i, e := range list {
			ti := TabItem{Window: wi, Order: orders[i], Pinned: e.t.Pinned}
			if e.t.Group != nil && !e.t.Pinned {
				ti.Group = idOf[*e.t.Group]
				g := s.Groups[*e.t.Group]
				if g != nil {
					raw, _ := json.Marshal(GroupItem{Title: g.Title, Color: g.Color, Collapsed: g.Collapsed})
					groups[ti.Group] = raw
				}
			}
			raw, _ := json.Marshal(ti)
			tabs[e.key] = raw
		}
	}
	return tabs, groups
}

// projectTabs builds the session this device should restore from merged
// state. It reuses local tabs (with their back/forward history) where the
// same page is open, keeps local-only pages where they were, and creates new
// tabs for pages opened on other devices. It returns the session and the
// synced ids of groups it created without a saved guid (by token).
func projectTabs(stateTabs, stateGroups *crdt.Map, local *sessions.Session, groupIDs map[string]string,
	titleOf func(url string) string, now int64) (*sessions.Session, map[[2]uint64]string) {
	localTab := map[string]*sessions.Tab{}
	keyedTabs(local, func(_ int, k string, t *sessions.Tab) { localTab[k] = t })
	allWins := normalWindows(local)
	nums := windowNumbers(local)
	wins := map[int]*sessions.Window{} // synced number -> local window
	for i, w := range allWins {
		if nums[i] >= 0 {
			wins[nums[i]] = w
		}
	}

	// Synced group id -> local token and saved guid (only guids Helium knows).
	type localGroup struct {
		tok   [2]uint64
		saved string
	}
	localByID := map[string]localGroup{}
	for tok, g := range local.Groups {
		if id, ok := groupIDs[g.SavedGUID]; ok && g.SavedGUID != "" {
			localByID[id] = localGroup{tok, g.SavedGUID}
		}
	}

	items := map[string]TabItem{}
	maxWin := -1
	for _, k := range stateTabs.Live() {
		v, _ := stateTabs.Get(k)
		var ti TabItem
		if json.Unmarshal(v, &ti) == nil {
			items[k] = ti
			if ti.Window > maxWin {
				maxWin = ti.Window
			}
		}
	}
	nWin := maxWin + 1
	// Local-only tabs keep their window and place.
	type kept struct {
		index int
		t     *sessions.Tab
	}
	localOnly := map[int][]kept{}
	for wi, w := range wins {
		for i, t := range w.Tabs {
			if cur := t.Current(); cur == nil || !Syncable(cur.URL) {
				localOnly[wi] = append(localOnly[wi], kept{i, t})
			}
		}
	}

	out := &sessions.Session{Groups: map[[2]uint64]*sessions.Group{}, ActiveWindow: local.ActiveWindow}
	created := map[[2]uint64]string{}
	tokenFor := map[string][2]uint64{}
	groupToken := func(id string) *[2]uint64 {
		if tok, ok := tokenFor[id]; ok {
			return &tok
		}
		raw, ok := stateGroups.Get(id)
		if !ok {
			return nil // group deleted elsewhere: the tab stays, ungrouped
		}
		var gi GroupItem
		json.Unmarshal(raw, &gi)
		g := &sessions.Group{Title: gi.Title, Color: gi.Color, Collapsed: gi.Collapsed}
		var tok [2]uint64
		if lg, ok := localByID[id]; ok {
			tok, g.SavedGUID = lg.tok, lg.saved
		} else {
			tok = randomToken()
			created[tok] = id
		}
		g.High, g.Low = tok[0], tok[1]
		out.Groups[tok] = g
		tokenFor[id] = tok
		return &tok
	}

	for wi := 0; wi < nWin; wi++ {
		var keys []string
		for k, ti := range items {
			if ti.Window == wi {
				keys = append(keys, k)
			}
		}
		sort.Slice(keys, func(i, j int) bool {
			a, b := items[keys[i]], items[keys[j]]
			if a.Pinned != b.Pinned {
				return a.Pinned // Helium keeps pinned tabs first
			}
			return less(a.Order, keys[i], b.Order, keys[j])
		})
		w := &sessions.Window{X: 80, Y: 60, W: 1280, H: 800, ShowState: 1}
		var selected *sessions.Tab
		if lw := wins[wi]; lw != nil {
			w.ID, w.X, w.Y, w.W, w.H, w.ShowState = lw.ID, lw.X, lw.Y, lw.W, lw.H, lw.ShowState
			if int(lw.SelectedTab) < len(lw.Tabs) && lw.SelectedTab >= 0 {
				selected = lw.Tabs[lw.SelectedTab]
			}
		}
		for _, k := range keys {
			ti := items[k]
			t := localTab[k]
			if t == nil {
				url := URLOfTabKey(k)
				t = &sessions.Tab{Navigations: []sessions.Navigation{{URL: url, Title: titleOf(url), Transition: 0x30000000, Timestamp: now}}}
			} else {
				c := *t
				t = &c
			}
			t.Pinned = ti.Pinned
			t.Group = nil
			if ti.Group != "" && !ti.Pinned {
				t.Group = groupToken(ti.Group)
			}
			w.Tabs = append(w.Tabs, t)
		}
		for _, lo := range localOnly[wi] {
			i := min(lo.index, len(w.Tabs))
			w.Tabs = append(w.Tabs[:i], append([]*sessions.Tab{lo.t}, w.Tabs[i:]...)...)
		}
		if len(w.Tabs) == 0 {
			continue
		}
		for i, t := range w.Tabs {
			if t == selected || (selected != nil && t.Current() != nil && selected.Current() != nil && t.Current().URL == selected.Current().URL) {
				w.SelectedTab = int32(i)
				break
			}
		}
		out.Windows = append(out.Windows, w)
	}
	// Windows with only local pages stay as they are.
	for i, w := range allWins {
		if nums[i] < 0 {
			out.Windows = append(out.Windows, w)
		}
	}
	// Non-normal windows (popups, apps) are device-local too.
	for _, w := range local.Windows {
		if w.Type != 0 {
			out.Windows = append(out.Windows, w)
		}
	}
	return out, created
}

func randomToken() [2]uint64 {
	var b [16]byte
	rand.Read(b[:])
	return [2]uint64{binary.LittleEndian.Uint64(b[:8]) | 1, binary.LittleEndian.Uint64(b[8:])}
}

// SyncTabs runs one sync step for a device's open tabs. titleOf gives a title
// for pages this device has never opened (from synced history).
func SyncTabs(local *sessions.Session, ts *TabSync, stateTabs, stateGroups *crdt.Map, clock *crdt.Clock,
	incoming []crdt.Op, titleOf func(string) string) (*sessions.Session, []crdt.Op) {
	curTabs, curGroups := extractTabs(local, ts.Tabs, liveMap(stateTabs), liveMap(stateGroups), ts.GroupIDs, nil)
	out := crdt.Diff(TypeTab, ts.Tabs, curTabs, clock)
	out = append(out, crdt.Diff(TypeGroup, ts.Groups, curGroups, clock)...)
	for _, op := range incoming {
		clock.Observe(op.Stamp)
	}
	apply := func(op crdt.Op) {
		switch op.Type {
		case TypeTab:
			stateTabs.Apply(op)
		case TypeGroup:
			stateGroups.Apply(op)
		}
	}
	for _, op := range incoming {
		apply(op)
	}
	for _, op := range out {
		apply(op)
	}
	projected, created := projectTabs(stateTabs, stateGroups, local, ts.GroupIDs, titleOf, time.Now().UnixMicro()+11644473600*1_000_000)
	ts.Tabs, ts.Groups = extractTabs(projected, liveMap(stateTabs), liveMap(stateTabs), liveMap(stateGroups), ts.GroupIDs, created)
	// Decisions made while projecting (e.g. a pinned tab leaves its group,
	// a group whose tabs all closed disappears) become ops too.
	repairs := crdt.Diff(TypeTab, liveMap(stateTabs), ts.Tabs, clock)
	repairs = append(repairs, crdt.Diff(TypeGroup, liveMap(stateGroups), ts.Groups, clock)...)
	for _, op := range repairs {
		apply(op)
	}
	return projected, append(out, repairs...)
}
