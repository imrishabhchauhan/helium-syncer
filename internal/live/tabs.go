package live

// Open tabs, while Helium is open.
//
// The session files hold the tabs only for the next start: a running
// browser keeps them in memory and rewrites the files as it goes, so the
// file-based sync can only change tabs while Helium is closed. The companion
// extension reports the open windows and tabs through Helium's tabs API and
// makes the changes the app asks for, so a tab opened on one computer opens
// on the other within seconds, and one closed there closes here.
//
// The engine already knows how to merge a session (merge.SyncTabs). This
// file turns the extension's report into a session, and a merged session
// back into a short list of changes for the extension. Changes are explicit:
// the extension closes only the tabs named in the list, all of which were
// in the report the merge saw, so a tab opened since then is never closed by
// a sync that did not know about it.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket/wsjson"

	"github.com/imrishabhchauhan/helium-syncer/internal/merge"
	"github.com/imrishabhchauhan/helium-syncer/internal/sessions"
)

// TabsReport is every normal window of one browser profile, as the
// extension saw it. Incognito, popup and app windows are not in it.
type TabsReport struct {
	Windows []LiveWindow `json:"windows"`
	Groups  []LiveGroup  `json:"groups,omitempty"`
}

// LiveWindow is one window, its tabs in the order they show.
type LiveWindow struct {
	ID   int32     `json:"id"`
	Tabs []LiveTab `json:"tabs"`
}

// LiveTab is one tab. GroupID is -1 when the tab is in no group. Origin is
// set for a tab the extension opened: the address it was asked to open, when
// the page then moved to another one by itself (see merge.keyer).
type LiveTab struct {
	ID      int32  `json:"id"`
	URL     string `json:"url"`
	Title   string `json:"title,omitempty"`
	Pinned  bool   `json:"pinned,omitempty"`
	GroupID int32  `json:"groupId"`
	Active  bool   `json:"active,omitempty"`
	Origin  string `json:"origin,omitempty"`
}

// LiveGroup is one tab group. Color is the browser's name for it.
type LiveGroup struct {
	ID        int32  `json:"id"`
	Title     string `json:"title,omitempty"`
	Color     string `json:"color"`
	Collapsed bool   `json:"collapsed,omitempty"`
}

// TabsApply asks the extension to change its tabs.
type TabsApply struct {
	// Windows lists, per window, the tabs in the order wanted. ID 0 is a
	// window to open. A tab with an ID exists; one without is opened at URL.
	Windows []ApplyWindow `json:"windows"`
	// Close names the tabs to close. Every one was in the report the merge
	// used; the extension leaves alone any tab it does not find here.
	Close []int32 `json:"close,omitempty"`
	// Groups are the groups the tabs above name, by Key.
	Groups []ApplyGroup `json:"groups,omitempty"`
}

// ApplyWindow is one window's wanted tabs.
type ApplyWindow struct {
	ID   int32      `json:"id"`
	Tabs []ApplyTab `json:"tabs"`
}

// ApplyTab is one wanted tab. Group is an ApplyGroup.Key, or empty.
type ApplyTab struct {
	ID     int32  `json:"id,omitempty"`
	URL    string `json:"url,omitempty"`
	Pinned bool   `json:"pinned,omitempty"`
	Group  string `json:"group,omitempty"`
}

// ApplyGroup is one wanted group. ID is the browser's group when it exists
// already, else 0 and the extension makes it.
type ApplyGroup struct {
	Key       string `json:"key"`
	ID        int32  `json:"id,omitempty"`
	Title     string `json:"title"`
	Color     string `json:"color"`
	Collapsed bool   `json:"collapsed,omitempty"`
}

// groupColors are the browser's colour names, in the order the session
// files number them.
var groupColors = []string{"grey", "blue", "red", "yellow", "green", "pink", "purple", "cyan", "orange"}

func colorIndex(name string) uint32 {
	for i, c := range groupColors {
		if c == name {
			return uint32(i)
		}
	}
	return 0
}

func colorName(i uint32) string {
	if int(i) < len(groupColors) {
		return groupColors[i]
	}
	return groupColors[0]
}

