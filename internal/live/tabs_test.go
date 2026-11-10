package live

import (
	"reflect"
	"testing"

	"github.com/imrishabhchauhan/helium-syncer/internal/sessions"
)

func report(tabs ...LiveTab) *TabsReport {
	return &TabsReport{Windows: []LiveWindow{{ID: 1, Tabs: tabs}}}
}

func tab(id int32, url string) LiveTab { return LiveTab{ID: id, URL: url, GroupID: -1} }

// The report becomes a session and back: with nothing changed in between,
// there is nothing to send.
func TestPlanTabsNoChange(t *testing.T) {
	r := report(tab(10, "https://a.example/"), tab(11, "chrome://newtab/"), tab(12, "https://b.example/"))
	r.Windows[0].Tabs[1].Pinned = false
	s := r.session("n1")
	if _, changed := planTabs(r, s, "n1"); changed {
		t.Fatalf("the same tabs again should change nothing")
	}
}

// Closing and opening are explicit, and a tab the merge did not see (opened
// since the report) is never named.
func TestPlanTabsOpenAndClose(t *testing.T) {
	r := report(tab(10, "https://a.example/"), tab(11, "https://16personalities.com/"))
	s := r.session("n1")
	w := s.Windows[0]
	w.Tabs = []*sessions.Tab{w.Tabs[0], {Navigations: []sessions.Navigation{{URL: "https://vosm.com/"}}}}
	a, changed := planTabs(r, s, "n1")
	if !changed {
		t.Fatal("want a change")
	}
	if !reflect.DeepEqual(a.Close, []int32{11}) {
		t.Fatalf("close %v, want [11]", a.Close)
	}
	want := []ApplyTab{{ID: 10}, {URL: "https://vosm.com/"}}
	if len(a.Windows) != 1 || a.Windows[0].ID != 1 || !reflect.DeepEqual(a.Windows[0].Tabs, want) {
		t.Fatalf("windows %+v, want window 1 with %+v", a.Windows, want)
	}
}

// A browser with no window open is left alone.
func TestPlanTabsNoWindow(t *testing.T) {
	r := &TabsReport{}
	s := report(tab(10, "https://a.example/")).session("n1")
	if _, changed := planTabs(r, s, "n1"); changed {
		t.Fatal("a browser with no window must not get one")
	}
}

// Groups: the live group keeps its id through the merge; a new group is
// made; a renamed one is updated.
func TestPlanTabsGroups(t *testing.T) {
	r := report(tab(10, "https://a.example/"), tab(11, "https://b.example/"))
	r.Windows[0].Tabs[0].GroupID = 7
	r.Groups = []LiveGroup{{ID: 7, Title: "Work", Color: "blue"}}
	s := r.session("n1")
	if _, changed := planTabs(r, s, "n1"); changed {
		t.Fatal("same groups should change nothing")
	}
	for _, g := range s.Groups {
		g.Title, g.Color = "Work 2", 2
	}
	a, changed := planTabs(r, s, "n1")
	if !changed || len(a.Groups) != 1 || a.Groups[0].ID != 7 || a.Groups[0].Title != "Work 2" || a.Groups[0].Color != "red" {
		t.Fatalf("rename: changed=%v groups=%+v", changed, a.Groups)
	}
	if a.Windows[0].Tabs[0].Group != "g7" {
		t.Fatalf("tab 10 should stay in group 7: %+v", a.Windows[0].Tabs[0])
	}
	// A group from another connection (an older browser start) is new here.
	if a, _ := planTabs(r, s, "other"); a.Groups[0].ID != 0 {
		t.Fatalf("a group name from another connection must not be reused: %+v", a.Groups)
	}
}

// The session numbers windows in the order Helium made them, like the file.
func TestReportSessionOrder(t *testing.T) {
	r := &TabsReport{Windows: []LiveWindow{
		{ID: 9, Tabs: []LiveTab{tab(20, "https://late.example/")}},
		{ID: 3, Tabs: []LiveTab{{ID: 21, URL: "https://www.vosm.com/", Origin: "http://vosm.com/", GroupID: -1, Active: true}}},
	}}
	s := r.session("n1")
	if s.Windows[0].ID != 3 || s.Windows[1].ID != 9 {
		t.Fatalf("windows out of order: %d, %d", s.Windows[0].ID, s.Windows[1].ID)
	}
	nav := s.Windows[0].Tabs[0].Current()
	if nav.URL != "https://www.vosm.com/" || nav.OriginalRequestURL != "http://vosm.com/" {
		t.Fatalf("origin lost: %+v", nav)
	}
}
