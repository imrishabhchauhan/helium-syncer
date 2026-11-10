// Package live syncs bookmarks while Helium is open.
//
// A tool that edits Helium's files cannot change a browser that is running:
// Chromium keeps bookmarks in memory and rewrites the file when it quits, so
// the change is thrown away. The companion extension does it from inside the
// browser instead, through Helium's own bookmarks API, and talks to this
// package over a WebSocket on the agent's existing local port.
//
// One wrinkle decides the shape of this code. The merge engine keys every
// bookmark on its `guid`, the id Chromium writes in the Bookmarks file. The
// extensions API never exposes that guid; it exposes a different `id`, which
// Chromium documents as unique within the profile and unchanged across
// restarts. The Bookmarks file carries both, so this package reads the file
// once to learn id -> guid, and translates in both directions. A bookmark
// made in the browser while it is open has an id this package has not seen:
// it gets a provisional guid now, and `Reconcile` swaps in Chromium's real
// one the next time the file is read. Without that step the same bookmark
// would exist twice, once under each guid.
package live

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"

	"github.com/imrishabhchauhan/helium-syncer/internal/bookmarks"
)

// Path is where the extension connects on the agent's local port.
const Path = "/live"

// Protocol version; the extension sends it in hello and an older or newer
// one is refused rather than guessed at.
const Version = 1

// Node is one bookmark or folder as the extension sees it. The field names
// are the browser's, so the extension sends what its API gave it.
type Node struct {
	ID        string `json:"id"`
	ParentID  string `json:"parentId,omitempty"`
	Index     int    `json:"index"`
	Title     string `json:"title"`
	URL       string `json:"url,omitempty"`
	DateAdded int64  `json:"dateAdded,omitempty"`
	Folder    bool   `json:"folder"`
	// FolderType marks the browser's own top folders: "bookmarks-bar",
	// "other", "mobile". Their names are translated and their ids are not
	// fixed, so this is the only safe way to recognise them.
	FolderType string `json:"folderType,omitempty"`
}

