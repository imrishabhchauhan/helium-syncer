// Package engine runs one sync for one Helium user data folder: it pulls
// changes from the other devices, merges them with this device's own
// changes, writes the result into Helium's files (only while Helium is
// closed, with a backup first), and sends this device's changes out.
package engine

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/imrishabhchauhan/helium-syncer/internal/crdt"
	"github.com/imrishabhchauhan/helium-syncer/internal/merge"
)

// State is everything this device remembers between syncs. It lives in
// Helium Syncer's own folder, never inside Helium's.
type State struct {
	DeviceID string     `json:"device_id"`
	Clock    crdt.Stamp `json:"clock"`
	Cursor   string     `json:"cursor"`

	Links merge.ProfileLinks `json:"links"`
	// Local-only name endings, e.g. "Profile 2" -> " (2)" (see SyncProfiles).
	ProfileSuffix map[string]string          `json:"profile_suffix,omitempty"`
	ProfilesBase  map[string]json.RawMessage `json:"profiles_base"`
	ProfilesMap   *crdt.Map                  `json:"profiles_map"`

	Profiles map[string]*ProfileState `json:"profiles"` // by synced profile id

	// Ops made here but not yet accepted by the server. They are sent first
	// on the next run, so a network failure never loses a change.
	Outbox []crdt.Op `json:"outbox"`
}

// ProfileState is the per-profile part of State.
type ProfileState struct {
	BookmarksBase map[string]json.RawMessage `json:"bookmarks_base"`
	Bookmarks     *crdt.Map                  `json:"bookmarks"`

	HistoryBase merge.History `json:"history_base"`
	Visits      *crdt.Map     `json:"visits"`
	Titles      *crdt.Map     `json:"titles"`

	Tabs     *merge.TabSync `json:"tabs"`
	TabMap   *crdt.Map      `json:"tab_map"`
	GroupMap *crdt.Map      `json:"group_map"`

	SettingsBase map[string]json.RawMessage `json:"settings_base"`
	Settings     *crdt.Map                  `json:"settings"`

	ExtBase     map[string]json.RawMessage `json:"ext_base"`
	ExtDataBase map[string]json.RawMessage `json:"ext_data_base"`
	Exts        *crdt.Map                  `json:"exts"`
	ExtData     *crdt.Map                  `json:"ext_data"`
	// Blob ids already uploaded or installed: "id@version" -> package blob,
	// id -> latest data blob. They keep uploads to one per version/change.
	ExtBlobs  map[string]string        `json:"ext_blobs"`
	DataBlobs map[string]merge.ExtData `json:"data_blobs"`
}

func newProfileState() *ProfileState {
	return &ProfileState{
		BookmarksBase: map[string]json.RawMessage{}, Bookmarks: crdt.NewMap(),
		HistoryBase: merge.History{Visits: map[string]json.RawMessage{}, Titles: map[string]json.RawMessage{}},
		Visits:      crdt.NewMap(), Titles: crdt.NewMap(),
		Tabs: merge.NewTabSync(), TabMap: crdt.NewMap(), GroupMap: crdt.NewMap(),
		SettingsBase: map[string]json.RawMessage{}, Settings: crdt.NewMap(),
		ExtBase: map[string]json.RawMessage{}, ExtDataBase: map[string]json.RawMessage{},
		Exts: crdt.NewMap(), ExtData: crdt.NewMap(),
		ExtBlobs: map[string]string{}, DataBlobs: map[string]merge.ExtData{},
	}
}

func newState(deviceID string) *State {
	return &State{DeviceID: deviceID, Links: merge.ProfileLinks{}, ProfilesBase: map[string]json.RawMessage{},
		ProfilesMap: crdt.NewMap(), Profiles: map[string]*ProfileState{}}
}

func (s *State) profile(id string) *ProfileState {
	p := s.Profiles[id]
	if p == nil {
		p = newProfileState()
		s.Profiles[id] = p
	}
	return p
}

const (
	stateFile   = "state.json"
	journalFile = "journal.json"
)

// LinkProfile marks a local profile folder for syncing (the user's choice
// on the setup screen) and returns its synced id.
func LinkProfile(stateDir, deviceID, dir string) (string, error) {
	st, err := LoadState(stateDir, deviceID)
	if err != nil {
		return "", err
	}
	if id, ok := st.Links[dir]; ok {
		return id, nil
	}
	id := st.Links.LinkNew(dir)
	return id, st.Save(stateDir)
}

// UnlinkProfile stops syncing a local profile folder (its data stays).
func UnlinkProfile(stateDir, deviceID, dir string) error {
	st, err := LoadState(stateDir, deviceID)
	if err != nil {
		return err
	}
	delete(st.Links, dir)
	return st.Save(stateDir)
}

// Links returns the linked profile folders and their synced ids.
func Links(stateDir, deviceID string) (map[string]string, error) {
	st, err := LoadState(stateDir, deviceID)
	if err != nil {
		return nil, err
	}
	return st.Links, nil
}

// Pending returns how many changes wait to be sent.
func Pending(stateDir, deviceID string) (int, error) {
	st, err := LoadState(stateDir, deviceID)
	if err != nil {
		return 0, err
	}
	return len(st.Outbox), nil
}

// LoadState reads the state, or returns a fresh one for deviceID.
func LoadState(dir, deviceID string) (*State, error) {
	data, err := os.ReadFile(filepath.Join(dir, stateFile))
	if errors.Is(err, fs.ErrNotExist) {
		return newState(deviceID), nil
	}
	if err != nil {
		return nil, err
	}
	s := newState(deviceID)
	if err := json.Unmarshal(data, s); err != nil {
		return nil, err
	}
	// State saved by an older version may lack newer fields.
	for id, p := range s.Profiles {
		fresh := newProfileState()
		if p == nil {
			s.Profiles[id] = fresh
			continue
		}
		p.fill(fresh)
	}
	return s, nil
}

// Save writes the state atomically.
func (s *State) Save(dir string) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, stateFile), data)
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".hs-tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// fill gives nil fields of p the empty values from fresh.
func (p *ProfileState) fill(fresh *ProfileState) {
	if p.BookmarksBase == nil {
		p.BookmarksBase = fresh.BookmarksBase
	}
	if p.Bookmarks == nil {
		p.Bookmarks = fresh.Bookmarks
	}
	if p.HistoryBase.Visits == nil {
		p.HistoryBase = fresh.HistoryBase
	}
	if p.Visits == nil {
		p.Visits = fresh.Visits
	}
	if p.Titles == nil {
		p.Titles = fresh.Titles
	}
	if p.Tabs == nil {
		p.Tabs = fresh.Tabs
	}
	if p.TabMap == nil {
		p.TabMap = fresh.TabMap
	}
	if p.GroupMap == nil {
		p.GroupMap = fresh.GroupMap
	}
	if p.SettingsBase == nil {
		p.SettingsBase = fresh.SettingsBase
	}
	if p.Settings == nil {
		p.Settings = fresh.Settings
	}
	if p.ExtBase == nil {
		p.ExtBase = fresh.ExtBase
	}
	if p.ExtDataBase == nil {
		p.ExtDataBase = fresh.ExtDataBase
	}
	if p.Exts == nil {
		p.Exts = fresh.Exts
	}
	if p.ExtData == nil {
		p.ExtData = fresh.ExtData
	}
	if p.ExtBlobs == nil {
		p.ExtBlobs = fresh.ExtBlobs
	}
	if p.DataBlobs == nil {
		p.DataBlobs = fresh.DataBlobs
	}
}
