// Package sessions reads and writes Helium's <profile>/Sessions/Session_*
// files (SNSS, components/sessions/core, Chromium 155): open windows, tabs,
// pinned state, tab groups and each tab's navigation history.
//
// File layout: int32 "SNSS" signature, int32 version (3 = clear text), then
// commands of [uint16 size][uint8 id][size-1 bytes]. A file is only used for
// restore if it contains the initial-state marker command (id 255).
package sessions

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/imrishabhchauhan/helium-syncer/internal/pickle"
)

const (
	signature     = 0x53534E53
	versionMarker = 3
	versionCrypt  = 5
	markerID      = 255
)

// Command ids (session_service_commands.cc).
const (
	cmdSetTabWindow          = 0
	cmdSetTabIndexInWindow   = 2
	cmdPrunedFromBack        = 5
	cmdUpdateTabNavigation   = 6
	cmdSetSelectedNavIndex   = 7
	cmdSetSelectedTabInIndex = 8
	cmdSetWindowType         = 9
	cmdPrunedFromFront       = 11
	cmdSetPinnedState        = 12
	cmdSetWindowBounds3      = 14
	cmdTabClosed             = 16
	cmdWindowClosed          = 17
	cmdSetActiveWindow       = 20
	cmdLastActiveTime        = 21
	cmdNavigationPathPruned  = 24
	cmdSetTabGroup           = 25
	cmdSetTabGroupMetadata2  = 27
	cmdSetTabGuid            = 28
)

// ErrEncrypted means the file uses OS-key encryption (version 5).
var ErrEncrypted = errors.New("sessions: file is encrypted (version 5)")

// Command is one raw command.
type Command struct {
	ID   uint8
	Data []byte
}

// Navigation is one back/forward entry of a tab.
type Navigation struct {
	Index                            int32
	URL                              string
	Title                            string
	PageState                        string // opaque, cross-platform
	Transition                       int32
	TypeMask                         int32
	Referrer                         string
	OriginalRequestURL               string
	OverridingUA                     bool
	Timestamp                        int64 // base::Time internal value (µs since 1601)
	HTTPStatus                       int32
	ReferrerPolicy                   int32
	Extended                         [][2]string
	TaskID, ParentTaskID, RootTaskID int64
}

// Group is a tab group's look.
type Group struct {
	High, Low uint64 // TabGroupId token
	Title     string
	Color     uint32 // 0 grey,1 blue,2 red,3 yellow,4 green,5 pink,6 purple,7 cyan,8 orange
	Collapsed bool
	SavedGUID string // set when the group is a saved tab group
}

// Key identifies a group token.
func (g Group) Key() [2]uint64 { return [2]uint64{g.High, g.Low} }

// Tab is one open tab.
type Tab struct {
	ID          int32
	WindowID    int32
	Index       int32 // visual index in its window
	Pinned      bool
	Group       *[2]uint64
	Navigations []Navigation
	SelectedNav int32
	LastActive  int64
	GUID        string
}

// Current returns the navigation the tab is showing.
func (t *Tab) Current() *Navigation {
	for i := range t.Navigations {
		if t.Navigations[i].Index == t.SelectedNav {
			return &t.Navigations[i]
		}
	}
	if len(t.Navigations) > 0 {
		return &t.Navigations[len(t.Navigations)-1]
	}
	return nil
}

// Window is one browser window.
type Window struct {
	ID          int32
	Type        int32 // 0 normal, 1 popup, 2 app, 3 devtools...
	X, Y, W, H  int32
	ShowState   int32
	SelectedTab int32 // visual index
	Tabs        []*Tab
}

// Session is the restorable state of a profile.
type Session struct {
	Windows      []*Window
	Groups       map[[2]uint64]*Group
	ActiveWindow int32
}

// ReadFile parses one session file.
func ReadFile(path string) (*Session, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cmds, hasMarker, err := ParseCommands(data)
	if err != nil {
		return nil, err
	}
	if !hasMarker {
		return nil, fmt.Errorf("sessions: %s has no initial-state marker", filepath.Base(path))
	}
	return Build(cmds), nil
}

