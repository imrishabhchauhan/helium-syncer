package live

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imrishabhchauhan/helium-syncer/internal/bookmarks"
)

// a Bookmarks file with two links, as Helium would have written it. Both
// kinds of id are here: `id` is what the extension sees, `guid` is what the
// merge engine keys on.
const fileJSON = `{"checksum":"","roots":{
 "bookmark_bar":{"children":[
   {"date_added":"13300000000000000","guid":"aaaaaaaa-0000-4000-8000-000000000001","id":"10","name":"SVGL","type":"url","url":"https://svgl.app/"},
   {"children":[
     {"date_added":"13300000000000000","guid":"aaaaaaaa-0000-4000-8000-000000000003","id":"12","name":"Kenney","type":"url","url":"https://kenney.nl/"}
   ],"date_added":"13300000000000000","date_modified":"0","guid":"aaaaaaaa-0000-4000-8000-000000000002","id":"11","name":"Dev","type":"folder"}
 ],"date_added":"0","date_last_used":"0","date_modified":"0","guid":"0bc5d13f-2cba-5d74-951f-3f233fe6c908","id":"1","name":"Bookmarks bar","type":"folder"},
 "other":{"children":[],"date_added":"0","date_last_used":"0","date_modified":"0","guid":"82b081ec-3dd3-529c-8475-ab6c344590dd","id":"2","name":"Other bookmarks","type":"folder"},
 "synced":{"children":[],"date_added":"0","date_last_used":"0","date_modified":"0","guid":"4cf2e351-0e85-532b-bb37-df045d8f8d0f","id":"3","name":"Mobile bookmarks","type":"folder"}},
 "version":1}`

// browser tree matching the file above, in the shape the extension sends:
// ids only, no guids, because the extensions API never exposes a guid.
func browserNodes() []Node {
	return []Node{
		{ID: "1", Title: "Bookmarks bar", Folder: true, FolderType: "bookmarks-bar"},
		{ID: "2", Title: "Other bookmarks", Folder: true, FolderType: "other"},
		{ID: "10", ParentID: "1", Index: 0, Title: "SVGL", URL: "https://svgl.app/"},
		{ID: "11", ParentID: "1", Index: 1, Title: "Dev", Folder: true},
		{ID: "12", ParentID: "11", Index: 0, Title: "Kenney", URL: "https://kenney.nl/"},
	}
}

func profileWithFile(t *testing.T, body string) (userData, dir string) {
	t.Helper()
	userData = t.TempDir()
	dir = filepath.Join(userData, "Profile 2")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Bookmarks"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return userData, dir
}

func connect(s *Server, dir string, nodes []Node) {
	s.mu.Lock()
	s.conns[dir] = &conn{profile: dir, nodes: nodes, seen: true}
	s.mu.Unlock()
}

// guids of every link the engine would see, by title.
func guidsByTitle(f *bookmarks.File) map[string]string {
	out := map[string]string{}
	walkFile(f, func(n map[string]any, _, guid string) { out[str(n["name"])] = guid })
	return out
}

// The browser's ids must translate to the guids already in the file, or the
// merge engine would treat every bookmark as new and duplicate the lot.
func TestReadUsesTheFilesGUIDs(t *testing.T) {
	userData, dir := profileWithFile(t, fileJSON)
	s := New(userData)
	connect(s, dir, browserNodes())

	f, ok := s.ReadBookmarks(dir)
	if !ok {
		t.Fatal("ReadBookmarks said no browser is connected")
	}
	got := guidsByTitle(f)
	want := map[string]string{
		"SVGL":   "aaaaaaaa-0000-4000-8000-000000000001",
		"Dev":    "aaaaaaaa-0000-4000-8000-000000000002",
		"Kenney": "aaaaaaaa-0000-4000-8000-000000000003",
	}
	for title, guid := range want {
		if got[title] != guid {
			t.Errorf("%s: guid %q, want %q", title, got[title], guid)
		}
	}
	// the folder must still hold its child
	if len(got) != 3 {
		t.Errorf("expected 3 nodes, got %d: %v", len(got), got)
	}
}

// The check above has to be able to fail: a browser id the file does not know
// must NOT come out with a guid from the file.
func TestReadInventsAGUIDForANewBookmark(t *testing.T) {
	userData, dir := profileWithFile(t, fileJSON)
	s := New(userData)
	nodes := append(browserNodes(),
		Node{ID: "99", ParentID: "1", Index: 2, Title: "Made while open", URL: "https://example.com/new"})
	connect(s, dir, nodes)

	f, ok := s.ReadBookmarks(dir)
	if !ok {
		t.Fatal("ReadBookmarks said no browser is connected")
	}
	got := guidsByTitle(f)
	g := got["Made while open"]
	if g == "" {
		t.Fatal("the new bookmark got no guid at all")
	}
	for title, known := range guidsByTitle(mustParse(t, fileJSON)) {
		if g == known {
			t.Fatalf("the new bookmark reused %s's guid %s", title, g)
		}
	}
	s.mu.Lock()
	kept := s.keys[dir]["99"].GUID
	s.mu.Unlock()
	if kept != g {
		t.Errorf("the new guid %s was not remembered for browser id 99 (got %q), so it would be lost when Helium quits", g, kept)
	}
}

