package prefhash

import (
	"testing"

	"github.com/imrishabhchauhan/helium-syncer/internal/chromejson"
)

func signed(t *testing.T, device string) map[string]any {
	t.Helper()
	v, err := chromejson.Decode([]byte(`{"homepage":"https://a.example/","extensions":{"settings":{"abc":{"path":"x"}}},` +
		`"protection":{"macs":{"homepage":"","extensions":{"settings":{"abc":""},"settings_encrypted_hash":{"abc":"Zm9v"}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	doc := v.(map[string]any)
	Resign(doc, Calculator{DeviceID: device}, false)
	return doc
}

// A file signed the way Helium signs verifies; a changed value, a file from
// another computer, or a changed signing scheme does not.
func TestVerify(t *testing.T) {
	c := Calculator{DeviceID: "S-1-5-21-1-2-3"}
	if !Verify(signed(t, c.DeviceID), c) {
		t.Fatal("a correctly signed file does not verify")
	}
	doc := signed(t, c.DeviceID)
	chromejson.Set(doc, "homepage", "https://evil.example/")
	if Verify(doc, c) {
		t.Fatal("a changed value verified")
	}
	if Verify(signed(t, "S-1-5-21-9-9-9"), c) {
		t.Fatal("a file signed for another computer verified")
	}
	if Verify(signed(t, c.DeviceID), Calculator{DeviceID: c.DeviceID, Seed: []byte("new scheme")}) {
		t.Fatal("a different signing scheme verified")
	}
	if !Verify(map[string]any{"homepage": "x"}, c) {
		t.Fatal("an unprotected file should verify (nothing to check)")
	}
}
