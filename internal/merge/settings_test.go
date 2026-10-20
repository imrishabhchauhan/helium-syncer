package merge

import (
	"encoding/json"
	"testing"

	"github.com/imrishabhchauhan/helium-syncer/internal/chromejson"
	"github.com/imrishabhchauhan/helium-syncer/internal/crdt"
	"github.com/imrishabhchauhan/helium-syncer/internal/prefhash"
)

func prefsFrom(t *testing.T, normal, secure string) Prefs {
	n, err := chromejson.Decode([]byte(normal))
	if err != nil {
		t.Fatal(err)
	}
	s, err := chromejson.Decode([]byte(secure))
	if err != nil {
		t.Fatal(err)
	}
	return Prefs{Normal: n.(map[string]any), Secure: s.(map[string]any)}
}

func TestSettingsSyncAllowlistOnly(t *testing.T) {
	s := &server{}
	mac := prefsFrom(t, `{"download":{"default_directory":"/Users/r/Downloads","prompt_for_download":true},
		"helium":{"browser":{"show_avatar_button":true}},"webkit":{"webprefs":{"fonts":{"standard":{"Zyyy":"Inter"}}}}}`,
		`{"session":{"restore_on_startup":1},"protection":{"macs":{}}}`)
	win := prefsFrom(t, `{"download":{"default_directory":"C:\\Users\\r\\Downloads"}}`, `{"protection":{"macs":{}}}`)
	macBase, winBase := map[string]json.RawMessage{}, map[string]json.RawMessage{}
	macState, winState := crdt.NewMap(), crdt.NewMap()
	macClock, winClock := crdt.NewClock("mac", nil), crdt.NewClock("win", nil)

	var out []crdt.Op
	macBase, out, _ = SyncSettings(mac, macBase, macState, macClock, nil, "MAC-UUID")
	s.log = append(s.log, out...)
	var changed []string
	winBase, out, changed = SyncSettings(win, winBase, winState, winClock, s.log, "S-1-5-21-1-2-3")
	s.log = append(s.log, out...)
	t.Logf("windows changed: %v", changed)

	if got := chromejson.Lookup(win.Normal, "download.default_directory"); got != `C:\Users\r\Downloads` {
		t.Errorf("download folder must stay local, got %v", got)
	}
	if got := chromejson.Lookup(win.Normal, "download.prompt_for_download"); got != true {
		t.Errorf("prompt_for_download should sync, got %v", got)
	}
	if got := chromejson.Lookup(win.Normal, "helium.browser.show_avatar_button"); got != true {
		t.Errorf("Helium toolbar setting should sync, got %v", got)
	}
	if got := chromejson.Lookup(win.Normal, "webkit.webprefs.fonts.standard"); got == nil {
		t.Errorf("font settings should sync")
	}
	if got := chromejson.Compact(chromejson.Lookup(win.Secure, "session.restore_on_startup")); got != "1" {
		t.Errorf("restore_on_startup should sync into Secure Preferences, got %v", got)
	}
	// Protected setting must be signed for the Windows device id.
	calc := prefhash.Calculator{DeviceID: "S-1-5-21-1-2-3"}
	want := calc.MAC("session.restore_on_startup", prefhash.ValueString(chromejson.Lookup(win.Secure, "session.restore_on_startup")))
	if got := chromejson.Lookup(win.Secure, "protection.macs.session.restore_on_startup"); got != want {
		t.Errorf("restore_on_startup MAC = %v, want %v", got, want)
	}
	// Another sync on both: no ping-pong.
	_, out, _ = SyncSettings(mac, macBase, macState, macClock, s.log[2:], "MAC-UUID")
	_, out2, _ := SyncSettings(win, winBase, winState, winClock, nil, "S-1-5-21-1-2-3")
	if len(out)+len(out2) != 0 {
		t.Errorf("ping-pong: %d + %d ops", len(out), len(out2))
	}
}

// An op for a path outside the allowlist (e.g. from a buggy or malicious
// client) must never be written.
func TestSettingsRejectUnknownPaths(t *testing.T) {
	p := prefsFrom(t, `{}`, `{"protection":{"macs":{}}}`)
	evil := []crdt.Op{
		{Type: TypeSetting, Key: "download.default_directory", Value: json.RawMessage(`"C:\\evil"`), Stamp: crdt.Stamp{Wall: 1, Device: "x"}},
		{Type: TypeSetting, Key: "extensions.settings.abc", Value: json.RawMessage(`{}`), Stamp: crdt.Stamp{Wall: 1, Device: "x"}},
		{Type: TypeSetting, Key: "profile.default_content_setting_values.a.b", Value: json.RawMessage(`1`), Stamp: crdt.Stamp{Wall: 1, Device: "x"}},
	}
	SyncSettings(p, map[string]json.RawMessage{}, crdt.NewMap(), crdt.NewClock("w", nil), evil, "dev")
	if len(p.Normal) != 0 {
		t.Errorf("unknown paths were written: %s", chromejson.Compact(p.Normal))
	}
}
