package merge

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/imrishabhchauhan/helium-syncer/internal/chromejson"
	"github.com/imrishabhchauhan/helium-syncer/internal/crdt"
	"github.com/imrishabhchauhan/helium-syncer/internal/prefhash"
)

// TypeSetting is the op type for settings; the key is the pref path.
const TypeSetting = "setting"

// SyncedSettings lists the preference paths that travel between devices.
// Everything else (download folder, window sizes, device ids, caches,
// per-site engagement, consents such as Helium services) stays local.
// A path ending in ".*" covers every key under that dictionary.
var SyncedSettings = []string{
	// Startup and home
	"session.restore_on_startup",
	"session.startup_urls",
	"homepage",
	"homepage_is_newtabpage",
	"browser.show_home_button",
	// Toolbar and look (Helium + Chromium)
	"helium.browser.show_back_button",
	"helium.browser.show_reload_button",
	"helium.browser.show_avatar_button",
	"helium.browser.show_extensions_button",
	"helium.browser.show_menu_button",
	"helium.browser.show_media_button",
	"bookmark_bar.show_on_all_tabs",
	"browser.theme.color_scheme2",
	"browser.theme.user_color2",
	"browser.theme.color_variant2",
	"browser.theme.is_grayscale2",
	"toolbar.pinned_actions",
	// Fonts and languages
	"webkit.webprefs.default_font_size",
	"webkit.webprefs.default_fixed_font_size",
	"webkit.webprefs.minimum_font_size",
	"webkit.webprefs.fonts.*",
	"intl.accept_languages",
	"spellcheck.dictionaries",
	"spellcheck.use_spelling_service",
	// Downloads behaviour (not the folder, which is a device path)
	"download.prompt_for_download",
	// Site permission defaults (not per-site exceptions)
	"profile.default_content_setting_values.*",
	// Privacy switches
	"enable_do_not_track",
	"profile.cookie_controls_mode",
	"privacy_sandbox.m1.topics_enabled",
}

// protectedSettings are written to Secure Preferences and need a MAC.
var protectedSettings = map[string]bool{
	"session.restore_on_startup": true,
	"session.startup_urls":       true,
	"homepage":                   true,
	"homepage_is_newtabpage":     true,
	"browser.show_home_button":   true,
}

// Prefs is a profile's two preference documents.
type Prefs struct {
	Normal map[string]any // Preferences
	Secure map[string]any // Secure Preferences
}

func (p Prefs) doc(path string) map[string]any {
	if protectedSettings[path] {
		return p.Secure
	}
	return p.Normal
}

// ExtractSettings reads the synced settings that are set (not default).
func ExtractSettings(p Prefs) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, path := range SyncedSettings {
		if prefix, ok := strings.CutSuffix(path, ".*"); ok {
			for _, doc := range []map[string]any{p.Normal, p.Secure} {
				m, _ := chromejson.Lookup(doc, prefix).(map[string]any)
				for k, v := range m {
					out[prefix+"."+k] = json.RawMessage(chromejson.Compact(v))
				}
			}
			continue
		}
		if v := chromejson.Lookup(p.doc(path), path); v != nil {
			out[path] = json.RawMessage(chromejson.Compact(v))
		}
	}
	return out
}

// SyncSettings runs one sync step for a profile's settings and returns the
// paths it changed. Protected settings are re-signed for deviceID, with the
// encrypted hashes dropped (Helium rebuilds them; verified in phase 0).
func SyncSettings(p Prefs, base map[string]json.RawMessage, state *crdt.Map, clock *crdt.Clock,
	incoming []crdt.Op, deviceID string) (map[string]json.RawMessage, []crdt.Op, []string) {
	cur := ExtractSettings(p)
	out := crdt.Diff(TypeSetting, base, cur, clock)
	for _, op := range incoming {
		clock.Observe(op.Stamp)
		state.Apply(op)
	}
	for _, op := range out {
		state.Apply(op)
	}
	var changed []string
	resign := false
	var keys []string
	for k := range state.Entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, path := range keys {
		if !allowedSetting(path) {
			continue // never write a path outside the allowlist, whatever arrives
		}
		e := state.Entries[path]
		local, here := cur[path]
		switch {
		case e.Deleted && here:
			removePath(p.doc(path), path)
		case !e.Deleted && (!here || string(local) != string(e.Value)):
			v, err := chromejson.Decode(e.Value)
			if err != nil {
				continue
			}
			chromejson.Set(p.doc(path), path, v)
		default:
			continue
		}
		changed = append(changed, path)
		if protectedSettings[path] {
			resign = true
		}
	}
	if resign {
		var extra []string
		for path := range protectedSettings {
			if chromejson.Lookup(p.Secure, path) != nil {
				extra = append(extra, path)
			}
		}
		prefhash.Resign(p.Secure, prefhash.Calculator{DeviceID: deviceID}, true, extra...)
	}
	return ExtractSettings(p), out, changed
}

func allowedSetting(path string) bool {
	for _, s := range SyncedSettings {
		if prefix, ok := strings.CutSuffix(s, ".*"); ok {
			if rest, ok := strings.CutPrefix(path, prefix+"."); ok && rest != "" && !strings.Contains(rest, ".") {
				return true
			}
		} else if s == path {
			return true
		}
	}
	return false
}

func removePath(doc map[string]any, path string) {
	parts := strings.Split(path, ".")
	cur := doc
	for _, part := range parts[:len(parts)-1] {
		next, ok := cur[part].(map[string]any)
		if !ok {
			return
		}
		cur = next
	}
	delete(cur, parts[len(parts)-1])
}
