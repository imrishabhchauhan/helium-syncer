package live

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// A real profile has hundreds of bookmarks, so the tree the extension sends
// is far bigger than a WebSocket library's default message limit (32 KB for
// coder/websocket). If the server keeps that default it cuts the connection
// on the first tree, the extension reconnects, and live sync never starts.
// This was seen on a real Helium with 504 bookmarks (about 75 KB).
func TestALargeTreeIsAccepted(t *testing.T) {
	const links = 1500 // comfortably past 32 KB, like a big real profile

	var file strings.Builder
	file.WriteString(`{"checksum":"","roots":{"bookmark_bar":{"children":[`)
	nodes := []Node{{ID: "1", Title: "Bookmarks bar", Folder: true, FolderType: "bookmarks-bar"}}
	for i := 0; i < links; i++ {
		id := fmt.Sprint(100 + i)
		url := fmt.Sprintf("https://example.com/a-fairly-long-path/for-bookmark-number-%d", i)
		title := fmt.Sprintf("A bookmark with a realistic title, number %d", i)
		if i > 0 {
			file.WriteString(",")
		}
		fmt.Fprintf(&file, `{"date_added":"13300000000000000","guid":"aaaaaaaa-0000-4000-8000-%012d","id":"%s","name":"%s","type":"url","url":"%s"}`, i, id, title, url)
		nodes = append(nodes, Node{ID: id, ParentID: "1", Index: i, Title: title, URL: url})
	}
	file.WriteString(`],"date_added":"0","date_modified":"0","guid":"0bc5d13f-2cba-5d74-951f-3f233fe6c908","id":"1","name":"Bookmarks bar","type":"folder"},`)
	file.WriteString(`"other":{"children":[],"date_added":"0","date_modified":"0","guid":"82b081ec-3dd3-529c-8475-ab6c344590dd","id":"2","name":"Other bookmarks","type":"folder"},`)
	file.WriteString(`"synced":{"children":[],"date_added":"0","date_modified":"0","guid":"4cf2e351-0e85-532b-bb37-df045d8f8d0f","id":"3","name":"Mobile bookmarks","type":"folder"}},"version":1}`)

	userData, dir := profileWithFile(t, file.String())
	const ext = "abcdefghijklmnopabcdefghijklmnop"
	s := New(userData)
	s.AllowOrigin = OriginOf(ext)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+Path, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": {"chrome-extension://" + ext}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	if err := wsjson.Write(ctx, ws, Msg{Type: "hello", Version: Version}); err != nil {
		t.Fatal(err)
	}
	tree := Msg{Type: "tree", Version: Version, Nodes: nodes}
	if err := wsjson.Write(ctx, ws, tree); err != nil {
		t.Fatal(err)
	}

	for !s.Connected(dir) {
		select {
		case <-ctx.Done():
			t.Fatalf("a %d-bookmark tree never registered the browser; the server dropped it", links)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// The browser reports dates in milliseconds, the file keeps microseconds.
// Reading through the browser must give back the file's exact value, or every
// bookmark looks edited each time sync switches between the two (seen on a
// real Windows PC: 357 bookmarks "changed" on the first live sync).
func TestDatesKeepTheFilesPrecision(t *testing.T) {
	body := strings.Replace(fileJSON, `"date_added":"13300000000000000","guid":"aaaaaaaa-0000-4000-8000-000000000001"`,
		`"date_added":"13300000000000123","guid":"aaaaaaaa-0000-4000-8000-000000000001"`, 1)
	userData, dir := profileWithFile(t, body)
	s := New(userData)
	nodes := browserNodes()
	const epochMs = 11644473600000
	nodes[2].DateAdded = 13300000000000123/1000 - epochMs     // what Chromium reports for SVGL
	nodes[4].DateAdded = 13300000000000000/1000 - epochMs + 5 // Kenney: really a different moment
	connect(s, dir, nodes)
	f, ok := s.ReadBookmarks(dir)
	if !ok {
		t.Fatal("not connected")
	}
	got := map[string]string{}
	walkFile(f, func(n map[string]any, _, _ string) { got[str(n["name"])] = str(n["date_added"]) })
	if got["SVGL"] != "13300000000000123" {
		t.Errorf("SVGL date_added %s, want the file's exact 13300000000000123", got["SVGL"])
	}
	if got["Kenney"] != "13300000000005000" {
		t.Errorf("Kenney date_added %s, want the browser's 13300000000005000 (a real change)", got["Kenney"])
	}
}

// After the app sends a tree, the extension applies it and reports its tree
// back. If an unchanged report counted as a change, or the app sent a tree
// the browser already has, the two would start each other for ever (seen on
// the Mac: a sync every second).
func TestNoEndlessLoopBetweenAppAndBrowser(t *testing.T) {
	userData, dir := profileWithFile(t, fileJSON)
	const ext = "abcdefghijklmnopabcdefghijklmnop"
	s := New(userData)
	s.AllowOrigin = OriginOf(ext)
	changed := make(chan string, 10)
	s.Changed = func(p string) { changed <- p }
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+Path, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": {"chrome-extension://" + ext}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	wsjson.Write(ctx, ws, Msg{Type: "hello", Version: Version})
	var hello Msg
	wsjson.Read(ctx, ws, &hello)

	send := func(nodes []Node) { wsjson.Write(ctx, ws, Msg{Type: "tree", Version: Version, Nodes: nodes}) }
	got := func() int {
		time.Sleep(150 * time.Millisecond)
		return len(changed)
	}
	send(browserNodes())
	if n := got(); n != 1 {
		t.Fatalf("first tree: %d change(s), want 1", n)
	}
	<-changed
	send(browserNodes()) // the same tree again, as after an apply
	if n := got(); n != 0 {
		t.Fatalf("an unchanged tree counted as %d change(s)", n)
	}
	moved := browserNodes()
	moved[2].Title = "SVGL renamed"
	send(moved)
	if n := got(); n != 1 {
		t.Fatalf("a renamed bookmark: %d change(s), want 1", n)
	}
	<-changed

	// The app writes back what the browser already has: nothing is sent.
	f, ok := s.ReadBookmarks(dir)
	if !ok {
		t.Fatal("not connected")
	}
	if err := s.WriteBookmarks(dir, f); err != nil {
		t.Fatal(err)
	}
	rctx, rcancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer rcancel()
	var m Msg
	if err := wsjson.Read(rctx, ws, &m); err == nil {
		t.Fatalf("the app sent %q for a tree the browser already has", m.Type)
	}
}
