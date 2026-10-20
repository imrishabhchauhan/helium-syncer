package merge

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"

	"github.com/imrishabhchauhan/helium-syncer/internal/crdt"
	"github.com/imrishabhchauhan/helium-syncer/internal/sessions"
)

type tabDevice struct {
	name         string
	sess         *sessions.Session
	ts           *TabSync
	tabs, groups *crdt.Map
	clock        *crdt.Clock
	seen         int
	r            *rand.Rand
}

func newTabDevice(name string, r *rand.Rand) *tabDevice {
	return &tabDevice{name: name, sess: &sessions.Session{Groups: map[[2]uint64]*sessions.Group{}}, ts: NewTabSync(),
		tabs: crdt.NewMap(), groups: crdt.NewMap(), clock: crdt.NewClock(name, nil), r: r}
}

// heliumRestore imitates what Helium 0.19.4.1 does with a session we wrote
// (verified on real Helium): binary round trip, new group tokens on restore,
// a fresh saved guid for groups that had none, pinned tabs first.
func heliumRestore(r *rand.Rand, s *sessions.Session) *sessions.Session {
	cmds, ok, err := sessions.ParseCommands(sessions.Encode(s))
	if err != nil || !ok {
		panic(fmt.Sprint("encoded session invalid: ", err))
	}
	out := sessions.Build(cmds)
	retoken := map[[2]uint64][2]uint64{}
	groups := map[[2]uint64]*sessions.Group{}
	for old, g := range out.Groups {
		nt := [2]uint64{r.Uint64() | 1, r.Uint64()}
		retoken[old] = nt
		c := *g
		c.High, c.Low = nt[0], nt[1]
		if c.SavedGUID == "" {
			c.SavedGUID = fmt.Sprintf("saved-%x", r.Uint64())
		}
		groups[nt] = &c
	}
	out.Groups = groups
	for _, w := range out.Windows {
		for _, t := range w.Tabs {
			if t.Group != nil {
				nt := retoken[*t.Group]
				t.Group = &nt
			}
		}
		sort.SliceStable(w.Tabs, func(i, j int) bool { return w.Tabs[i].Pinned && !w.Tabs[j].Pinned })
	}
	return out
}

func (d *tabDevice) sync(s *server) int {
	incoming := append([]crdt.Op(nil), s.log[d.seen:]...)
	projected, out := SyncTabs(d.sess, d.ts, d.tabs, d.groups, d.clock, incoming, func(u string) string { return "title of " + u })
	s.log = append(s.log, out...)
	d.seen = len(s.log)
	d.sess = heliumRestore(d.r, projected)
	return len(out)
}

var tabPages = []string{"https://mail.example/", "https://yt.example/watch?v=1", "https://wiki.example/हिंदी",
	"https://news.example/", "https://docs.example/a", "https://docs.example/b", "chrome://settings/", "chrome://newtab/"}

func nav(url string) []sessions.Navigation {
	return []sessions.Navigation{{URL: url, Transition: 0x30000000}}
}

