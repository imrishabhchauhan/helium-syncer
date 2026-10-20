package merge

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/imrishabhchauhan/helium-syncer/internal/crdt"
)

func ext(name, version string, enabled bool) json.RawMessage {
	b, _ := json.Marshal(ExtItem{Name: name, Version: version, Enabled: enabled, Package: "pkg-" + version})
	return b
}

func extData(hash, blob string) json.RawMessage {
	b, _ := json.Marshal(ExtData{Hash: hash, Blob: blob})
	return b
}

func TestPlanExtensions(t *testing.T) {
	clock := crdt.NewClock("win", nil)
	exts, data := crdt.NewMap(), crdt.NewMap()
	st := func(w int64) crdt.Stamp { return crdt.Stamp{Wall: w, Device: "mac"} }
	incoming := []crdt.Op{
		{Type: TypeExtension, Key: "dark", Value: ext("Dark Reader", "4.9.133", true), Stamp: st(1)},
		{Type: TypeExtension, Key: "ublock", Value: ext("uBO Lite", "2.0", true), Stamp: st(2)},
		{Type: TypeExtension, Key: "old", Deleted: true, Stamp: st(3)},
		{Type: TypeExtension, Key: "off", Value: ext("Off", "1.0", false), Stamp: st(4)},
		{Type: TypeExtData, Key: "dark", Value: extData("hash-from-mac", "blob-a"), Stamp: st(5)},
		// Same data content uploaded separately: different blob, same hash.
		{Type: TypeExtData, Key: "off", Value: extData("same", "blob-mac"), Stamp: st(6)},
	}
	cur := map[string]json.RawMessage{
		"ublock": ext("uBO Lite", "1.9", true), // older here
		"old":    ext("Old", "1.0", true),      // removed on the Mac
		"off":    ext("Off", "1.0", true),      // disabled on the Mac
		"mine":   ext("Only here", "1.0", true),
	}
	curData := map[string]json.RawMessage{"off": extData("same", "blob-win")}
	p := PlanExtensions(cur, curData, exts, data, clock, incoming, nil)
	want := ExtPlan{Install: []string{"dark", "ublock"}, Remove: []string{"old"}, Disable: []string{"off"}, LoadData: []string{"dark"}}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("plan %+v\nwant %+v", p, want)
	}
}

func TestNewerVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{{"4.9.133", "4.9.99", true}, {"1.10", "1.9", true}, {"1.0", "1.0.0", false}, {"2", "10", false}}
	for _, c := range cases {
		if got := newerVersion(c.a, c.b); got != c.want {
			t.Errorf("newerVersion(%s, %s) = %v", c.a, c.b, got)
		}
	}
}
