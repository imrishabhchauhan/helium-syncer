package merge

import (
	"encoding/json"
	"sort"

	"github.com/imrishabhchauhan/helium-syncer/internal/crdt"
	"github.com/imrishabhchauhan/helium-syncer/internal/extensions"
)

// Op types for extensions.
const (
	TypeExtension = "ext"      // key: extension id
	TypeExtData   = "ext-data" // key: extension id
)

// ExtItem is the synced form of an installed Web Store extension.
type ExtItem struct {
	Name    string `json:"n"`
	Version string `json:"v"`
	Enabled bool   `json:"e"`
	Package string `json:"p,omitempty"` // encrypted blob with the files + registration
}

// ExtData points at the latest data of an extension.
type ExtData struct {
	Hash string `json:"h"` // fingerprint of the data (same content = same hash)
	Blob string `json:"b"` // encrypted blob with the data folders
}

// ExtPlan is what the engine must do on this device after merging.
type ExtPlan struct {
	Install  []string // ids to install from their synced package (missing here, or older here)
	Remove   []string // ids uninstalled on another device
	Enable   []string
	Disable  []string
	LoadData []string // ids whose data changed on another device
}

// ExtractExtensions lists syncable extensions in a profile. pkg returns the
// blob id of an extension version's package (uploading it if needed); data
// returns the extension's current data reference (zero if it has none).
func ExtractExtensions(profile string, pkg func(id, version string) (string, error),
	data func(id string) (ExtData, error)) (exts, datas map[string]json.RawMessage, err error) {
	list, err := extensions.List(profile)
	if err != nil {
		return nil, nil, err
	}
	exts, datas = map[string]json.RawMessage{}, map[string]json.RawMessage{}
	for _, e := range list {
		if !e.Syncable() {
			continue
		}
		p, err := pkg(e.ID, e.Version)
		if err != nil {
			return nil, nil, err
		}
		raw, _ := json.Marshal(ExtItem{Name: e.Name, Version: e.Version, Enabled: !e.Disabled, Package: p})
		exts[e.ID] = raw
		d, err := data(e.ID)
		if err != nil {
			return nil, nil, err
		}
		if d.Hash != "" {
			raw, _ := json.Marshal(d)
			datas[e.ID] = raw
		}
	}
	return exts, datas, nil
}

// PlanExtensions merges extension ops and returns what to change locally.
// curExts/curData are this device's current snapshot (the one whose diff
// produced the local ops).
func PlanExtensions(curExts, curData map[string]json.RawMessage, exts, data *crdt.Map, clock *crdt.Clock,
	incoming, local []crdt.Op) ExtPlan {
	for _, op := range incoming {
		clock.Observe(op.Stamp)
	}
	for _, op := range append(append([]crdt.Op(nil), incoming...), local...) {
		switch op.Type {
		case TypeExtension:
			exts.Apply(op)
		case TypeExtData:
			data.Apply(op)
		}
	}
	var p ExtPlan
	var ids []string
	for k := range exts.Entries {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	for _, id := range ids {
		e := exts.Entries[id]
		raw, here := curExts[id]
		var local ExtItem
		if here {
			json.Unmarshal(raw, &local)
		}
		if e.Deleted {
			if here {
				p.Remove = append(p.Remove, id)
			}
			continue
		}
		var want ExtItem
		if json.Unmarshal(e.Value, &want) != nil {
			continue
		}
		switch {
		case !here || (local.Version != want.Version && newerVersion(want.Version, local.Version)):
			p.Install = append(p.Install, id)
			if !want.Enabled {
				p.Disable = append(p.Disable, id)
			}
		case local.Enabled != want.Enabled && want.Enabled:
			p.Enable = append(p.Enable, id)
		case local.Enabled != want.Enabled:
			p.Disable = append(p.Disable, id)
		}
		if raw, ok := data.Get(id); ok {
			var want, have ExtData
			json.Unmarshal(raw, &want)
			if cur, ok := curData[id]; ok {
				json.Unmarshal(cur, &have)
			}
			if want.Hash != "" && want.Hash != have.Hash {
				p.LoadData = append(p.LoadData, id)
			}
		}
	}
	return p
}

// newerVersion compares dotted extension versions numerically.
func newerVersion(a, b string) bool {
	pa, pb := splitVersion(a), splitVersion(b)
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func splitVersion(v string) []int {
	var out []int
	n := 0
	for _, c := range v + "." {
		if c == '.' {
			out = append(out, n)
			n = 0
		} else if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return out
}
