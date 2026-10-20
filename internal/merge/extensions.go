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
	TypeExtData   = "ext-data" // key: extension id, value: hash of its data
)

// ExtItem is the synced form of an installed Web Store extension.
type ExtItem struct {
	Name    string `json:"n"`
	Version string `json:"v"`
	Enabled bool   `json:"e"`
}

// ExtPlan is what the engine must do on this device after merging.
type ExtPlan struct {
	Install  []string // ids to install from their synced package (missing here, or older here)
	Remove   []string // ids uninstalled on another device
	Enable   []string
	Disable  []string
	LoadData []string // ids whose data changed on another device
}

// ExtractExtensions lists syncable extensions in a profile. dataHash gives
// the current hash of an extension's data folders ("" if none).
func ExtractExtensions(profile string, dataHash func(id string) string) (exts, data map[string]json.RawMessage, err error) {
	list, err := extensions.List(profile)
	if err != nil {
		return nil, nil, err
	}
	exts, data = map[string]json.RawMessage{}, map[string]json.RawMessage{}
	for _, e := range list {
		if !e.Syncable() {
			continue
		}
		raw, _ := json.Marshal(ExtItem{Name: e.Name, Version: e.Version, Enabled: !e.Disabled})
		exts[e.ID] = raw
		if h := dataHash(e.ID); h != "" {
			hb, _ := json.Marshal(h)
			data[e.ID] = hb
		}
	}
	return exts, data, nil
}

// PlanExtensions merges extension ops and returns what to change locally.
// cur is this device's current snapshot, taken by the caller with
// ExtractExtensions (the same one whose diff produced the local ops).
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
	ids := map[string]bool{}
	for k := range exts.Entries {
		ids[k] = true
	}
	var sorted []string
	for k := range ids {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, id := range sorted {
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
		case local.Enabled != want.Enabled && want.Enabled:
			p.Enable = append(p.Enable, id)
		case local.Enabled != want.Enabled:
			p.Disable = append(p.Disable, id)
		}
		if d, ok := data.Get(id); ok && string(d) != string(curData[id]) {
			p.LoadData = append(p.LoadData, id)
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