// edit makes one change like a user in Helium would.
func (d *tabDevice) edit(seq int) {
	r, s := d.r, d.sess
	if len(normalWindows(s)) == 0 {
		s.Windows = append(s.Windows, &sessions.Window{ShowState: 1})
	}
	wins := normalWindows(s)
	w := wins[r.Intn(len(wins))]
	firstUnpinned := func(w *sessions.Window) int {
		i := 0
		for i < len(w.Tabs) && w.Tabs[i].Pinned {
			i++
		}
		return i
	}
	insert := func(w *sessions.Window, t *sessions.Tab) {
		lo := firstUnpinned(w)
		if t.Pinned {
			lo = 0
		}
		hi := len(w.Tabs)
		if t.Pinned {
			hi = firstUnpinned(w)
		}
		i := lo + r.Intn(hi-lo+1)
		w.Tabs = append(w.Tabs[:i], append([]*sessions.Tab{t}, w.Tabs[i:]...)...)
	}
	remove := func(w *sessions.Window, i int) *sessions.Tab {
		t := w.Tabs[i]
		w.Tabs = append(w.Tabs[:i:i], w.Tabs[i+1:]...)
		return t
	}
	switch op := r.Intn(12); {
	case op < 4 || len(w.Tabs) == 0: // open a tab
		insert(w, &sessions.Tab{Navigations: nav(tabPages[r.Intn(len(tabPages))])})
	case op < 6: // close a tab
		remove(w, r.Intn(len(w.Tabs)))
	case op < 7: // navigate a tab to another page
		t := w.Tabs[r.Intn(len(w.Tabs))]
		t.Navigations = nav(tabPages[r.Intn(len(tabPages))])
	case op < 8: // move a tab, maybe to another window
		t := remove(w, r.Intn(len(w.Tabs)))
		insert(wins[r.Intn(len(wins))], t)
	case op < 9: // pin / unpin
		i := r.Intn(len(w.Tabs))
		t := remove(w, i)
		t.Pinned = !t.Pinned
		t.Group = nil // pinned tabs are never grouped
		insert(w, t)
	case op < 10: // group a tab (new group or an existing one)
		i := firstUnpinned(w)
		if i >= len(w.Tabs) {
			return
		}
		t := w.Tabs[i+r.Intn(len(w.Tabs)-i)]
		for tok := range s.Groups {
			if r.Intn(2) == 0 {
				t.Group = &tok
				return
			}
		}
		tok := [2]uint64{r.Uint64() | 1, r.Uint64()}
		s.Groups[tok] = &sessions.Group{High: tok[0], Low: tok[1], Title: fmt.Sprintf("Group %d", seq), Color: uint32(r.Intn(9)),
			SavedGUID: fmt.Sprintf("user-%s-%d", d.name, seq)}
		t.Group = &tok
	case op < 11: // rename / recolour a group, or ungroup a tab
		for _, g := range s.Groups {
			g.Title = fmt.Sprintf("Renamed %d", seq)
			g.Color = uint32(r.Intn(9))
			return
		}
	default: // new window, or close a window
		if r.Intn(2) == 0 || len(wins) < 2 {
			s.Windows = append(s.Windows, &sessions.Window{ShowState: 1, Tabs: []*sessions.Tab{{Navigations: nav(tabPages[r.Intn(len(tabPages))])}}})
		} else {
			for i, sw := range s.Windows {
				if sw == w {
					s.Windows = append(s.Windows[:i:i], s.Windows[i+1:]...)
					break
				}
			}
		}
	}
	// Drop groups no tab uses, like Helium.
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
}

// view is what the user sees of the synced tabs, independent of local ids.
func view(s *sessions.Session) string {
	var b strings.Builder
	n := 0
	for _, w := range normalWindows(s) {
		var line []string
		for _, t := range w.Tabs {
			cur := t.Current()
			if cur == nil || !Syncable(cur.URL) {
				continue
			}
			g := ""
			if t.Group != nil {
				if meta := s.Groups[*t.Group]; meta != nil {
					g = fmt.Sprintf("[%s/%d]", meta.Title, meta.Color)
				}
			}
			p := ""
			if t.Pinned {
				p = "📌"
			}
			line = append(line, p+cur.URL+g)
		}
		if len(line) > 0 {
			fmt.Fprintf(&b, "window %d: %s\n", n, strings.Join(line, " | "))
			n++
		}
	}
	return b.String()
}

func TestTabsConvergeRandomized(t *testing.T) {
	for _, n := range []int{2, 3} {
		for round := 0; round < rounds(400, 3000); round++ {
			r := rand.New(rand.NewSource(int64(round*10 + n)))
			s := &server{}
			devs := []*tabDevice{newTabDevice("mac", r), newTabDevice("win", r), newTabDevice("laptop", r)}[:n]
			for step := 0; step < 30; step++ {
				d := devs[r.Intn(n)]
				for i := r.Intn(3); i >= 0; i-- {
					d.edit(step*10 + i)
				}
				if r.Intn(3) == 0 {
					nothingPending := d.seen == len(s.log)
					before := view(d.sess)
					d.sync(s)
					if nothingPending && view(d.sess) != before {
						t.Fatalf("n=%d round %d: sync changed %s's own tabs\nbefore:\n%s\nafter:\n%s", n, round, d.name, before, view(d.sess))
					}
				}
			}
			for i := 0; i < 3; i++ {
				for _, d := range devs {
					d.sync(s)
				}
			}
			first := view(devs[0].sess)
			if round == 7 {
				t.Logf("sample converged state (n=%d):\n%s", n, first)
			}
			for _, d := range devs[1:] {
				if v := view(d.sess); v != first {
					t.Fatalf("n=%d round %d: devices diverged\n%s:\n%s\n%s:\n%s", n, round, devs[0].name, first, d.name, v)
				}
			}
			ops := 0
			for _, d := range devs {
				ops += d.sync(s)
			}
			if ops != 0 {
				t.Fatalf("n=%d round %d: %d ops after convergence (ping-pong)\n%s", n, round, ops, first)
			}
		}
	}
}