// ParseCommands splits an SNSS file into commands.
func ParseCommands(data []byte) (cmds []Command, hasMarker bool, err error) {
	if len(data) < 8 || binary.LittleEndian.Uint32(data) != signature {
		return nil, false, errors.New("sessions: not an SNSS file")
	}
	switch v := binary.LittleEndian.Uint32(data[4:]); v {
	case versionMarker:
	case versionCrypt:
		return nil, false, ErrEncrypted
	default:
		return nil, false, fmt.Errorf("sessions: unsupported version %d", v)
	}
	p := data[8:]
	for len(p) >= 2 {
		size := int(binary.LittleEndian.Uint16(p))
		if size < 1 || 2+size > len(p) {
			break // truncated last write: Chromium also stops here
		}
		c := Command{ID: p[2], Data: p[3 : 2+size]}
		p = p[2+size:]
		if c.ID == markerID {
			hasMarker = true
			continue
		}
		cmds = append(cmds, c)
	}
	return cmds, hasMarker, nil
}

func i32(b []byte, off int) int32 { return int32(binary.LittleEndian.Uint32(b[off:])) }
func i64(b []byte, off int) int64 { return int64(binary.LittleEndian.Uint64(b[off:])) }

// Build replays commands the way SessionService restores them.
func Build(cmds []Command) *Session {
	tabs := map[int32]*Tab{}
	wins := map[int32]*Window{}
	s := &Session{Groups: map[[2]uint64]*Group{}}
	tab := func(id int32) *Tab {
		if t := tabs[id]; t != nil {
			return t
		}
		t := &Tab{ID: id}
		tabs[id] = t
		return t
	}
	win := func(id int32) *Window {
		if w := wins[id]; w != nil {
			return w
		}
		w := &Window{ID: id, ShowState: 1}
		wins[id] = w
		return w
	}
	for _, c := range cmds {
		d := c.Data
		switch c.ID {
		case cmdSetTabWindow:
			if len(d) >= 8 {
				win(i32(d, 0))
				tab(i32(d, 4)).WindowID = i32(d, 0)
			}
		case cmdSetTabIndexInWindow:
			if len(d) >= 8 {
				tab(i32(d, 0)).Index = i32(d, 4)
			}
		case cmdSetSelectedNavIndex:
			if len(d) >= 8 {
				tab(i32(d, 0)).SelectedNav = i32(d, 4)
			}
		case cmdSetSelectedTabInIndex:
			if len(d) >= 8 {
				win(i32(d, 0)).SelectedTab = i32(d, 4)
			}
		case cmdSetWindowType:
			if len(d) >= 8 {
				win(i32(d, 0)).Type = i32(d, 4)
			}
		case cmdSetWindowBounds3:
			if len(d) >= 24 {
				w := win(i32(d, 0))
				w.X, w.Y, w.W, w.H, w.ShowState = i32(d, 4), i32(d, 8), i32(d, 12), i32(d, 16), i32(d, 20)
			}
		case cmdSetPinnedState:
			if len(d) >= 5 {
				tab(i32(d, 0)).Pinned = d[4] != 0
			}
		case cmdSetTabGroup:
			if len(d) >= 25 {
				t := tab(i32(d, 0))
				if d[24] != 0 {
					k := [2]uint64{uint64(i64(d, 8)), uint64(i64(d, 16))}
					t.Group = &k
				} else {
					t.Group = nil
				}
			}
		case cmdLastActiveTime:
			if len(d) >= 16 {
				tab(i32(d, 0)).LastActive = i64(d, 8)
			}
		case cmdTabClosed:
			if len(d) >= 4 {
				delete(tabs, i32(d, 0))
			}
		case cmdWindowClosed:
			if len(d) >= 4 {
				delete(wins, i32(d, 0))
			}
		case cmdSetActiveWindow:
			if len(d) >= 4 {
				s.ActiveWindow = i32(d, 0)
			}
		case cmdPrunedFromBack: // keep the first `index` entries
			if len(d) >= 8 {
				t := tab(i32(d, 0))
				keep := i32(d, 4)
				t.Navigations = filterNav(t.Navigations, func(n Navigation) bool { return n.Index < keep })
			}
		case cmdPrunedFromFront: // drop the first `count`, shift indices
			if len(d) >= 8 {
				t := tab(i32(d, 0))
				count := i32(d, 4)
				t.Navigations = filterNav(t.Navigations, func(n Navigation) bool { return n.Index >= count })
				for i := range t.Navigations {
					t.Navigations[i].Index -= count
				}
				t.SelectedNav = max(0, t.SelectedNav-count)
			}
		case cmdNavigationPathPruned: // remove [index, index+count), shift later ones
			if len(d) >= 12 {
				t := tab(i32(d, 0))
				idx, count := i32(d, 4), i32(d, 8)
				t.Navigations = filterNav(t.Navigations, func(n Navigation) bool { return n.Index < idx || n.Index >= idx+count })
				for i := range t.Navigations {
					if t.Navigations[i].Index >= idx+count {
						t.Navigations[i].Index -= count
					}
				}
			}
		case cmdUpdateTabNavigation:
			if id, nav, err := readNavigation(d); err == nil {
				t := tab(id)
				replaced := false
				for i := range t.Navigations {
					if t.Navigations[i].Index == nav.Index {
						t.Navigations[i] = nav
						replaced = true
					}
				}
				if !replaced {
					t.Navigations = append(t.Navigations, nav)
					sort.Slice(t.Navigations, func(a, b int) bool { return t.Navigations[a].Index < t.Navigations[b].Index })
				}
			}
		case cmdSetTabGroupMetadata2:
			if g, err := readGroupMetadata(d); err == nil {
				s.Groups[g.Key()] = g
			}
		case cmdSetTabGuid:
			if r, err := pickle.NewReader(d); err == nil {
				id, _ := r.Int()
				guid, err := r.String()
				if err == nil {
					tab(id).GUID = guid
				}
			}
		}
	}
	for _, t := range tabs {
		if w := wins[t.WindowID]; w != nil && len(t.Navigations) > 0 {
			w.Tabs = append(w.Tabs, t)
		}
	}
	ids := make([]int32, 0, len(wins))
	for id, w := range wins {
		if len(w.Tabs) > 0 {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
	for _, id := range ids {
		w := wins[id]
		sort.SliceStable(w.Tabs, func(a, b int) bool { return w.Tabs[a].Index < w.Tabs[b].Index })
		s.Windows = append(s.Windows, w)
	}
	// Drop metadata for groups no tab uses.
	used := map[[2]uint64]bool{}
	for _, w := range s.Windows {
		for _, t := range w.Tabs {
			if t.Group != nil {
				used[*t.Group] = true
			}
		}
	}
	for k := range s.Groups {
		if !used[k] {
			delete(s.Groups, k)
		}
	}
	return s
}

func filterNav(ns []Navigation, keep func(Navigation) bool) []Navigation {
	out := ns[:0]
	for _, n := range ns {
		if keep(n) {
			out = append(out, n)
		}
	}
	return out
}

func readNavigation(d []byte) (int32, Navigation, error) {
	var n Navigation
	r, err := pickle.NewReader(d)
	if err != nil {
		return 0, n, err
	}
	tabID, err := r.Int()
	if err != nil {
		return 0, n, err
	}
	// Required fields.
	if n.Index, err = r.Int(); err != nil {
		return 0, n, err
	}
	if n.URL, err = r.String(); err != nil {
		return 0, n, err
	}
	if n.Title, err = r.String16(); err != nil {
		return 0, n, err
	}
	if n.PageState, err = r.String(); err != nil {
		return 0, n, err
	}
	if n.Transition, err = r.Int(); err != nil {
		return 0, n, err
	}
	// Optional fields, added over the years; stop quietly when absent.
	opt := func(f func() error) bool { return r.Remaining() > 0 && f() == nil }
	_ = opt(func() (e error) { n.TypeMask, e = r.Int(); return }) &&
		opt(func() (e error) { n.Referrer, e = r.String(); return }) &&
		opt(func() (e error) { _, e = r.Int(); return }) && // obsolete referrer policy
		opt(func() (e error) { n.OriginalRequestURL, e = r.String(); return }) &&
		opt(func() (e error) { n.OverridingUA, e = r.Bool(); return }) &&
		opt(func() (e error) { n.Timestamp, e = r.Int64(); return }) &&
		opt(func() (e error) { _, e = r.String16(); return }) && // obsolete search terms
		opt(func() (e error) { n.HTTPStatus, e = r.Int(); return }) &&
		opt(func() (e error) { n.ReferrerPolicy, e = r.Int(); return }) &&
		opt(func() error {
			cnt, e := r.Int()
			if e != nil {
				return e
			}
			for i := int32(0); i < cnt; i++ {
				k, e1 := r.String()
				v, e2 := r.String()
				if e1 != nil || e2 != nil {
					return pickle.ErrShort
				}
				n.Extended = append(n.Extended, [2]string{k, v})
			}
			return nil
		}) &&
		opt(func() (e error) { n.TaskID, e = r.Int64(); return }) &&
		opt(func() (e error) { n.ParentTaskID, e = r.Int64(); return }) &&
		opt(func() (e error) { n.RootTaskID, e = r.Int64(); return })
	return tabID, n, nil
}

func readGroupMetadata(d []byte) (*Group, error) {
	r, err := pickle.NewReader(d)
	if err != nil {
		return nil, err
	}
	g := &Group{}
	if g.High, err = r.Uint64(); err != nil {
		return nil, err
	}
	if g.Low, err = r.Uint64(); err != nil {
		return nil, err
	}
	if g.Title, err = r.String16(); err != nil {
		return nil, err
	}
	if g.Color, err = r.Uint32(); err != nil {
		return nil, err
	}
	g.Collapsed, _ = r.Bool()
	if saved, err := r.Bool(); err == nil && saved {
		g.SavedGUID, _ = r.String()
	}
	return g, nil
}

// --- writing ---

func cmd(id uint8, data []byte) []byte {
	out := binary.LittleEndian.AppendUint16(nil, uint16(1+len(data)))
	out = append(out, id)
	return append(out, data...)
}

func ints(vs ...int32) []byte {
	var b []byte
	for _, v := range vs {
		b = binary.LittleEndian.AppendUint32(b, uint32(v))
	}
	return b
}

func navCommand(tabID int32, n Navigation) []byte {
	var w pickle.Writer
	w.Int(tabID)
	w.Int(n.Index)
	w.String(n.URL)
	w.String16(n.Title)
	w.String(n.PageState)
	w.Int(n.Transition)
	w.Int(n.TypeMask)
	w.String(n.Referrer)
	w.Int(0) // obsolete referrer policy
	w.String(n.OriginalRequestURL)
	w.Bool(n.OverridingUA)
	w.Int64(n.Timestamp)
	w.String16("") // obsolete search terms
	w.Int(n.HTTPStatus)
	w.Int(n.ReferrerPolicy)
	w.Int(int32(len(n.Extended)))
	for _, kv := range n.Extended {
		w.String(kv[0])
		w.String(kv[1])
	}
	w.Int64(n.TaskID)
	w.Int64(n.ParentTaskID)
	w.Int64(n.RootTaskID)
	w.Int(0)
	return cmd(cmdUpdateTabNavigation, w.Bytes())
}

// Encode writes a complete, restorable session file (version 3 + marker).
// Window and tab ids are reassigned 1..n so the file is self-consistent.
func Encode(s *Session) []byte {
	out := binary.LittleEndian.AppendUint32(nil, signature)
	out = binary.LittleEndian.AppendUint32(out, versionMarker)
	next := int32(1)
	activeWin := int32(0)
	for wi, w := range s.Windows {
		winID := next
		next++
		if w.ID == s.ActiveWindow || (activeWin == 0 && wi == 0) {
			activeWin = winID
		}
		x, y, wd, h, show := w.X, w.Y, w.W, w.H, w.ShowState
		if wd <= 0 || h <= 0 {
			x, y, wd, h = 80, 60, 1280, 800
		}
		if show == 0 {
			show = 1
		}
		out = append(out, cmd(cmdSetWindowBounds3, ints(winID, x, y, wd, h, show))...)
		out = append(out, cmd(cmdSetWindowType, ints(winID, w.Type))...)
		for ti, t := range w.Tabs {
			tabID := next
			next++
			out = append(out, cmd(cmdSetTabWindow, ints(winID, tabID))...)
			if t.LastActive != 0 {
				b := ints(tabID, 0)
				b = binary.LittleEndian.AppendUint64(b, uint64(t.LastActive))
				out = append(out, cmd(cmdLastActiveTime, b)...)
			}
			if t.GUID != "" {
				var p pickle.Writer
				p.Int(tabID)
				p.String(t.GUID)
				out = append(out, cmd(cmdSetTabGuid, p.Bytes())...)
			}
			if t.Pinned {
				out = append(out, cmd(cmdSetPinnedState, []byte{byte(tabID), byte(tabID >> 8), byte(tabID >> 16), byte(tabID >> 24), 1, 0, 0, 0})...)
			}
			if t.Group != nil {
				b := ints(tabID, 0)
				b = binary.LittleEndian.AppendUint64(b, t.Group[0])
				b = binary.LittleEndian.AppendUint64(b, t.Group[1])
				b = append(b, 1, 0, 0, 0, 0, 0, 0, 0)
				out = append(out, cmd(cmdSetTabGroup, b)...)
			}
			for _, n := range t.Navigations {
				out = append(out, navCommand(tabID, n)...)
			}
			out = append(out, cmd(cmdSetSelectedNavIndex, ints(tabID, t.SelectedNav))...)
			out = append(out, cmd(cmdSetTabIndexInWindow, ints(tabID, int32(ti)))...)
		}
		sel := w.SelectedTab
		if sel < 0 || int(sel) >= len(w.Tabs) {
			sel = 0
		}
		out = append(out, cmd(cmdSetSelectedTabInIndex, ints(winID, sel))...)
	}
	keys := make([][2]uint64, 0, len(s.Groups))
	for k := range s.Groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, b int) bool {
		return keys[a][0] < keys[b][0] || (keys[a][0] == keys[b][0] && keys[a][1] < keys[b][1])
	})
	for _, k := range keys {
		g := s.Groups[k]
		var p pickle.Writer
		p.Uint64(g.High)
		p.Uint64(g.Low)
		p.String16(g.Title)
		p.Uint32(g.Color)
		p.Bool(g.Collapsed)
		p.Bool(g.SavedGUID != "")
		if g.SavedGUID != "" {
			p.String(g.SavedGUID)
		}
		out = append(out, cmd(cmdSetTabGroupMetadata2, p.Bytes())...)
	}
	if activeWin != 0 {
		out = append(out, cmd(cmdSetActiveWindow, ints(activeWin))...)
	}
	return append(out, cmd(markerID, nil)...)
}

