// Package extensions moves user-installed extensions, with their data,
// between Helium profiles (possibly on different devices and OSes).
//
// An installed extension is:
//   - its unpacked files:      <profile>/Extensions/<id>/<version>_0/
//   - its registration:        Secure Preferences -> extensions.settings.<id>
//     (protected: must be re-signed for the target device)
//   - its data, if any:        Local Extension Settings/<id>, Sync Extension Settings/<id>,
//     IndexedDB/chrome-extension_<id>_0.indexeddb.{leveldb,blob}
//
// Only Web Store installs (location 1) are moved; built-in, policy and
// unpacked extensions are device-specific.
package extensions

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/imrishabhchauhan/helium-syncer/internal/chromejson"
	"github.com/imrishabhchauhan/helium-syncer/internal/prefhash"
)

// LocationInternal is a normal Web Store install (extensions::mojom::ManifestLocation::kInternal).
const LocationInternal = 1

// Extension is one registered extension.
type Extension struct {
	ID          string
	Name        string
	Version     string
	Location    int
	FromStore   bool
	Path        string // relative to Extensions/, with forward slashes
	Disabled    bool
	settingsRaw map[string]any
}

// List returns the extensions registered in a profile.
func List(profile string) ([]Extension, error) {
	doc, err := readJSON(filepath.Join(profile, "Secure Preferences"))
	if err != nil {
		return nil, err
	}
	settings, _ := chromejson.Lookup(doc, "extensions.settings").(map[string]any)
	var out []Extension
	for id, v := range settings {
		m, _ := v.(map[string]any)
		if m == nil {
			continue
		}
		e := Extension{ID: id, settingsRaw: m}
		e.Location = intOf(m["location"])
		e.FromStore, _ = m["from_webstore"].(bool)
		e.Path = filepath.ToSlash(str(m["path"]))
		if man, ok := m["manifest"].(map[string]any); ok {
			e.Name, e.Version = str(man["name"]), str(man["version"])
		}
		if dr, ok := m["disable_reasons"].([]any); ok && len(dr) > 0 {
			e.Disabled = true
		}
		out = append(out, e)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out, nil
}

// Syncable reports whether this extension should move between devices.
func (e Extension) Syncable() bool { return e.Location == LocationInternal && e.FromStore }

// Transfer copies extension id with its data from src to dst profile and
// registers it there, signed for this device. Helium must not be running on
// dst. Existing data for the same extension in dst is replaced (last writer
// wins per extension); call sites take a backup first.
func Transfer(src, dst, id, deviceID, goos string) error {
	exts, err := List(src)
	if err != nil {
		return err
	}
	var e *Extension
	for i := range exts {
		if exts[i].ID == id {
			e = &exts[i]
		}
	}
	if e == nil {
		return fmt.Errorf("extensions: %s is not installed in source", id)
	}
	if !e.Syncable() {
		return fmt.Errorf("extensions: %s (%s) is not a Web Store install", id, e.Name)
	}
	if e.Path == "" || strings.Contains(e.Path, "..") || !strings.HasPrefix(e.Path, id+"/") {
		return fmt.Errorf("extensions: %s has an unexpected path %q", id, e.Path)
	}

	// 1. Files and data folders.
	dirs := [][2]string{{filepath.Join("Extensions", filepath.FromSlash(e.Path)), ""}}
	for _, d := range []string{"Local Extension Settings", "Sync Extension Settings"} {
		dirs = append(dirs, [2]string{filepath.Join(d, id), ""})
	}
	for _, suffix := range []string{".indexeddb.leveldb", ".indexeddb.blob"} {
		dirs = append(dirs, [2]string{filepath.Join("IndexedDB", "chrome-extension_"+id+"_0"+suffix), ""})
	}
	for _, d := range dirs {
		from := filepath.Join(src, d[0])
		if _, err := os.Stat(from); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		to := filepath.Join(dst, d[0])
		if err := os.RemoveAll(to); err != nil {
			return err
		}
		if err := copyTree(from, to); err != nil {
			return fmt.Errorf("extensions: copy %s: %w", d[0], err)
		}
	}

	// 2. Registration, with the path in the target OS's separator, minus
	// state that belongs to the source device.
	entry, _ := chromejson.Decode([]byte(chromejson.Compact(e.settingsRaw))) // deep copy
	m := entry.(map[string]any)
	sep := "/"
	if goos == "windows" {
		sep = `\`
	}
	m["path"] = strings.ReplaceAll(e.Path, "/", sep)
	for _, k := range []string{"service_worker_registration_info", "has_started_service_worker", "serviceworkerevents"} {
		delete(m, k)
	}

	path := filepath.Join(dst, "Secure Preferences")
	doc, err := readJSON(path)
	if err != nil {
		return err
	}
	chromejson.Set(doc, "extensions.settings."+id, m)
	prefhash.Resign(doc, prefhash.Calculator{DeviceID: deviceID}, true, "extensions.settings."+id)
	return writeAtomic(path, []byte(chromejson.Compact(doc)))
}

func readJSON(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	v, err := chromejson.Decode(data)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: not an object", path)
	}
	return m, nil
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

func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		target := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if d.Name() == "LOCK" {
			return nil // LevelDB lock file is per process
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

func str(v any) string { s, _ := v.(string); return s }

func intOf(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case interface{ Int64() (int64, error) }:
		n, _ := t.Int64()
		return int(n)
	}
	return 0
}
