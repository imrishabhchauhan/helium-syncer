package merge

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/imrishabhchauhan/helium-syncer/internal/sessions"
)

func window(urls ...string) *sessions.Window {
	w := &sessions.Window{ShowState: 1}
	for _, u := range urls {
		w.Tabs = append(w.Tabs, &sessions.Tab{Navigations: nav(u)})
	}
	return w
}

// twoInSync returns a Mac and a Windows device that both show these pages.
func twoInSync(t *testing.T, urls ...string) (*server, *tabDevice, *tabDevice) {
	t.Helper()
	r := rand.New(rand.NewSource(7))
	s := &server{}
	mac, win := newTabDevice("mac", r), newTabDevice("win", r)
	win.sess.Windows = []*sessions.Window{window(urls...)}
	win.sync(s)
	mac.sync(s)
	win.sync(s)
	if view(mac.sess) != view(win.sess) || strings.Count(view(mac.sess), "http") != len(urls) {
		t.Fatalf("setup: devices differ\nmac:\n%s\nwin:\n%s", view(mac.sess), view(win.sess))
	}
	return s, mac, win
}

var fivePages = []string{"https://claude.ai/a", "https://vosm.com/", "https://totalapp.com/",
	"https://centralsquarefoundation.org/", "https://www.16personalities.com/"}

// Helium on a Mac quit with no window open (or still runs with none): the
// other computer must keep every tab, and the Mac gets them back.
func TestTabsNoWindowNeverClosesElsewhere(t *testing.T) {
	for _, tc := range []struct {
		name  string
		local []*sessions.Window
	}{
		{"no window", nil},
		{"only a new tab page", []*sessions.Window{window("chrome://newtab/")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mac, win := twoInSync(t, fivePages...)
			want := view(win.sess)
			mac.sess.Windows = tc.local
			if n := mac.sync(s); n != 0 {
				t.Fatalf("the Mac sent %d changes; it must send none", n)
			}
			win.sync(s)
			if v := view(win.sess); v != want {
				t.Fatalf("Windows lost tabs:\n%s\nwant:\n%s", v, want)
			}
			if v := view(mac.sess); v != want {
				t.Fatalf("the Mac should get its tabs back:\n%s\nwant:\n%s", v, want)
			}
		})
	}
}

// After a spell with no tab, the Mac opens a new window and one page in it,
// before the synced tabs came back: that page is added, nothing is closed.
func TestTabsFirstPageAfterNoWindowAddsOnly(t *testing.T) {
	s, mac, win := twoInSync(t, fivePages...)
	mac.sess.Windows = nil
	mac.sync(s)
	mac.sess.Windows = []*sessions.Window{window("https://ei.study/")}
	mac.sync(s)
	win.sync(s)
	v := view(win.sess)
	for _, u := range append(fivePages, "https://ei.study/") {
		if !strings.Contains(v, u) {
			t.Fatalf("Windows is missing %s:\n%s", u, v)
		}
	}
	if view(mac.sess) != v {
		t.Fatalf("devices differ\nmac:\n%s\nwin:\n%s", view(mac.sess), v)
	}
	// From then on, closing a tab is closing a tab again.
	mw := normalWindows(mac.sess)[0]
	for i, tb := range mw.Tabs {
		if strings.Contains(tb.Current().URL, "16personalities") {
			mw.Tabs = append(mw.Tabs[:i:i], mw.Tabs[i+1:]...)
			break
		}
	}
	mac.sync(s)
	win.sync(s)
	if strings.Contains(view(win.sess), "16personalities") {
		t.Fatalf("a tab closed on the Mac after the empty spell stayed open on Windows:\n%s", view(win.sess))
	}
}

// The owner's case: a tab open on both, closed on Windows, closes on the Mac.
func TestTabsCloseTravels(t *testing.T) {
	s, mac, win := twoInSync(t, fivePages...)
	w := normalWindows(win.sess)[0]
	w.Tabs = w.Tabs[:len(w.Tabs)-1] // 16personalities is last
	win.sync(s)
	mac.sync(s)
	if v := view(mac.sess); strings.Contains(v, "16personalities") || strings.Count(v, "http") != 4 {
		t.Fatalf("the Mac should show the other 4 tabs only:\n%s", v)
	}
}

// A page that moved to another address when it opened on the Mac stays the
// same synced tab: before and after a restart, and until the person goes to
// another page in it.
func TestTabsRedirectKeepsItsKey(t *testing.T) {
	s, mac, win := twoInSync(t, "https://claude.ai/a", "http://vosm.com/")
	var tab *sessions.Tab
	for _, tb := range normalWindows(mac.sess)[0].Tabs {
		if tb.Current().URL == "http://vosm.com/" {
			tab = tb
		}
	}
	// Helium followed the site's redirect.
	tab.Navigations = []sessions.Navigation{{URL: "https://www.vosm.com/", OriginalRequestURL: "http://vosm.com/", Transition: 0x30000000}}
	if n := mac.sync(s); n != 0 {
		t.Fatalf("a redirect sent %d changes", n)
	}
	// Restarted: the first address is no longer recorded.
	for _, tb := range normalWindows(mac.sess)[0].Tabs {
		if strings.Contains(tb.Current().URL, "vosm") {
			tab = tb
		}
	}
	tab.Navigations = []sessions.Navigation{{URL: "https://www.vosm.com/", Transition: 0x30000000}}
	if n := mac.sync(s); n != 0 {
		t.Fatalf("a redirected tab after a restart sent %d changes", n)
	}
	win.sync(s)
	if v := view(win.sess); !strings.Contains(v, "http://vosm.com/") || strings.Contains(v, "www.vosm.com") {
		t.Fatalf("Windows should keep its own tab:\n%s", v)
	}
	// The person opens another page in that tab: that travels.
	for _, tb := range normalWindows(mac.sess)[0].Tabs {
		if strings.Contains(tb.Current().URL, "vosm") {
			tb.Navigations = nav("https://ei.study/")
		}
	}
	if n := mac.sync(s); n == 0 {
		t.Fatalf("a real navigation sent nothing")
	}
	win.sync(s)
	if v := view(win.sess); strings.Contains(v, "vosm") || !strings.Contains(v, "https://ei.study/") {
		t.Fatalf("Windows should show the new page instead:\n%s", v)
	}
	if len(mac.ts.Aliases) != 0 {
		t.Fatalf("the alias should be forgotten once the page is gone: %v", mac.ts.Aliases)
	}
}

func TestTabsSyncable(t *testing.T) {
	for url, want := range map[string]bool{
		"https://claude.ai/":        true,
		"http://example.com/":       true,
		"chrome://extensions/":      true,
		"chrome://settings/privacy": true,
		"chrome://newtab/":          false,
		"chrome://new-tab-page/":    false,
		"chrome://NewTab":           false,
		"chrome://":                 false,
		"chrome://restart":          false,
		"file:///C:/x.html":         false,
		"about:blank":               false,
		"chrome-extension://abc/x":  false,
	} {
		if Syncable(url) != want {
			t.Errorf("Syncable(%q) = %v, want %v", url, !want, want)
		}
	}
}
