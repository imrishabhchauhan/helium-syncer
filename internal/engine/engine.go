package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/imrishabhchauhan/helium-syncer/internal/bookmarks"
	"github.com/imrishabhchauhan/helium-syncer/internal/chromejson"
	"github.com/imrishabhchauhan/helium-syncer/internal/crdt"
	"github.com/imrishabhchauhan/helium-syncer/internal/helium"
	"github.com/imrishabhchauhan/helium-syncer/internal/history"
	"github.com/imrishabhchauhan/helium-syncer/internal/merge"
	"github.com/imrishabhchauhan/helium-syncer/internal/prefhash"
	"github.com/imrishabhchauhan/helium-syncer/internal/sessions"
)

// Transport moves ops between devices. The real one talks to the
// end-to-end encrypted server; tests use Memory.
type Transport interface {
	Push(ctx context.Context, ops []crdt.Op) error
	Pull(ctx context.Context, cursor string) (ops []crdt.Op, next string, err error)
	// Versions reports the Helium version each other device last synced with.
	Versions(ctx context.Context, exceptDevice string) (map[string]string, error)
	Announce(ctx context.Context, device, name, version string) error
}

// Config describes one device and one Helium user data folder.
type Config struct {
	UserData   string // Helium's folder, e.g. ...\imput\Helium\User Data
	StateDir   string // Helium Syncer's own folder for this user data
	DeviceID   string // stable id of this device in the sync account
	DeviceName string // shown to the user, e.g. "Rishabh's MacBook Air"
	MachineID  string // Chromium device id (machine SID / IOPlatformUUID) for re-signing
	Transport  Transport
	// RestoreTabs turns on "Continue where you left off" so synced tabs open.
	RestoreTabs bool
	// Off lists the kinds (Kind*) this computer does not sync. Their Helium
	// files are not read or written and local changes are not sent; changes
	// from other computers are kept, and arrive when the kind is on again.
	Off  map[string]bool
	Now  func() time.Time
	GOOS string // target OS file conventions; default runtime.GOOS
	// Running reports whether Helium is open; default helium.IsRunning.
	// Tests replace it to open Helium in the middle of a sync.
	Running func(userData string) (bool, error)
}

// closed is checked right before each write into Helium's folder: if a
// person opens Helium during a sync, the sync stops and is rolled back.
func closed(cfg Config) error {
	is := cfg.Running
	if is == nil {
		is = helium.IsRunning
	}
	running, err := is(cfg.UserData)
	if err != nil {
		return err
	}
	if running {
		return ErrHeliumRunning
	}
	return nil
}

// Kinds of data a person can switch off per computer.
const (
	KindBookmarks  = "bookmarks"
	KindHistory    = "history"
	KindTabs       = "tabs" // open, pinned and grouped tabs
	KindSettings   = "settings"
	KindExtensions = "extensions"
)

// Kinds lists every kind, in the order the app shows them.
var Kinds = []string{KindBookmarks, KindHistory, KindTabs, KindSettings, KindExtensions}

// keep stores ops from other computers without touching Helium's files.
func keep(clock *crdt.Clock, ops []crdt.Op, into func(crdt.Op) *crdt.Map) {
	for _, op := range ops {
		clock.Observe(op.Stamp)
		into(op).Apply(op)
	}
}

// Report says what a run did, for the status line and logs.
type Report struct {
	Sent, Received int
	SentByType     map[string]int // e.g. "bookmark": 12
	ReceivedByType map[string]int
	ProfilesAdded  []string
	Written        []string // files changed in Helium's folder, relative to it
	Paused         string   // version gate message, when sync is paused
	// Took is how long each step took, in milliseconds, e.g. "pull": 120.
	// Steps inside profiles add up over all profiles.
	Took map[string]int64 `json:",omitempty"`
}

// step measures one step of a run into rep.Took: defer rep.step("pull")().
func (rep *Report) step(name string) func() {
	start := time.Now()
	return func() {
		if rep.Took == nil {
			rep.Took = map[string]int64{}
		}
		rep.Took[name] += time.Since(start).Milliseconds()
	}
}

// timer is the report of the run in progress, for steps deep inside it.
type timerKey struct{}

