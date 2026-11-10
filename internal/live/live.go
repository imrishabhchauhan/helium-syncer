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
// made in the browser while it is open, or by the extension because the
// other computer sent it, has no guid the sync knows: it gets one now, kept
// by browser id (see bookmarkKey), and RestoreKeys writes it into the file
// after Helium quits, in place of the random guid Helium chose. Without that
// step every such bookmark would read as deleted and made again, on both
// computers in turn.
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

// MaxMessageBytes is the biggest message the app accepts from the extension.
// The whole bookmark tree comes in one message: about 150 bytes a bookmark,
// so 500 bookmarks is already 75 KB, well past the WebSocket library's 32 KB
// default. 64 MB covers several hundred thousand bookmarks; the connection
// only ever comes from this computer, from the extension the app trusts.
const MaxMessageBytes = 64 << 20

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
	// Created answers an "apply": for each bookmark the extension made, the
	// app's placeholder ("new:<guid>") and the browser id Helium gave it.
	Created map[string]string `json:"created,omitempty"`
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
	// pending are the bookmarks the last "apply" asked the browser to make,
	// by placeholder, until the extension says which ids they got.
	pending map[string]Node
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
	// KeysFile keeps the sync keys of bookmarks made in an open browser
	// (see bookmarkKey), so they survive the app restarting before Helium
	// quits. Empty keeps them in memory only.
	KeysFile string

	mu    sync.Mutex
	conns map[string]*conn                  // by profile directory
	guid  map[string]idGUIDs                // by profile directory
	keys  map[string]map[string]bookmarkKey // profile directory -> browser id -> key
}

// bookmarkKey is the sync identity of a bookmark that exists in the open
// browser but not yet in its file: one the extension made because the other
// computer sent it, or one the person made while Helium was open.
//
// The merge engine knows every bookmark by a guid. The extensions API cannot
// set or read that guid, so when Helium quits it writes a random one of its
// own for such a bookmark. Read as it is, the file would then say "that
// bookmark was deleted, and here is a new one", and the other computer would
// do the same in turn, back and forth. Remembering the key by browser id
// (ids do not change) lets the sync after the quit put the right guid back
// (RestoreKeys). URL and title make sure the id still means the same thing.
type bookmarkKey struct {
	GUID   string `json:"guid"`
	URL    string `json:"url,omitempty"`
	Title  string `json:"title,omitempty"`
	Folder bool   `json:"folder,omitempty"`
}

// sameThing reports whether a file node is still the bookmark k was made for.
func (k bookmarkKey) sameThing(node map[string]any) bool {
	folder := str(node["type"]) == "folder"
	if folder != k.Folder {
		return false
	}
	if folder {
		return str(node["name"]) == k.Title
	}
	return str(node["url"]) == k.URL
}

// loadKeys reads KeysFile once. The caller holds s.mu.
func (s *Server) loadKeys() {
	if s.keys != nil {
		return
	}
	s.keys = map[string]map[string]bookmarkKey{}
	if s.KeysFile == "" {
		return
	}
	if b, err := os.ReadFile(s.KeysFile); err == nil {
		json.Unmarshal(b, &s.keys)
		if s.keys == nil {
			s.keys = map[string]map[string]bookmarkKey{}
		}
	}
}

// saveKeys writes KeysFile. The caller holds s.mu.
func (s *Server) saveKeys() {
	if s.KeysFile == "" {
		return
	}
	b, err := json.Marshal(s.keys)
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(s.KeysFile), 0o700)
	tmp := s.KeysFile + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, s.KeysFile)
	}
}

// remember records a key for a browser id. The caller holds s.mu.
func (s *Server) remember(profileDir, id string, k bookmarkKey) {
	s.loadKeys()
	if s.keys[profileDir] == nil {
		s.keys[profileDir] = map[string]bookmarkKey{}
	}
	s.keys[profileDir][id] = k
	s.saveKeys()
}

