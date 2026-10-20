package prefhash

import (
	"strings"
	"testing"
)

// esc builds a JSON backslash-u escape without writing one literally in this
// file (some editors and tools silently decode them).
func esc(hex string) string { return `\` + "u" + hex }

func TestWriteJSONMatchesChromium(t *testing.T) {
	in := `{"a":["` + esc("003C") + `all_urls>"],"d":1792511605.21412,"f":2.0,"i":5,` +
		`"big":13436985145181507,"tiny":1e-9,"huge":1e22,"neg":-0.5,` +
		`"s":"x` + esc("2028") + `y` + esc("0001") + `z\n\"q\\","utf":"हिंदी 😀"}`
	v, err := Decode([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":["` + esc("003C") + `all_urls>"],"big":13436985145181508.0,"d":1792511605.21412,` +
		`"f":2.0,"huge":1e+22,"i":5,"neg":-0.5,"s":"x` + esc("2028") + `y` + esc("0001") + `z\n\"q\\",` +
		`"tiny":1e-9,"utf":"हिंदी 😀"}`
	if got := WriteJSON(v); got != want {
		t.Errorf("WriteJSON\n got: %s\nwant: %s", got, want)
	}
}

func TestValueStringDropsEmptyContainers(t *testing.T) {
	v, _ := Decode([]byte(`{"a":{},"b":[],"c":{"d":[{}],"e":1},"l":[[],{"x":{}}]}`))
	if got, want := ValueString(v), `{"c":{"e":1}}`; got != want {
		t.Errorf("got %s want %s", got, want)
	}
	if ValueString(nil) != "" {
		t.Error("nil must serialise to empty string")
	}
}

func TestMACShape(t *testing.T) {
	c := Calculator{DeviceID: "S-1-5-21-1-2-3"}
	got := c.MAC("homepage", `"https://example.com"`)
	if len(got) != 64 || strings.ToUpper(got) != got {
		t.Fatalf("MAC must be 64 upper-case hex chars, got %q", got)
	}
	if got == (Calculator{DeviceID: "other"}).MAC("homepage", `"https://example.com"`) {
		t.Error("device id must change the MAC")
	}
}
