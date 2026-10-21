// Package profiles lists and adds Helium profiles in a user data folder
// ("Local State" -> profile.info_cache, plus one folder per profile).
//
// Adding a profile never changes existing ones: it creates a new folder and
// a new info_cache entry next to them.
package profiles

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/imrishabhchauhan/helium-syncer/internal/chromejson"
)

// Profile is one entry of info_cache.
type Profile struct {
	Dir           string // folder name, e.g. "Default", "Profile 1"
	Name          string
	Avatar        int  // built-in picture number (IDR_PROFILE_AVATAR_n), -1 if not known
	DefaultAvatar bool // Helium shows its default picture in the profile colour
}

var avatarRe = regexp.MustCompile(`IDR_PROFILE_AVATAR_(\d+)$`)

func avatarOf(icon string) int {
	if m := avatarRe.FindStringSubmatch(icon); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return -1
}

func loadLocalState(userData string) (map[string]any, error) {
	data, err := os.ReadFile(filepath.Join(userData, "Local State"))
	if err != nil {
		return nil, err
	}
	v, err := chromejson.Decode(data)
	if err != nil {
		return nil, err
	}
	doc, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("profiles: Local State is not an object")
	}
	return doc, nil
}

// List returns the profiles in info_cache, sorted by folder name.
func List(userData string) ([]Profile, error) {
	doc, err := loadLocalState(userData)
	if err != nil {
		return nil, err
	}
	cache, _ := chromejson.Lookup(doc, "profile.info_cache").(map[string]any)
	var out []Profile
	for dir, v := range cache {
		m, _ := v.(map[string]any)
		name, _ := m["name"].(string)
		icon, _ := m["avatar_icon"].(string)
		def, _ := m["is_using_default_avatar"].(bool)
		out = append(out, Profile{Dir: dir, Name: name, Avatar: avatarOf(icon), DefaultAvatar: def})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Dir < out[b].Dir })
	return out, nil
}

// Rename changes a profile's display name in Local State and in the
// profile's own Preferences. Helium must not be running.
func Rename(userData, dir, name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("profiles: empty name")
	}
	doc, err := loadLocalState(userData)
	if err != nil {
		return err
	}
	entry, _ := chromejson.Lookup(doc, "profile.info_cache."+dir).(map[string]any)
	if entry == nil {
		return fmt.Errorf("profiles: no profile %q", dir)
	}
	entry["name"] = name
	entry["is_using_default_name"] = false
	prefsPath := filepath.Join(userData, dir, "Preferences")
	if data, err := os.ReadFile(prefsPath); err == nil {
		if v, err := chromejson.Decode(data); err == nil {
			if p, ok := v.(map[string]any); ok {
				chromejson.Set(p, "profile.name", name)
				chromejson.Set(p, "profile.using_default_name", false)
				if err := writeAtomic(prefsPath, []byte(chromejson.Compact(p))); err != nil {
					return err
				}
			}
		}
	}
	return writeAtomic(filepath.Join(userData, "Local State"), []byte(chromejson.Compact(doc)))
}

// SetAvatar sets a profile's picture in Local State and in the profile's
// Preferences (Helium reads the picture from both). Helium must not be
// running.
func SetAvatar(userData, dir string, avatar int, isDefault bool) error {
	if avatar < 0 {
		return errors.New("profiles: no picture number")
	}
	doc, err := loadLocalState(userData)
	if err != nil {
		return err
	}
	entry, _ := chromejson.Lookup(doc, "profile.info_cache."+dir).(map[string]any)
	if entry == nil {
		return fmt.Errorf("profiles: no profile %q", dir)
	}
	f := func(s string) any { v, _ := chromejson.Decode([]byte(s)); return v }
	entry["avatar_icon"] = fmt.Sprintf("chrome://theme/IDR_PROFILE_AVATAR_%d", avatar)
	entry["is_using_default_avatar"] = isDefault
	prefsPath := filepath.Join(userData, dir, "Preferences")
	if data, err := os.ReadFile(prefsPath); err == nil {
		if v, err := chromejson.Decode(data); err == nil {
			if p, ok := v.(map[string]any); ok {
				chromejson.Set(p, "profile.avatar_index", f(strconv.Itoa(avatar)))
				chromejson.Set(p, "profile.using_default_avatar", isDefault)
				if err := writeAtomic(prefsPath, []byte(chromejson.Compact(p))); err != nil {
					return err
				}
			}
		}
	}
	return writeAtomic(filepath.Join(userData, "Local State"), []byte(chromejson.Compact(doc)))
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".hs-tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Add creates a new profile called name and returns its folder name. Helium
// must not be running. avatarIndex picks one of Chromium's built-in avatars.
func Add(userData, name string, avatarIndex int) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", errors.New("profiles: empty name")
	}
	doc, err := loadLocalState(userData)
	if err != nil {
		return "", err
	}
	cache, _ := chromejson.Lookup(doc, "profile.info_cache").(map[string]any)
	if cache == nil {
		cache = map[string]any{}
		chromejson.Set(doc, "profile.info_cache", cache)
	}
	dir := ""
	for n := 1; n < 10000; n++ {
		cand := fmt.Sprintf("Profile %d", n)
		if _, used := cache[cand]; used {
			continue
		}
		if _, err := os.Stat(filepath.Join(userData, cand)); err == nil {
			continue // folder exists without an entry: leave it alone
		}
		dir = cand
		break
	}
	if dir == "" {
		return "", errors.New("profiles: no free profile folder name")
	}
	f := func(s string) any { v, _ := chromejson.Decode([]byte(s)); return v }
	cache[dir] = map[string]any{
		"name":                         name,
		"avatar_icon":                  fmt.Sprintf("chrome://theme/IDR_PROFILE_AVATAR_%d", avatarIndex),
		"is_using_default_name":        false,
		"is_using_default_avatar":      false,
		"is_ephemeral":                 false,
		"is_consented_primary_account": false,
		"background_apps":              false,
		"managed_user_id":              "",
		"active_time":                  f("0.0"),
	}
	order, _ := chromejson.Lookup(doc, "profile.profiles_order").([]any)
	chromejson.Set(doc, "profile.profiles_order", append(order, dir))

	pdir := filepath.Join(userData, dir)
	if err := os.Mkdir(pdir, 0o700); err != nil {
		return "", err
	}
	prefs := map[string]any{"profile": map[string]any{
		"name": name, "avatar_index": f(fmt.Sprint(avatarIndex)),
		"using_default_name": false, "using_default_avatar": false,
	}}
	if err := os.WriteFile(filepath.Join(pdir, "Preferences"), []byte(chromejson.Compact(prefs)), 0o600); err != nil {
		os.RemoveAll(pdir)
		return "", err
	}
	path := filepath.Join(userData, "Local State")
	tmp := path + ".hs-tmp"
	if err := os.WriteFile(tmp, []byte(chromejson.Compact(doc)), 0o600); err != nil {
		os.RemoveAll(pdir)
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		os.RemoveAll(pdir)
		return "", err
	}
	return dir, nil
}