// RestoreKeys puts the remembered sync keys back into a Bookmarks file read
// after Helium quit, where Helium wrote its own guid instead, and forgets
// them: from now on the file itself carries the right guid. It returns how
// many it put back. A key whose id now holds something else is dropped.
func (s *Server) RestoreKeys(profileDir string, f *bookmarks.File) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadKeys()
	ks := s.keys[profileDir]
	if len(ks) == 0 {
		return 0
	}
	inFile := map[string]bool{}
	walkFile(f, func(_ map[string]any, _, guid string) { inFile[guid] = true })
	n := 0
	walkFile(f, func(node map[string]any, _, guid string) {
		k, ok := ks[str(node["id"])]
		if !ok || guid == k.GUID || inFile[k.GUID] || !k.sameThing(node) {
			return
		}
		node["guid"] = k.GUID
		inFile[k.GUID] = true
		n++
	})
	delete(s.keys, profileDir)
	s.saveKeys()
	delete(s.guid, profileDir) // learn from the file again
	return n
}

// idGUIDs is one profile's translation between the two kinds of id.
type idGUIDs struct {
	toGUID map[string]string // browser id  -> file guid
	toID   map[string]string // file guid   -> browser id
	// added is each bookmark's date_added exactly as the file has it. The
	// browser reports milliseconds and the file keeps microseconds; without
	// the file's value every bookmark would look changed on each switch
	// between the browser and the file, and sync back and forth for ever.
	added map[string]string
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
		ws.SetReadLimit(MaxMessageBytes)
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
			// The same tree again (the extension reports after it applied
			// what we sent) is no change, and must not start a sync.
			changed := !c.seen || !sameShape(c.nodes, m.Nodes)
			if !c.seen {
				// A new connection is a browser that just started, so its
				// file is current: learn ids and dates from it again. What
				// was learned before may come from an older file (another
				// sync may have rewritten it while Helium was closed).
				delete(s.guid, profile)
			}
			c.profile, c.nodes, c.seen = profile, m.Nodes, true
			s.conns[profile] = c
			s.mu.Unlock()
			if changed && s.Changed != nil {
				s.Changed(profile)
			}
		case "created":
			s.mu.Lock()
			if c.profile != "" {
				ids, ok := s.guid[c.profile]
				for placeholder, id := range m.Created {
					want, known := c.pending[placeholder]
					guid := strings.TrimPrefix(placeholder, "new:")
					if !known || id == "" || guid == placeholder {
						continue
					}
					if ok {
						ids.toGUID[id] = guid
						ids.toID[guid] = id
					}
					s.remember(c.profile, id, bookmarkKey{GUID: guid, URL: want.URL, Title: want.Title, Folder: want.Folder})
					delete(c.pending, placeholder)
				}
			}
			s.mu.Unlock()
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
// whatever the browser last wrote, which is enough: ids do not change. The
// caller holds s.mu.
func (s *Server) learn(profileDir string) idGUIDs {
	m := idGUIDs{toGUID: map[string]string{}, toID: map[string]string{}, added: map[string]string{}}
	f, err := bookmarks.Load(filepath.Join(profileDir, "Bookmarks"))
	if err != nil {
		return m
	}
	s.loadKeys()
	remembered := s.keys[profileDir]
	walkFile(f, func(n map[string]any, _, guid string) {
		id := str(n["id"])
		if id == "" || guid == "" {
			return
		}
		// Helium reopened before a sync put the remembered key into the
		// file: the key, not Helium's own guid, is this bookmark's identity.
		if k, ok := remembered[id]; ok && k.sameThing(n) {
			guid = k.GUID
		}
		m.toGUID[id] = guid
		m.toID[guid] = id
		if a := str(n["date_added"]); a != "" {
			m.added[id] = a
		}
	})
	// Bookmarks the browser holds but the file does not have yet: made or
	// received while Helium is open, and remembered then.
	for id, k := range remembered {
		if _, inFile := m.toGUID[id]; !inFile {
			m.toGUID[id] = k.GUID
			m.toID[k.GUID] = id
		}
	}
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

// guidFor returns the sync guid for a browser node. A bookmark the person
// made while the browser was open has none yet: one is made for it and
// remembered, so it keeps it after Helium quits (see bookmarkKey). The caller
// holds s.mu.
func (s *Server) guidFor(profileDir string, m idGUIDs, n Node) string {
	if g, ok := m.toGUID[n.ID]; ok {
		return g
	}
	g := uuid.NewString()
	m.toGUID[n.ID] = g
	m.toID[g] = n.ID
	s.remember(profileDir, n.ID, bookmarkKey{GUID: g, URL: n.URL, Title: n.Title, Folder: n.Folder})
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
	defer s.mu.Unlock() // the maps are shared with the connection's reader
	nodes := append([]Node(nil), c.nodes...)
	ids, known := s.guid[profileDir]
	if !known {
		ids = s.learn(profileDir)
		s.guid[profileDir] = ids
	}

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
				"guid":       s.guidFor(profileDir, ids, n),
				"id":         n.ID,
				"name":       n.Title,
				"date_added": ids.dateAdded(n.ID, n.DateAdded),
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

// dateAdded is the file's own date_added when it is the same moment the
// browser reported, to the millisecond; otherwise the browser's value.
func (m idGUIDs) dateAdded(id string, ms int64) string {
	browser := chromeTime(ms)
	exact, ok := m.added[id]
	if !ok || len(exact) < 4 || len(browser) < 4 {
		return browser
	}
	if exact[:len(exact)-3] == browser[:len(browser)-3] {
		return exact
	}
	return browser
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
	ids, known := s.guid[profileDir]
	if ok && c.seen && !known {
		// Without the id map every bookmark would go out as new.
		ids = s.learn(profileDir)
		s.guid[profileDir] = ids
	}
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
	// Nothing to change: say nothing. Sending anyway makes the extension
	// apply it and report its tree back, which starts another sync, and so
	// on for ever (seen on the Mac: a sync every second).
	if sameShape(out, c.nodes) {
		return nil
	}
	pending := map[string]Node{}
	for _, n := range out {
		if strings.HasPrefix(n.ID, "new:") {
			pending[n.ID] = n
		}
	}
	s.mu.Lock()
	c.pending = pending
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return wsjson.Write(ctx, c.ws, Msg{Type: "apply", Profile: profileDir, Nodes: out})
}

// place is where a bookmark sits and what it shows.
type place struct {
	parent, title, url string
	index              int
	folder             bool
}

// shapeOf is what a tree looks like to a person: which bookmark is where,
// with what title and address. Dates and the permanent folders' own entries
// are left out; neither is something the extension changes.
func shapeOf(nodes []Node) map[string]place {
	m := make(map[string]place, len(nodes))
	for _, n := range nodes {
		if n.FolderType != "" {
			continue
		}
		m[n.ID] = place{parent: n.ParentID, title: n.Title, url: n.URL, index: n.Index, folder: n.Folder}
	}
	return m
}

// sameShape reports whether two trees hold the same bookmarks in the same
// places.
func sameShape(a, b []Node) bool {
	sa, sb := shapeOf(a), shapeOf(b)
	if len(sa) != len(sb) {
		return false
	}
	for id, p := range sa {
		if q, ok := sb[id]; !ok || q != p {
			return false
		}
	}
	return true
}

func folderType(root string) string {
	for ft, r := range chromiumRoots {
		if r == root {
			return ft
		}
	}
	return ""
}

// OriginOf builds the AllowOrigin check for one extension id.
func OriginOf(extensionID string) func(string) bool {
	want := "chrome-extension://" + strings.TrimSpace(extensionID)
	return func(origin string) bool { return origin != "" && origin == want }
}