func stepIn(ctx context.Context, name string) func() {
	if rep, ok := ctx.Value(timerKey{}).(*Report); ok {
		return rep.step(name)
	}
	return func() {}
}

// ErrHeliumRunning: the folder is in use; try again after Helium quits.
var ErrHeliumRunning = errors.New("Helium is open on this profile folder; sync runs when it is closed")

var runLock sync.Mutex

// Run performs one complete sync.
func Run(ctx context.Context, cfg Config) (Report, error) {
	runLock.Lock()
	defer runLock.Unlock()
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.GOOS == "" {
		cfg.GOOS = runtime.GOOS
	}
	var rep Report
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return rep, err
	}
	unlock, err := lockState(cfg.StateDir)
	if err != nil {
		return rep, err
	}
	defer unlock()
	if err := closed(cfg); err != nil {
		return rep, err
	}

	// 1. Finish or undo an interrupted previous run.
	if j, err := readJournal(cfg.StateDir); err != nil {
		return rep, err
	} else if j != nil {
		if err := restore(cfg.UserData, *j); err != nil {
			return rep, fmt.Errorf("engine: recovering from an interrupted sync: %w", err)
		}
		os.Remove(filepath.Join(cfg.StateDir, journalFile))
	}

	// 2. Version gate.
	version, err := helium.DataVersion(cfg.UserData)
	if err != nil {
		return rep, err
	}
	ctx = context.WithValue(ctx, timerKey{}, &rep)
	defer rep.step("total")()
	done := rep.step("versions")
	peers, err := cfg.Transport.Versions(ctx, cfg.DeviceID)
	done()
	if err != nil {
		return rep, err
	}
	if err := helium.CheckVersions(version, peers); err != nil {
		var ge *helium.GateError
		if errors.As(err, &ge) {
			rep.Paused = ge.Message
			return rep, nil
		}
		return rep, err
	}

	done = rep.step("load state")
	st, err := LoadState(cfg.StateDir, cfg.DeviceID)
	done()
	if err != nil {
		return rep, err
	}
	clock := crdt.NewClock(cfg.DeviceID, func() int64 { return cfg.Now().UnixMilli() })
	clock.Restore(st.Clock)

	// 3. Send what an earlier run could not send.
	rep.SentByType = map[string]int{}
	if len(st.Outbox) > 0 {
		if err := cfg.Transport.Push(ctx, st.Outbox); err != nil {
			return rep, fmt.Errorf("engine: sending queued changes: %w", err)
		}
		rep.Sent += len(st.Outbox)
		addTypes(rep.SentByType, st.Outbox)
		st.Outbox = nil
	}

	// 4. Receive.
	done = rep.step("pull")
	incoming, next, err := cfg.Transport.Pull(ctx, st.Cursor)
	done()
	if err != nil {
		return rep, err
	}
	var mine []crdt.Op
	for _, op := range incoming {
		if op.Stamp.Device != cfg.DeviceID {
			mine = append(mine, op) // our own ops are already applied
		}
	}
	incoming = mine
	rep.Received = len(incoming)
	rep.ReceivedByType = countTypes(incoming)

	// 5. Backup, then apply.
	var dirs []string
	for d := range st.Links {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	done = rep.step("backup")
	bk, err := backup(cfg.StateDir, cfg.UserData, dirs, cfg.Now())
	done()
	if err != nil {
		return rep, fmt.Errorf("engine: backup failed, nothing was changed: %w", err)
	}
	j := journal{Backup: bk}
	if err := writeJournal(cfg.StateDir, j); err != nil {
		return rep, err
	}

	done = rep.step("apply")
	out, added, err := apply(ctx, cfg, st, clock, incoming, &j)
	done()
	if err != nil {
		// Put everything back exactly as it was.
		if rerr := restore(cfg.UserData, j); rerr != nil {
			return rep, fmt.Errorf("engine: sync failed (%v) and restore failed: %w", err, rerr)
		}
		os.Remove(filepath.Join(cfg.StateDir, journalFile))
		return rep, err
	}
	rep.ProfilesAdded = added
	for _, w := range j.Written {
		if rel, err := filepath.Rel(cfg.UserData, w); err == nil {
			rep.Written = append(rep.Written, rel)
		}
	}

	// 6. Remember, then send.
	st.Cursor = next
	st.Clock = clock.Last()
	st.Outbox = out
	done = rep.step("save state")
	err = st.Save(cfg.StateDir)
	done()
	if err != nil {
		return rep, err
	}
	os.Remove(filepath.Join(cfg.StateDir, journalFile))
	if len(out) > 0 {
		done = rep.step("push")
		err := cfg.Transport.Push(ctx, out)
		done()
		if err == nil {
			rep.Sent += len(out)
			addTypes(rep.SentByType, out)
			st.Outbox = nil
			if err := st.Save(cfg.StateDir); err != nil {
				return rep, err
			}
		}
	}
	cfg.Transport.Announce(ctx, cfg.DeviceID, cfg.DeviceName, version)
	pruneBackups(cfg.StateDir, 20, 30*24*time.Hour, cfg.Now())
	return rep, nil
}