// liveTokenHigh marks the group tokens the live reader makes up: the API
// gives a group a number, the session format a 128-bit token.
const liveTokenHigh = 0x4c49564547524f55 // "LIVEGROU"

// liveGUID is the lasting name a group gets for one browser connection.
func liveGUID(nonce string, id int32) string {
	return merge.LiveGroupPrefix + nonce + "-" + strconv.Itoa(int(id))
}

// session turns a report into the session the merge works on.
func (r *TabsReport) session(nonce string) *sessions.Session {
	s := &sessions.Session{Groups: map[[2]uint64]*sessions.Group{}}
	for _, g := range r.Groups {
		tok := [2]uint64{liveTokenHigh, uint64(uint32(g.ID))}
		s.Groups[tok] = &sessions.Group{High: tok[0], Low: tok[1], Title: g.Title, Color: colorIndex(g.Color),
			Collapsed: g.Collapsed, SavedGUID: liveGUID(nonce, g.ID)}
	}
	wins := append([]LiveWindow(nil), r.Windows...)
	// The session files keep windows in the order Helium made them, and
	// window ids count up the same way: both sides number them alike.
	sort.Slice(wins, func(i, j int) bool { return wins[i].ID < wins[j].ID })
	for _, lw := range wins {
		w := &sessions.Window{ID: lw.ID, ShowState: 1}
		for i, lt := range lw.Tabs {
			t := &sessions.Tab{ID: lt.ID, WindowID: lw.ID, Index: int32(i), Pinned: lt.Pinned,
				Navigations: []sessions.Navigation{{URL: lt.URL, Title: lt.Title, OriginalRequestURL: lt.Origin}}}
			if lt.GroupID >= 0 && !lt.Pinned {
				tok := [2]uint64{liveTokenHigh, uint64(uint32(lt.GroupID))}
				if s.Groups[tok] != nil {
					t.Group = &tok
				}
			}
			if lt.Active {
				w.SelectedTab = int32(i)
			}
			w.Tabs = append(w.Tabs, t)
		}
		s.Windows = append(s.Windows, w)
	}
	return s
}

// tabsShape is what a report looks like to a person, for telling a real
// change from the same report again: titles and the active tab are left
// out, as nothing syncs them.
func tabsShape(r *TabsReport) string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	for _, w := range r.Windows {
		fmt.Fprintf(&b, "w%d:", w.ID)
		for _, t := range w.Tabs {
			fmt.Fprintf(&b, "%d|%s|%s|%t|%d;", t.ID, t.URL, t.Origin, t.Pinned, t.GroupID)
		}
	}
	gs := append([]LiveGroup(nil), r.Groups...)
	sort.Slice(gs, func(i, j int) bool { return gs[i].ID < gs[j].ID })
	for _, g := range gs {
		fmt.Fprintf(&b, "g%d|%s|%s|%t;", g.ID, g.Title, g.Color, g.Collapsed)
	}
	return b.String()
}

// ReadTabs returns the open browser's tabs as a session. ok is false when
// no extension that reports tabs is connected for this profile; the caller
// then leaves tabs to the sync after Helium quits.
func (s *Server) ReadTabs(profileDir string) (*sessions.Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.conns[profileDir]
	if !ok || !c.seen || c.tabs == nil {
		return nil, false
	}
	// The changes WriteTabs sends are worked out against this report.
	c.tabsRead = c.tabs
	return c.tabs.session(c.nonce), true
}

