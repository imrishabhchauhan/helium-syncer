package chromejson

import "testing"

// esc builds a JSON backslash-u escape without writing one literally in this
// file (some editors and tools silently decode them).
func esc(hex string) string { return `\` + "u" + hex }

func TestCompactMatchesChromium(t *testing.T) {
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
	if got := Compact(v); got != want {
		t.Errorf("Compact\n got: %s\nwant: %s", got, want)
	}
}

func TestCleanDropsEmptyContainers(t *testing.T) {
	v, _ := Decode([]byte(`{"a":{},"b":[],"c":{"d":[{}],"e":1},"l":[[],{"x":{}}]}`))
	if got, want := Compact(Clean(v)), `{"c":{"e":1}}`; got != want {
		t.Errorf("got %s want %s", got, want)
	}
}

func TestPrettyMatchesChromium(t *testing.T) {
	v, _ := Decode([]byte(`{"b":[1,{"x":true}],"a":{},"e":[]}`))
	want := "{\n   \"a\": {\n   },\n   \"b\": [ 1, {\n      \"x\": true\n   } ],\n   \"e\": [  ]\n}\n"
	if got := Pretty(v, "\n"); got != want {
		t.Errorf("Pretty\n got: %q\nwant: %q", got, want)
	}
}