// quitFile is the Bookmarks file Helium writes on quit, with one more link,
// id 99, under the random guid Helium chose for it.
func quitFile(heliumGUID, url string) string {
	return strings.Replace(fileJSON,
		`"name":"SVGL","type":"url","url":"https://svgl.app/"}`,
		`"name":"SVGL","type":"url","url":"https://svgl.app/"},`+
			`{"date_added":"13300000000000000","guid":"`+heliumGUID+`","id":"99","name":"Made while open","type":"url","url":"`+url+`"}`, 1)
}

// A bookmark made while Helium was open must keep its sync guid after Helium
// quits and writes its own random one, also when the app restarted in
// between. Otherwise the file reads as "deleted, and a new one made", on
// both computers in turn.
func TestABookmarkMadeWhileOpenKeepsItsKeyAfterQuit(t *testing.T) {
	userData, dir := profileWithFile(t, fileJSON)
	keys := filepath.Join(t.TempDir(), "live-keys.json")
	s := New(userData)
	s.KeysFile = keys
	connect(s, dir, append(browserNodes(),
		Node{ID: "99", ParentID: "1", Index: 2, Title: "Made while open", URL: "https://example.com/new"}))
	f, _ := s.ReadBookmarks(dir)
	key := guidsByTitle(f)["Made while open"]

	// The app restarts; Helium quits and writes its own guid for id 99.
	after := New(userData)
	after.KeysFile = keys
	file := mustParse(t, quitFile("bbbbbbbb-0000-4000-8000-000000000099", "https://example.com/new"))
	if n := after.RestoreKeys(dir, file); n != 1 {
		t.Fatalf("RestoreKeys put back %d key(s), want 1", n)
	}
	if got := guidsByTitle(file)["Made while open"]; got != key {
		t.Errorf("after quit the bookmark has guid %s, want its sync key %s", got, key)
	}
	if got := guidsByTitle(file)["SVGL"]; got != "aaaaaaaa-0000-4000-8000-000000000001" {
		t.Errorf("an ordinary bookmark's guid changed to %s", got)
	}
	// Done once: the next file read changes nothing.
	again := mustParse(t, quitFile("cccccccc-0000-4000-8000-000000000099", "https://example.com/new"))
	if n := after.RestoreKeys(dir, again); n != 0 {
		t.Errorf("RestoreKeys put back %d key(s) a second time", n)
	}
}

// If browser id 99 now holds something else, the remembered key must not be
// forced onto it.
func TestRestoreKeysIgnoresAnIDThatChangedMeaning(t *testing.T) {
	userData, dir := profileWithFile(t, fileJSON)
	s := New(userData)
	connect(s, dir, append(browserNodes(),
		Node{ID: "99", ParentID: "1", Index: 2, Title: "Made while open", URL: "https://example.com/new"}))
	s.ReadBookmarks(dir)
	file := mustParse(t, quitFile("bbbbbbbb-0000-4000-8000-000000000099", "https://example.com/something-else"))
	if n := s.RestoreKeys(dir, file); n != 0 {
		t.Errorf("a key was forced onto a different bookmark (%d)", n)
	}
}

// A browser that is not connected must fall back to the file, never pretend.
func TestReadSaysNoWhenNothingIsConnected(t *testing.T) {
	userData, dir := profileWithFile(t, fileJSON)
	s := New(userData)
	if _, ok := s.ReadBookmarks(dir); ok {
		t.Error("ReadBookmarks claimed a browser was connected when none was")
	}
}

// Only the extension's own origin may connect; a web page must not.
func TestOnlyTheExtensionOriginIsAllowed(t *testing.T) {
	allow := OriginOf("abcdefghijklmnopabcdefghijklmnop")
	if !allow("chrome-extension://abcdefghijklmnopabcdefghijklmnop") {
		t.Error("the extension's own origin was refused")
	}
	for _, bad := range []string{"", "https://example.com", "chrome-extension://someoneelse", "http://127.0.0.1:47811"} {
		if allow(bad) {
			t.Errorf("origin %q was allowed", bad)
		}
	}
}

func mustParse(t *testing.T, body string) *bookmarks.File {
	t.Helper()
	f, err := bookmarks.Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// match must refuse rather than guess when the bookmarks do not line up.
func TestMatchRefusesAStranger(t *testing.T) {
	userData, _ := profileWithFile(t, fileJSON)
	s := New(userData)
	strangers := []Node{
		{ID: "1", Title: "Bookmarks bar", Folder: true, FolderType: "bookmarks-bar"},
		{ID: "20", ParentID: "1", Title: "A", URL: "https://a.example/"},
		{ID: "21", ParentID: "1", Title: "B", URL: "https://b.example/"},
		{ID: "22", ParentID: "1", Title: "C", URL: "https://c.example/"},
	}
	if got := s.match("", strangers); got != "" {
		t.Errorf("matched an unrelated browser to %q", got)
	}
	if got := s.match("", browserNodes()); !strings.HasSuffix(got, "Profile 2") {
		t.Errorf("did not match the right profile, got %q", got)
	}
}

func TestChromeTime(t *testing.T) {
	if got := chromeTime(0); got != "0" {
		t.Errorf("chromeTime(0) = %q, want 0", got)
	}
	// 1 Jan 1970 in Chromium's clock is the gap between 1601 and 1970.
	if got := chromeTime(1); got != "11644473600001000" {
		t.Errorf("chromeTime(1) = %q", got)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(`{"t":"1"}`), &doc); err != nil {
		t.Fatal(err)
	}
}
