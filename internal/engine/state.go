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

	Links        merge.ProfileLinks         `json:"links"`
	ProfilesBase map[string]json.RawMessage `json:"profiles_base"`
	ProfilesMap  *crdt.Map                  `json:"profiles_map"`

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
}

func newProfileState() *ProfileState {
	return &ProfileState{
		BookmarksBase: map[string]json.RawMessage{}, Bookmarks: crdt.NewMap(),
		HistoryBase: merge.History{Visits: map[string]json.RawMessage{}, Titles: map[string]json.RawMessage{}},
		Visits:      crdt.NewMap(), Titles: crdt.NewMap(),
		Tabs: merge.NewTabSync(), TabMap: crdt.NewMap(), GroupMap: crdt.NewMap(),
		SettingsBase: map[string]json.RawMessage{}, Settings: crdt.NewMap(),
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