// Op types are scoped per profile: "<profile id>/<type>".
func scoped(profileID, typ string) string { return profileID + "/" + typ }

func splitScope(t string) (profileID, typ string) {
	if i := strings.IndexByte(t, '/'); i >= 0 {
		return t[:i], t[i+1:]
	}
	return "", t
}

func scope(ops []crdt.Op, profileID string) []crdt.Op {
	for i := range ops {
		ops[i].Type = scoped(profileID, ops[i].Type)
	}
	return ops
}

func apply(ctx context.Context, cfg Config, st *State, clock *crdt.Clock, incoming []crdt.Op, j *journal) ([]crdt.Op, []string, error) {
	// Route incoming ops.
	var profileOps []crdt.Op
	perProfile := map[string][]crdt.Op{}
	for _, op := range incoming {
		pid, typ := splitScope(op.Type)
		if pid == "" {
			if typ == merge.TypeProfile {
				profileOps = append(profileOps, op)
			}
			continue
		}
		op.Type = typ
		perProfile[pid] = append(perProfile[pid], op)
	}
	var out []crdt.Op

	// Profiles first: new ones are created next to the local ones.
	before := map[string]bool{}
	for d := range st.Links {
		before[d] = true
	}
	if st.ProfileSuffix == nil {
		st.ProfileSuffix = map[string]string{}
	}
	if err := closed(cfg); err != nil {
		return nil, nil, err
	}
	base, pOut, plan, err := merge.SyncProfiles(cfg.UserData, st.Links, st.ProfileSuffix, st.ProfilesBase, st.ProfilesMap, clock, profileOps,
		func(i int) int { return 26 + i%30 })
	if err != nil {
		return nil, nil, err
	}
	st.ProfilesBase = base
	out = append(out, pOut...)
	var added []string
	for dir := range plan.Added {
		j.Created = append(j.Created, filepath.Join(cfg.UserData, dir))
		added = append(added, dir)
	}
	if len(plan.Added) > 0 || len(plan.Renamed) > 0 {
		j.Written = append(j.Written, filepath.Join(cfg.UserData, "Local State"))
	}
	sort.Strings(added)
	if err := writeJournal(cfg.StateDir, *j); err != nil {
		return nil, nil, err
	}

	var dirs []string
	for d := range st.Links {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		pid := st.Links[dir]
		ops, err := syncProfile(ctx, cfg, st.profile(pid), filepath.Join(cfg.UserData, dir), clock, perProfile[pid], j)
		if err != nil {
			return nil, nil, fmt.Errorf("profile %q: %w", dir, err)
		}
		out = append(out, scope(ops, pid)...)
	}
	return out, added, nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// noteCreate records a file this run is about to create, for recovery.
func noteCreate(cfg Config, j *journal, path string) error {
	if exists(path) {
		return nil
	}
	j.Created = append(j.Created, path)
	return writeJournal(cfg.StateDir, *j)
}

const emptyBookmarks = `{"checksum":"","roots":{` +
	`"bookmark_bar":{"children":[],"date_added":"0","date_last_used":"0","date_modified":"0","guid":"0bc5d13f-2cba-5d74-951f-3f233fe6c908","id":"1","name":"Bookmarks bar","type":"folder"},` +
	`"other":{"children":[],"date_added":"0","date_last_used":"0","date_modified":"0","guid":"82b081ec-3dd3-529c-8475-ab6c344590dd","id":"2","name":"Other bookmarks","type":"folder"},` +
	`"synced":{"children":[],"date_added":"0","date_last_used":"0","date_modified":"0","guid":"4cf2e351-0e85-532b-bb37-df045d8f8d0f","id":"3","name":"Mobile bookmarks","type":"folder"}},"version":1}`

func readDoc(path, empty string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		data = []byte(empty)
	} else if err != nil {
		return nil, err
	}
	v, err := chromejson.Decode(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: not an object", filepath.Base(path))
	}
	return m, nil
}