// WriteTabs asks the connected browser to make its tabs match s, the
// merged session built from the last ReadTabs.
func (s *Server) WriteTabs(profileDir string, sess *sessions.Session) error {
	s.mu.Lock()
	c, ok := s.conns[profileDir]
	var base *TabsReport
	var nonce string
	if ok {
		base, nonce = c.tabsRead, c.nonce
	}
	s.mu.Unlock()
	if !ok || base == nil {
		return errors.New("live: no browser connected for this profile's tabs")
	}
	a, changed := planTabs(base, sess, nonce)
	if !changed {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return wsjson.Write(ctx, c.ws, Msg{Type: "applyTabs", Profile: profileDir, Apply: a})
}

// planTabs works out the changes that turn the browser's tabs (base) into
// the merged session. changed is false when there is nothing to do.
//
// A browser with no window open is left alone: opening one would be a
// window popping up out of nowhere. The tabs arrive when the person opens a
// window (merge.SyncTabs keeps them meanwhile).
func planTabs(base *TabsReport, sess *sessions.Session, nonce string) (*TabsApply, bool) {
	if len(base.Windows) == 0 {
		return nil, false
	}
	type where struct {
		win    int32
		pinned bool
		group  int32
	}
	have := map[int32]where{}
	order := map[int32][]int32{} // window -> tab ids, as reported
	for _, w := range base.Windows {
		for _, t := range w.Tabs {
			have[t.ID] = where{w.ID, t.Pinned, t.GroupID}
			order[w.ID] = append(order[w.ID], t.ID)
		}
	}
	groupsNow := map[int32]LiveGroup{}
	for _, g := range base.Groups {
		groupsNow[g.ID] = g
	}
	prefix := merge.LiveGroupPrefix + nonce + "-"
	a := &TabsApply{}
	changed := false
	keep := map[int32]bool{}
	groups := map[string]*ApplyGroup{}
	groupKey := func(tok [2]uint64) string {
		g := sess.Groups[tok]
		if g == nil {
			return ""
		}
		key := fmt.Sprintf("n%016x%016x", tok[0], tok[1])
		var id int32
		if rest, ok := strings.CutPrefix(g.SavedGUID, prefix); ok {
			if n, err := strconv.Atoi(rest); err == nil {
				if _, exists := groupsNow[int32(n)]; exists {
					id, key = int32(n), "g"+rest
				}
			}
		}
		if _, done := groups[key]; !done {
			ag := &ApplyGroup{Key: key, ID: id, Title: g.Title, Color: colorName(g.Color), Collapsed: g.Collapsed}
			groups[key] = ag
			if now, ok := groupsNow[id]; id == 0 || !ok || now.Title != ag.Title || now.Color != ag.Color || now.Collapsed != ag.Collapsed {
				changed = true
			}
		}
		return key
	}
	seenWin := map[int32]bool{}
	for _, w := range sess.Windows {
		if w.Type != 0 {
			continue
		}
		aw := ApplyWindow{}
		if _, ok := order[w.ID]; ok && !seenWin[w.ID] {
			aw.ID = w.ID
			seenWin[w.ID] = true
		}
		var ids []int32
		for _, t := range w.Tabs {
			cur := t.Current()
			if cur == nil {
				continue
			}
			at := ApplyTab{Pinned: t.Pinned}
			if h, ok := have[t.ID]; ok && t.ID != 0 && !keep[t.ID] {
				at.ID = t.ID
				keep[t.ID] = true
				ids = append(ids, t.ID)
				if h.win != aw.ID || h.pinned != t.Pinned {
					changed = true
				}
			} else {
				at.URL = cur.URL
				changed = true
			}
			if t.Group != nil && !t.Pinned {
				at.Group = groupKey(*t.Group)
			}
			if at.ID != 0 {
				want := int32(-1)
				if strings.HasPrefix(at.Group, "g") {
					n, _ := strconv.Atoi(at.Group[1:])
					want = int32(n)
				} else if at.Group != "" {
					want = -2 // a new group
				}
				if want != have[at.ID].group {
					changed = true
				}
			}
			aw.Tabs = append(aw.Tabs, at)
		}
		if len(aw.Tabs) == 0 {
			continue
		}
		if aw.ID == 0 {
			changed = true
		} else if !sameIDs(order[aw.ID], ids) {
			changed = true
		}
		a.Windows = append(a.Windows, aw)
	}
	for id := range have {
		if !keep[id] {
			a.Close = append(a.Close, id)
			changed = true
		}
	}
	sort.Slice(a.Close, func(i, j int) bool { return a.Close[i] < a.Close[j] })
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		a.Groups = append(a.Groups, *groups[k])
	}
	return a, changed
}

func sameIDs(a, b []int32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