// --- files on disk ---

// LatestFile returns the newest Session_* file that has the marker, which is
// the one Helium restores from.
func LatestFile(profile string) (string, error) {
	dir := filepath.Join(profile, "Sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	type cand struct {
		ts   int64
		path string
	}
	var cs []cand
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "Session_") {
			continue
		}
		ts, err := strconv.ParseInt(strings.TrimPrefix(name, "Session_"), 10, 64)
		if err != nil {
			continue
		}
		cs = append(cs, cand{ts, filepath.Join(dir, name)})
	}
	sort.Slice(cs, func(a, b int) bool { return cs[a].ts > cs[b].ts })
	for _, c := range cs {
		data, err := os.ReadFile(c.path)
		if err != nil {
			continue
		}
		if _, ok, err := ParseCommands(data); err == nil && ok {
			return c.path, nil
		}
	}
	return "", errors.New("sessions: no restorable session file")
}

// WriteNew writes s as a new Session_<now> file that Helium will restore on
// its next start. Helium must not be running on this profile.
func WriteNew(profile string, s *Session) (string, error) {
	dir := filepath.Join(profile, "Sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	ts := time.Now().UnixMicro() + 11644473600*1_000_000
	if latest, err := LatestFile(profile); err == nil {
		if old, err := strconv.ParseInt(strings.TrimPrefix(filepath.Base(latest), "Session_"), 10, 64); err == nil && old >= ts {
			ts = old + 1
		}
	}
	path := filepath.Join(dir, "Session_"+strconv.FormatInt(ts, 10))
	tmp := path + ".hs-tmp"
	if err := os.WriteFile(tmp, Encode(s), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return path, nil
}
