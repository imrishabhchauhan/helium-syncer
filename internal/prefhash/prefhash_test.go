package prefhash

import (
	"strings"
	"testing"

	"github.com/imrishabhchauhan/helium-syncer/internal/chromejson"
)

func TestValueString(t *testing.T) {
	v, _ := chromejson.Decode([]byte(`{"a":{},"c":{"e":1}}`))
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
