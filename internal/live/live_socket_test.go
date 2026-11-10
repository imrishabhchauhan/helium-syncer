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