// The owner's scenario, step by step.
func TestTabsOwnerScenario(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	s := &server{}
	mac, win := newTabDevice("mac", r), newTabDevice("win", r)
	mac.sess.Windows = []*sessions.Window{{ShowState: 1, Tabs: []*sessions.Tab{{Navigations: nav("https://mail.google.com/")}}}}
	mac.sync(s) // Mac quits with Gmail open
	win.sync(s) // Windows opens: gets Gmail
	if v := view(win.sess); !strings.Contains(v, "mail.google.com") {
		t.Fatalf("Windows should get Gmail:\n%s", v)
	}
	// On Windows, open YouTube, quit.
	w := normalWindows(win.sess)[0]
	w.Tabs = append(w.Tabs, &sessions.Tab{Navigations: nav("https://www.youtube.com/")})
	win.sync(s)
	// Mac opens: Gmail + YouTube.
	mac.sync(s)
	if v := view(mac.sess); !strings.Contains(v, "mail.google.com") || !strings.Contains(v, "youtube.com") {
		t.Fatalf("Mac should have Gmail + YouTube:\n%s", v)
	}
	// On Mac: close YouTube, open two Wikipedia articles, quit.
	mw := normalWindows(mac.sess)[0]
	var kept []*sessions.Tab
	for _, tb := range mw.Tabs {
		if !strings.Contains(tb.Current().URL, "youtube") {
			kept = append(kept, tb)
		}
	}
	mw.Tabs = append(kept, &sessions.Tab{Navigations: nav("https://en.wikipedia.org/wiki/Helium")},
		&sessions.Tab{Navigations: nav("https://en.wikipedia.org/wiki/Chromium")})
	mac.sync(s)
	macView := view(mac.sess)
	// Mac reopens: nothing changes.
	mac.sync(s)
	if view(mac.sess) != macView {
		t.Fatalf("reopening the Mac changed its tabs")
	}
	// Windows reopens: YouTube closed, both Wikipedia tabs there.
	win.sync(s)
	v := view(win.sess)
	if strings.Contains(v, "youtube") || !strings.Contains(v, "wiki/Helium") || !strings.Contains(v, "wiki/Chromium") || !strings.Contains(v, "mail.google.com") {
		t.Fatalf("Windows should show Gmail + 2 Wikipedia, no YouTube:\n%s", v)
	}
	if v != macView {
		t.Fatalf("Windows and Mac differ\nmac:\n%s\nwin:\n%s", macView, v)
	}
}

// Both devices open at once: tabs are combined, never duplicated.
func TestTabsCombineWithoutDuplicates(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	s := &server{}
	mac, win := newTabDevice("mac", r), newTabDevice("win", r)
	mac.sess.Windows = []*sessions.Window{{ShowState: 1, Tabs: []*sessions.Tab{
		{Navigations: nav("https://mail.google.com/")}, {Navigations: nav("https://en.wikipedia.org/")}}}}
	win.sess.Windows = []*sessions.Window{{ShowState: 1, Tabs: []*sessions.Tab{
		{Navigations: nav("https://www.youtube.com/")}, {Navigations: nav("https://mail.google.com/")}}}}
	win.sync(s)
	mac.sync(s)
	win.sync(s)
	v := view(mac.sess)
	if strings.Count(v, "mail.google.com") != 1 || !strings.Contains(v, "youtube") || !strings.Contains(v, "wikipedia") {
		t.Fatalf("want Gmail once + YouTube + Wikipedia:\n%s", v)
	}
	if view(win.sess) != v {
		t.Fatalf("devices differ\n%s\n%s", v, view(win.sess))
	}
}