func syncProfile(ctx context.Context, cfg Config, ps *ProfileState, dir string, clock *crdt.Clock, incoming []crdt.Op, j *journal) ([]crdt.Op, error) {
	byType := map[string][]crdt.Op{}
	for _, op := range incoming {
		byType[op.Type] = append(byType[op.Type], op)
	}
	var out []crdt.Op

	// Settings (also turns on tab restore when asked).
	prefsPath, securePath := filepath.Join(dir, "Preferences"), filepath.Join(dir, "Secure Preferences")
	normal, err := readDoc(prefsPath, `{}`)
	if err != nil {
		return nil, err
	}
	secure, err := readDoc(securePath, `{"protection":{"macs":{}}}`)
	if err != nil {
		return nil, err
	}
	origNormal, origSecure := chromejson.Compact(normal), chromejson.Compact(secure)
	prefs := merge.Prefs{Normal: normal, Secure: secure}
	if cfg.RestoreTabs {
		if v := chromejson.Compact(chromejson.Lookup(secure, "session.restore_on_startup")); v != "1" {
			one, _ := chromejson.Decode([]byte("1"))
			chromejson.Set(secure, "session.restore_on_startup", one)
		}
	}
	if cfg.Off[KindSettings] {
		keep(clock, byType[merge.TypeSetting], func(crdt.Op) *crdt.Map { return ps.Settings })
	} else {
		base, ops, _ := merge.SyncSettings(prefs, ps.SettingsBase, ps.Settings, clock, byType[merge.TypeSetting], cfg.MachineID)
		ps.SettingsBase = base
		out = append(out, ops...)
	}
	if chromejson.Compact(secure) != origSecure || !exists(securePath) {
		// Anything we changed in the protected file must carry a valid MAC
		// for this device, or Helium resets it.
		var extra []string
		for _, p := range merge.ProtectedSettings() {
			if chromejson.Lookup(secure, p) != nil {
				extra = append(extra, p)
			}
		}
		prefhash.Resign(secure, prefhash.Calculator{DeviceID: cfg.MachineID}, true, extra...)
	}
	for _, f := range []struct {
		path string
		doc  map[string]any
		orig string
	}{{prefsPath, normal, origNormal}, {securePath, secure, origSecure}} {
		data := chromejson.Compact(f.doc)
		if data == f.orig && exists(f.path) {
			continue // unchanged: leave Helium's file alone
		}
		if err := noteCreate(cfg, j, f.path); err != nil {
			return nil, err
		}
		if err := closed(cfg); err != nil {
			return nil, err
		}
		if err := writeAtomic(f.path, []byte(data)); err != nil {
			return nil, err
		}
		j.Written = append(j.Written, f.path)
	}

	// Bookmarks.
	done := stepIn(ctx, "bookmarks")
	defer func() { done() }()
	if cfg.Off[KindBookmarks] {
		keep(clock, byType[merge.TypeBookmark], func(crdt.Op) *crdt.Map { return ps.Bookmarks })
	} else {
		bmPath := filepath.Join(dir, "Bookmarks")
		raw, err := os.ReadFile(bmPath)
		if errors.Is(err, fs.ErrNotExist) {
			raw = []byte(emptyBookmarks)
		} else if err != nil {
			return nil, err
		}
		bf, err := bookmarks.Parse(raw)
		if err != nil {
			return nil, err
		}
		bf, bmBase, ops := merge.SyncBookmarks(bf, ps.BookmarksBase, ps.Bookmarks, clock, byType[merge.TypeBookmark])
		ps.BookmarksBase = bmBase
		out = append(out, ops...)
		if enc := bf.Encode(cfg.GOOS); string(enc) != string(raw) || !exists(bmPath) {
			if err := noteCreate(cfg, j, bmPath); err != nil {
				return nil, err
			}
			if err := closed(cfg); err != nil {
				return nil, err
			}
			if err := writeAtomic(bmPath, enc); err != nil {
				return nil, err
			}
			j.Written = append(j.Written, bmPath)
		}
	}

	// Extensions (after settings: both live in Secure Preferences).
	done()
	done = stepIn(ctx, "extensions")
	extOps := append(byType[merge.TypeExtension], byType[merge.TypeExtData]...)
	if cfg.Off[KindExtensions] {
		keep(clock, extOps, func(op crdt.Op) *crdt.Map {
			if op.Type == merge.TypeExtData {
				return ps.ExtData
			}
			return ps.Exts
		})
	} else {
		ops, err := syncExtensions(ctx, cfg, ps, dir, clock, extOps, j)
		if err != nil {
			return nil, err
		}
		out = append(out, ops...)
	}

	// History: only once Helium has created the database for this profile.
	done()
	done = stepIn(ctx, "history")
	histOps := append(byType[merge.TypeVisit], byType[merge.TypeTitle]...)
	if hp := filepath.Join(dir, "History"); exists(hp) && !cfg.Off[KindHistory] {
		if err := closed(cfg); err != nil {
			return nil, err
		}
		db, err := history.Open(hp)
		if err != nil {
			return nil, err
		}
		now := cfg.Now().UnixMicro() + 11644473600*1_000_000
		hb, ops, err := merge.SyncHistory(db, ps.HistoryBase, ps.Visits, ps.Titles, clock, histOps, merge.HistoryCutoff(now))
		db.Close()
		if err != nil {
			return nil, err
		}
		ps.HistoryBase = hb
		out = append(out, ops...)
	} else {
		for _, op := range histOps {
			clock.Observe(op.Stamp)
			if op.Type == merge.TypeVisit {
				ps.Visits.Apply(op)
			} else {
				ps.Titles.Apply(op)
			}
		}
	}

	// Open tabs.
	done()
	done = stepIn(ctx, "tabs")
	tabOps := append(byType[merge.TypeTab], byType[merge.TypeGroup]...)
	if cfg.Off[KindTabs] {
		keep(clock, tabOps, func(op crdt.Op) *crdt.Map {
			if op.Type == merge.TypeGroup {
				return ps.GroupMap
			}
			return ps.TabMap
		})
		return out, nil
	}
	local := &sessions.Session{Groups: map[[2]uint64]*sessions.Group{}}
	if latest, err := sessions.LatestFile(dir); err == nil {
		if s, err := sessions.ReadFile(latest); err == nil {
			local = s
		} else if errors.Is(err, sessions.ErrEncrypted) {
			return nil, err // never overwrite a session we cannot read
		}
	}
	titleOf := func(u string) string {
		var t string
		if v, ok := ps.Titles.Get(u); ok {
			json.Unmarshal(v, &t)
		}
		return t
	}
	projected, ops := merge.SyncTabs(local, ps.Tabs, ps.TabMap, ps.GroupMap, clock, tabOps, titleOf)
	out = append(out, ops...)
	// Only rewrite the session when tabs arrived from another device; this
	// device's own changes are already in Helium's file.
	if len(tabOps) > 0 {
		if err := closed(cfg); err != nil {
			return nil, err
		}
		path, err := sessions.WriteNew(dir, projected)
		if err != nil {
			return nil, err
		}
		j.Created = append(j.Created, path)
		j.Written = append(j.Written, path)
		if err := writeJournal(cfg.StateDir, *j); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// countTypes counts ops per data type (without the profile scope).
func countTypes(ops []crdt.Op) map[string]int {
	m := map[string]int{}
	addTypes(m, ops)
	return m
}

func addTypes(m map[string]int, ops []crdt.Op) {
	for _, op := range ops {
		_, t := splitScope(op.Type)
		m[t]++
	}
}
