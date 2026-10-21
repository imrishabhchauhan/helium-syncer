package merge

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/imrishabhchauhan/helium-syncer/internal/crdt"
	"github.com/imrishabhchauhan/helium-syncer/internal/profiles"
)

// TypeProfile is the op type for profiles; the key is a synced profile id.
const TypeProfile = "profile"

// ProfileItem is the synced form of a profile.
type ProfileItem struct {
	Name   string `json:"n"`
	Avatar int    `json:"a,omitempty"`
}

// ProfileLinks maps this device's profile folders to synced profile ids.
// Only linked profiles sync; the user picks which local profiles to link.
type ProfileLinks map[string]string // folder -> synced id

// LinkNew gives a local profile folder a fresh synced id.
func (l ProfileLinks) LinkNew(dir string) string {
	id := uuid.NewString()
	l[dir] = id
	return id
}

// ProfilePlan says what SyncProfiles did on this device.
type ProfilePlan struct {
	Added    map[string]string // new folder -> synced id
	Renamed  []string          // folders renamed
	Unlinked []string          // folders whose profile was removed elsewhere: kept, no longer synced
}

// SyncProfiles runs one sync step for the list of profiles. Profiles are
// never deleted: a profile removed on another device stays here and just
// stops syncing; profiles that exist elsewhere are added next to the local
// ones (never merged into them).
//
// suffix holds a local-only ending per profile folder, e.g. " (2)": when an
// added profile has the same name as a profile here, it is shown as
// "Personal (2)" on this computer only. The synced name stays "Personal".
func SyncProfiles(userData string, links ProfileLinks, suffix map[string]string, base map[string]json.RawMessage, state *crdt.Map, clock *crdt.Clock,
	incoming []crdt.Op, avatarFor func(i int) int) (map[string]json.RawMessage, []crdt.Op, ProfilePlan, error) {
	plan := ProfilePlan{Added: map[string]string{}}
	if suffix == nil {
		suffix = map[string]string{}
	}
	cur, err := extractProfiles(userData, links, suffix)
	if err != nil {
		return nil, nil, plan, err
	}
	out := crdt.Diff(TypeProfile, base, cur, clock)
	for _, op := range incoming {
		clock.Observe(op.Stamp)
		state.Apply(op)
	}
	for _, op := range out {
		state.Apply(op)
	}
	byID := map[string]string{}
	for dir, id := range links {
		byID[id] = dir
	}
	var ids []string
	for id := range state.Entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for i, id := range ids {
		e := state.Entries[id]
		dir, linked := byID[id]
		if e.Deleted {
			if linked {
				delete(links, dir)
				plan.Unlinked = append(plan.Unlinked, dir)
			}
			continue
		}
		var p ProfileItem
		if json.Unmarshal(e.Value, &p) != nil {
			continue
		}
		if !linked {
			end, err := freeSuffix(userData, p.Name, "")
			if err != nil {
				return nil, nil, plan, err
			}
			newDir, err := profiles.Add(userData, p.Name+end, avatarFor(i))
			if err != nil {
				return nil, nil, plan, err
			}
			links[newDir] = id
			if end != "" {
				suffix[newDir] = end
			}
			plan.Added[newDir] = id
			continue
		}
		if raw, ok := cur[id]; ok {
			var local ProfileItem
			json.Unmarshal(raw, &local)
			if local.Name != p.Name {
				end, err := freeSuffix(userData, p.Name, dir)
				if err != nil {
					return nil, nil, plan, err
				}
				if err := profiles.Rename(userData, dir, p.Name+end); err != nil {
					return nil, nil, plan, err
				}
				if end == "" {
					delete(suffix, dir)
				} else {
					suffix[dir] = end
				}
				plan.Renamed = append(plan.Renamed, dir)
			}
		}
	}
	newBase, err := extractProfiles(userData, links, suffix)
	return newBase, out, plan, err
}

// freeSuffix returns "" when no other profile here (skip is the profile
// itself) is called name, else the first free " (n)" ending.
func freeSuffix(userData, name, skip string) (string, error) {
	list, err := profiles.List(userData)
	if err != nil {
		return "", err
	}
	used := map[string]bool{}
	for _, p := range list {
		if p.Dir != skip {
			used[strings.ToLower(p.Name)] = true
		}
	}
	if !used[strings.ToLower(name)] {
		return "", nil
	}
	for n := 2; ; n++ {
		end := fmt.Sprintf(" (%d)", n)
		if !used[strings.ToLower(name+end)] {
			return end, nil
		}
	}
}

func extractProfiles(userData string, links ProfileLinks, suffix map[string]string) (map[string]json.RawMessage, error) {
	list, err := profiles.List(userData)
	if err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	present := map[string]bool{}
	for _, p := range list {
		present[p.Dir] = true
		if id, ok := links[p.Dir]; ok {
			name := p.Name
			if end := suffix[p.Dir]; end != "" {
				if trimmed, had := strings.CutSuffix(name, end); had {
					name = trimmed
				} else {
					delete(suffix, p.Dir) // the person renamed it here: their name syncs
				}
			}
			raw, _ := json.Marshal(ProfileItem{Name: name})
			out[id] = raw
		}
	}
	// A linked folder the user deleted in Helium: drop the link; the missing
	// item becomes a tombstone so other devices stop syncing it (and keep it).
	for dir := range links {
		if !present[dir] {
			delete(links, dir)
			delete(suffix, dir)
		}
	}
	return out, nil
}