// Msg is every message in both directions.
type Msg struct {
	Type    string `json:"type"`
	Version int    `json:"version,omitempty"`
	Profile string `json:"profile,omitempty"`
	Nodes   []Node `json:"nodes,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// chromiumRoots maps the browser's folder types to the names the Bookmarks
// file uses for its three roots.
var chromiumRoots = map[string]string{
	"bookmarks-bar": "bookmark_bar",
	"other":         "other",
	"mobile":        "synced",
}

// rootGUIDs are Chromium's fixed guids for the three permanent folders.
var rootGUIDs = map[string]string{
	"bookmark_bar": "0bc5d13f-2cba-5d74-951f-3f233fe6c908",
	"other":        "82b081ec-3dd3-529c-8475-ab6c344590dd",
	"synced":       "4cf2e351-0e85-532b-bb37-df045d8f8d0f",
}

// conn is one connected browser profile.
type conn struct {
	ws      *websocket.Conn
	profile string // the profile directory, e.g. ...\User Data\Profile 2
	nodes   []Node // the tree as the browser last reported it
	seen    bool   // a tree has arrived at least once
}

// Server holds the connected browsers and translates between the extension's
// ids and the guids the merge engine works in.
type Server struct {
	// UserData is Helium's folder, used to find each profile.
	UserData string
	// Changed is called when a browser reports a change, so the agent can
	// start a sync. It must not block.
	Changed func(profile string)
	// AllowOrigin reports whether this WebSocket Origin may connect. The
	// browser sets Origin itself and a web page cannot forge another
	// extension's, so this is what keeps other pages out.
	AllowOrigin func(origin string) bool

	mu    sync.Mutex
	conns map[string]*conn   // by profile directory
	guid  map[string]idGUIDs // by profile directory
}

// idGUIDs is one profile's translation between the two kinds of id.
type idGUIDs struct {
	toGUID map[string]string // browser id  -> file guid
	toID   map[string]string // file guid   -> browser id
	// provisional guids this package invented for bookmarks made while the
	// browser was open, waiting for Reconcile to replace them.
	provisional map[string]bool
}

// New makes a Server. UserData is Helium's folder.
func New(userData string) *Server {
	return &Server{UserData: userData, conns: map[string]*conn{}, guid: map[string]idGUIDs{}}
}

// Connected reports whether a browser is connected for this profile.
func (s *Server) Connected(profileDir string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.conns[profileDir]
	return ok && c.seen
}

// Profiles lists the profile directories with a browser connected.
func (s *Server) Profiles() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for p, c := range s.conns {
		if c.seen {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// Handler serves the WebSocket the extension connects to. Mount it at Path
// on the agent's existing local listener.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if s.AllowOrigin == nil || !s.AllowOrigin(origin) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		// The Origin check above is the gate; the library's own same-origin
		// rule would refuse an extension origin outright.
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		s.serve(r.Context(), ws)
	})
}

func (s *Server) serve(ctx context.Context, ws *websocket.Conn) {
	defer ws.CloseNow()
	c := &conn{ws: ws}
	for {
		var m Msg
		if err := wsjson.Read(ctx, ws, &m); err != nil {
			s.drop(c)
			return
		}
		switch m.Type {
		case "hello":
			if m.Version != Version {
				_ = wsjson.Write(ctx, ws, Msg{Type: "refused",
					Reason: fmt.Sprintf("this extension speaks version %d, the app speaks %d; update both", m.Version, Version)})
				s.drop(c)
				return
			}
			_ = wsjson.Write(ctx, ws, Msg{Type: "hello", Version: Version})
		case "tree":
			if len(m.Nodes) == 0 {
				continue
			}
			profile := s.match(m.Profile, m.Nodes)
			if profile == "" {
				_ = wsjson.Write(ctx, ws, Msg{Type: "refused",
					Reason: "cannot tell which profile this browser window is"})
				continue
			}
			s.mu.Lock()
			c.profile, c.nodes, c.seen = profile, m.Nodes, true
			s.conns[profile] = c
			s.mu.Unlock()
			if s.Changed != nil {
				s.Changed(profile)
			}
		case "pong":
			// keeps the extension's service worker awake; nothing to do
		}
	}
}

func (s *Server) drop(c *conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.profile != "" && s.conns[c.profile] == c {
		delete(s.conns, c.profile)
	}
}

// match decides which profile directory a connected browser belongs to.
// The extension cannot read its own profile path, so when it does not name
// one this compares the bookmarks it reported against each profile's file
// and takes the best overlap. Two profiles with different bookmarks are
// told apart easily; an empty profile cannot be, and is left unmatched.
func (s *Server) match(named string, nodes []Node) string {
	if named != "" {
		if filepath.IsAbs(named) {
			return named
		}
		return filepath.Join(s.UserData, named)
	}
	want := map[string]bool{}
	for _, n := range nodes {
		if n.URL != "" {
			want[n.URL] = true
		}
	}
	if len(want) == 0 {
		return ""
	}
	best, bestHits := "", 0
	ents, err := os.ReadDir(s.UserData)
	if err != nil {
		return ""
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(s.UserData, e.Name())
		f, err := bookmarks.Load(filepath.Join(dir, "Bookmarks"))
		if err != nil {
			continue
		}
		hits := 0
		walkFile(f, func(_ map[string]any, url, _ string) {
			if url != "" && want[url] {
				hits++
			}
		})
		if hits > bestHits {
			best, bestHits = dir, hits
		}
	}
	// Demand a real overlap: at least half of what the browser reported, and
	// never on a single shared link, which two unrelated profiles may have.
	if bestHits*2 < len(want) || bestHits < 2 {
		return ""
	}
	return best
}

// walkFile visits every bookmark and folder in a Bookmarks file, giving the
// node, its url (empty for a folder) and its guid.
func walkFile(f *bookmarks.File, fn func(node map[string]any, url, guid string)) {
	var visit func(n map[string]any)
	visit = func(n map[string]any) {
		kids, _ := n["children"].([]any)
		for _, k := range kids {
			km, ok := k.(map[string]any)
			if !ok {
				continue
			}
			fn(km, str(km["url"]), str(km["guid"]))
			visit(km)
		}
	}
	for _, root := range bookmarks.Roots {
		if n := f.Root(root); n != nil {
			visit(n)
		}
	}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// learn reads a profile's Bookmarks file and records id -> guid. The file is
// whatever the browser last wrote, which is enough: ids do not change.
func (s *Server) learn(profileDir string) idGUIDs {
	m := idGUIDs{toGUID: map[string]string{}, toID: map[string]string{}, provisional: map[string]bool{}}
	f, err := bookmarks.Load(filepath.Join(profileDir, "Bookmarks"))
	if err != nil {
		return m
	}
	walkFile(f, func(n map[string]any, _, guid string) {
		id := str(n["id"])
		if id == "" || guid == "" {
			return
		}
		m.toGUID[id] = guid
		m.toID[guid] = id
	})
	for root, guid := range rootGUIDs {
		if n := f.Root(root); n != nil {
			if id := str(n["id"]); id != "" {
				m.toGUID[id] = guid
				m.toID[guid] = id
			}
		}
	}
	return m
}

// guidFor returns the file guid for a browser id, inventing a provisional one
// for a bookmark made while the browser was open. Reconcile replaces it later.
func (m idGUIDs) guidFor(id string) string {
	if g, ok := m.toGUID[id]; ok {
		return g
	}
	g := uuid.NewString()
	m.toGUID[id] = g
	m.toID[g] = id
	m.provisional[g] = true
	return g
}

// ReadBookmarks builds the Bookmarks document from what the open browser
// reported, so the merge engine sees the live tree instead of a stale file.
func (s *Server) ReadBookmarks(profileDir string) (*bookmarks.File, bool) {
	s.mu.Lock()
	c, ok := s.conns[profileDir]
	if !ok || !c.seen {
		s.mu.Unlock()
		return nil, false
	}
	nodes := append([]Node(nil), c.nodes...)
	ids, known := s.guid[profileDir]
	if !known {
		ids = s.learn(profileDir)
		s.guid[profileDir] = ids
	}
	s.mu.Unlock()

	byParent := map[string][]Node{}
	roots := map[string]string{} // browser id -> file root name
	for _, n := range nodes {
		if r, ok := chromiumRoots[n.FolderType]; ok {
			roots[n.ID] = r
			continue
		}
		byParent[n.ParentID] = append(byParent[n.ParentID], n)
	}
	for p := range byParent {
		kids := byParent[p]
		sort.SliceStable(kids, func(i, j int) bool { return kids[i].Index < kids[j].Index })
		byParent[p] = kids
	}

	var build func(parentID string) []any
	build = func(parentID string) []any {
		var out []any
		for _, n := range byParent[parentID] {
			node := map[string]any{
				"guid":       ids.guidFor(n.ID),
				"id":         n.ID,
				"name":       n.Title,
				"date_added": chromeTime(n.DateAdded),
			}
			if n.Folder {
				node["type"] = "folder"
				node["children"] = build(n.ID)
				node["date_modified"] = "0"
			} else {
				node["type"] = "url"
				node["url"] = n.URL
			}
			out = append(out, node)
		}
		if out == nil {
			out = []any{}
		}
		return out
	}

	doc := map[string]any{"version": float64(1), "roots": map[string]any{}}
	rootsOut := doc["roots"].(map[string]any)
	// Start from the three roots the file always has, so a browser that
	// reports only two still produces a complete document.
	for _, name := range bookmarks.Roots {
		rootsOut[name] = map[string]any{
			"children": []any{}, "date_added": "0", "date_modified": "0",
			"guid": rootGUIDs[name], "id": rootID(name), "name": rootName(name), "type": "folder",
		}
	}
	for id, name := range roots {
		r := rootsOut[name].(map[string]any)
		r["children"] = build(id)
		r["id"] = id
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, false
	}
	f, err := bookmarks.Parse(raw)
	if err != nil {
		return nil, false
	}
	return f, true
}

func rootID(name string) string {
	switch name {
	case "bookmark_bar":
		return "1"
	case "other":
		return "2"
	default:
		return "3"
	}
}

func rootName(name string) string {
	switch name {
	case "bookmark_bar":
		return "Bookmarks bar"
	case "other":
		return "Other bookmarks"
	default:
		return "Mobile bookmarks"
	}
}

// chromeTime turns milliseconds since 1970 into Chromium's microseconds
// since 1601, as the Bookmarks file stores them.
func chromeTime(ms int64) string {
	if ms <= 0 {
		return "0"
	}
	const epoch = 11644473600000 // ms between 1601 and 1970
	return fmt.Sprintf("%d", (ms+epoch)*1000)
}

// WriteBookmarks asks the connected browser to make its tree match f.
func (s *Server) WriteBookmarks(profileDir string, f *bookmarks.File) error {
	s.mu.Lock()
	c, ok := s.conns[profileDir]
	ids := s.guid[profileDir]
	s.mu.Unlock()
	if !ok || !c.seen {
		return errors.New("live: no browser connected for this profile")
	}
	var out []Node
	var visit func(parentBrowserID string, n map[string]any)
	visit = func(parentBrowserID string, n map[string]any) {
		kids, _ := n["children"].([]any)
		for i, k := range kids {
			km, ok := k.(map[string]any)
			if !ok {
				continue
			}
			guid := str(km["guid"])
			node := Node{
				ID:       ids.toID[guid],
				ParentID: parentBrowserID,
				Index:    i,
				Title:    str(km["name"]),
				URL:      str(km["url"]),
				Folder:   str(km["type"]) == "folder",
			}
			// A node the browser has never seen has no browser id; the
			// extension creates it and tells us the id it was given.
			if node.ID == "" {
				node.ID = "new:" + guid
			}
			out = append(out, node)
			if node.Folder {
				visit(node.ID, km)
			}
		}
	}
	for _, name := range bookmarks.Roots {
		n := f.Root(name)
		if n == nil {
			continue
		}
		id := ids.toID[rootGUIDs[name]]
		if id == "" {
			id = rootID(name)
		}
		out = append(out, Node{ID: id, Title: rootName(name), Folder: true, FolderType: folderType(name)})
		visit(id, n)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return wsjson.Write(ctx, c.ws, Msg{Type: "apply", Profile: profileDir, Nodes: out})
}

func folderType(root string) string {
	for ft, r := range chromiumRoots {
		if r == root {
			return ft
		}
	}
	return ""
}

// Reconcile is called after the browser has quit and written its file. Every
// bookmark made while the browser was open now has Chromium's own guid in the
// file; this swaps the provisional guid this package invented for the real
// one, keyed on the browser id, which does not change. remap tells the caller
// which guids moved so stored state can follow.
func (s *Server) Reconcile(profileDir string) (remap map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids, ok := s.guid[profileDir]
	if !ok || len(ids.provisional) == 0 {
		return nil
	}
	fresh := s.learn(profileDir)
	remap = map[string]string{}
	for guid := range ids.provisional {
		id := ids.toID[guid]
		real, ok := fresh.toGUID[id]
		if !ok || real == guid {
			continue
		}
		remap[guid] = real
		delete(ids.toGUID, id)
		delete(ids.toID, guid)
		ids.toGUID[id] = real
		ids.toID[real] = id
		delete(ids.provisional, guid)
	}
	if len(remap) == 0 {
		return nil
	}
	return remap
}

// OriginOf builds the AllowOrigin check for one extension id.
func OriginOf(extensionID string) func(string) bool {
	want := "chrome-extension://" + strings.TrimSpace(extensionID)
	return func(origin string) bool { return origin != "" && origin == want }
}
