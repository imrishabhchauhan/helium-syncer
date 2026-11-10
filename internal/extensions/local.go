package extensions

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/imrishabhchauhan/helium-syncer/internal/chromejson"
)

// InstallLocal registers an extension that lives in a folder on this
// computer, the way Helium registers the ad blocker it ships with: the files
// stay where they are and the profile points at them. It is how Helium
// Syncer installs its own companion extension, so nobody has to find the
// browser's extensions page and turn on developer mode.
//
// Helium must not be running. `dir` must hold a manifest.json whose `key`
// pins the extension to `id`.
func InstallLocal(profile, id, dir, deviceID, goos string) error {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(dir) == "" {
		return errors.New("extensions: install needs an id and a folder")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(abs, "manifest.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("extensions: no manifest.json in %s", abs)
	} else if err != nil {
		return err
	}
	// Decode the Chromium way: it keeps whole numbers as integers, and the
	// signature over Secure Preferences is computed on that exact spelling.
	v, err := chromejson.Decode(raw)
	if err != nil {
		return fmt.Errorf("extensions: manifest.json in %s is not valid JSON: %w", abs, err)
	}
	manifest, _ := v.(map[string]any)
	if manifest == nil {
		return fmt.Errorf("extensions: manifest.json in %s is not an object", abs)
	}

	// This mirrors, field for field, the entry Helium itself writes when it
	// loads an extension from a folder (checked on Helium 0.19.4.1). Two
	// fields decide whether it runs, and both were learned the hard way:
	//
	//   granted_permissions   without it Helium treats the permissions as
	//                         never approved and keeps the extension off.
	//   (no "manifest")       Helium reads a folder extension's manifest from
	//                         the folder; it stores one only for packed ones.
	now := chromeNow()
	entry := map[string]any{
		"account_extension_type": json.Number("0"),
		"active_permissions":     permissions(manifest),
		"commands":               map[string]any{},
		"content_settings":       []any{},
		// What Helium writes for an extension loaded from a folder.
		"creation_flags":             json.Number("38"),
		"disable_reasons":            []any{},
		"first_install_time":         now,
		"from_webstore":              false,
		"granted_permissions":        permissions(manifest),
		"incognito_content_settings": []any{},
		"incognito_preferences":      map[string]any{},
		"last_update_time":           now,
		// 4 is an extension loaded from a folder on this computer. 5 would
		// mean a part of the browser itself, which Helium owns: it removes
		// anything claiming that which its own build does not know about.
		// 8 (loaded with --load-extension) is forgotten on the next start.
		"location":                 json.Number(fmt.Sprint(LocationUnpacked)),
		"path":                     pathFor(abs, goos),
		"preferences":              map[string]any{},
		"regular_only_preferences": map[string]any{},
		"was_installed_by_default": false,
		"was_installed_by_oem":     false,
		"withholding_permissions":  false,
	}
	return updateSecure(profile, deviceID, func(doc map[string]any) {
		setPath(doc, "extensions.settings."+id, entry)
		// Helium switches off any folder extension while developer mode is
		// off (disable reason 1<<24). The setting is protected: its value
		// lives here in Secure Preferences and must be signed, or Helium
		// resets it on start.
		setPath(doc, DeveloperModePath, true)
	}, "extensions.settings."+id, DeveloperModePath)
}

// LocationUnpacked is an extension loaded from a folder on this computer
// (extensions::mojom::ManifestLocation::kUnpacked).
const LocationUnpacked = 4

// DeveloperModePath is the protected setting that lets folder extensions run.
const DeveloperModePath = "extensions.ui.developer_mode"

// RemoveLocal takes the registration out again, leaving the folder alone.
func RemoveLocal(profile, id, deviceID string) error {
	return updateSecure(profile, deviceID, func(doc map[string]any) {
		exts, _ := doc["extensions"].(map[string]any)
		if exts == nil {
			return
		}
		settings, _ := exts["settings"].(map[string]any)
		if settings == nil {
			return
		}
		delete(settings, id)
	}, "extensions.settings."+id)
}

// LocalInstalled reports whether this profile points at this folder and
// Helium will run it: registered from a folder, nothing switching it off,
// developer mode on, and every permission the folder's manifest asks for
// already granted (a new version may ask for more; registering it again
// grants them, so nobody is asked).
func LocalInstalled(profile, id, dir, goos string) bool {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	doc, err := readJSON(filepath.Join(profile, "Secure Preferences"))
	if err != nil {
		return false
	}
	e, _ := chromejson.Lookup(doc, "extensions.settings."+id).(map[string]any)
	if e == nil {
		return false
	}
	reasons, _ := e["disable_reasons"].([]any)
	return str(e["path"]) == pathFor(abs, goos) &&
		intOf(e["location"]) == LocationUnpacked &&
		len(reasons) == 0 &&
		chromejson.Lookup(doc, DeveloperModePath) == true &&
		grantedAll(e, abs)
}

// grantedAll reports whether the entry grants every API permission the
// manifest in dir asks for.
func grantedAll(entry map[string]any, dir string) bool {
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return true // nothing to compare with; the path check decides
	}
	var manifest map[string]any
	if json.Unmarshal(raw, &manifest) != nil {
		return true
	}
	granted := map[string]bool{}
	g, _ := entry["granted_permissions"].(map[string]any)
	api, _ := g["api"].([]any)
	for _, p := range api {
		if s, ok := p.(string); ok {
			granted[s] = true
		}
	}
	want, _ := permissions(manifest)["api"].([]any)
	for _, p := range want {
		if !granted[p.(string)] {
			return false
		}
	}
	return true
}

func pathFor(abs, goos string) string {
	if goos == "windows" {
		return strings.ReplaceAll(abs, "/", `\`)
	}
	return strings.ReplaceAll(abs, `\`, "/")
}

// setPath writes a value at a dotted path, making the maps on the way.
func setPath(doc map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	cur := doc
	for _, p := range parts[:len(parts)-1] {
		next, _ := cur[p].(map[string]any)
		if next == nil {
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	cur[parts[len(parts)-1]] = value
}

// permissions turns the manifest's permission lists into the shape Helium
// records as already granted, so the person is never asked again on a
// computer they already approved.
func permissions(manifest map[string]any) map[string]any {
	api := []any{}
	for _, key := range []string{"permissions"} {
		list, _ := manifest[key].([]any)
		for _, p := range list {
			if s, ok := p.(string); ok && !strings.Contains(s, "://") {
				api = append(api, s)
			}
		}
	}
	sort.Slice(api, func(i, j int) bool { return api[i].(string) < api[j].(string) })
	hosts := []any{}
	for _, key := range []string{"host_permissions"} {
		list, _ := manifest[key].([]any)
		hosts = append(hosts, list...)
	}
	// The key names are Helium's own, singular: "explicit_host".
	return map[string]any{
		"api":                  api,
		"explicit_host":        hosts,
		"manifest_permissions": []any{},
		"scriptable_host":      []any{},
	}
}

// chromeNow is the current time in Chromium's clock: microseconds since
// 1601, written as a string the way Helium writes these two fields.
func chromeNow() string {
	const epoch = 11644473600 // seconds between 1601 and 1970
	return fmt.Sprintf("%d", (time.Now().Unix()+epoch)*1000000)
}
