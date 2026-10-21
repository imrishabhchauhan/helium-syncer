package extensions

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/imrishabhchauhan/helium-syncer/internal/chromejson"
	"github.com/imrishabhchauhan/helium-syncer/internal/prefhash"
)

// Files skipped when packing: per-process LevelDB locks and logs.
func skipFile(name string) bool {
	return name == "LOCK" || name == "LOG" || name == "LOG.old"
}

// dataDirs are an extension's data folders, relative to the profile.
func dataDirs(id string) []string {
	return []string{
		filepath.Join("Local Extension Settings", id),
		filepath.Join("Sync Extension Settings", id),
		filepath.Join("IndexedDB", "chrome-extension_"+id+"_0.indexeddb.leveldb"),
		filepath.Join("IndexedDB", "chrome-extension_"+id+"_0.indexeddb.blob"),
	}
}

// tarDirs writes the given folders (relative to base) into a deterministic
// tar.gz: sorted names, no timestamps or owners, so equal content gives
// equal bytes.
func tarDirs(base string, rels []string, extra map[string][]byte) ([]byte, error) {
	var buf bytes.Buffer
	gz, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	tw := tar.NewWriter(gz)
	type entry struct {
		name string
		data []byte
	}
	var entries []entry
	for _, rel := range rels {
		root := filepath.Join(base, rel)
		if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || skipFile(d.Name()) {
				return nil
			}
			r, _ := filepath.Rel(base, p)
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			entries = append(entries, entry{filepath.ToSlash(r), data})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	for name, data := range extra {
		entries = append(entries, entry{name, data})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o600, Size: int64(len(e.data)), Format: tar.FormatPAX}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(e.data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// untar extracts a package into base. Every name must stay inside one of
// the allowed prefixes; anything else (absolute paths, "..", links) is
// refused, so a damaged or malicious package cannot write elsewhere.
func untar(data []byte, base string, allowed []string) (map[string][]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	extras := map[string][]byte{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("extensions: package contains a non-file entry %q", h.Name)
		}
		name := h.Name
		clean := path.Clean(name)
		if clean != name || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "../") || clean == ".." || strings.Contains(clean, "\\") {
			return nil, fmt.Errorf("extensions: unsafe path in package: %q", name)
		}
		body, err := io.ReadAll(io.LimitReader(tr, 200<<20))
		if err != nil {
			return nil, err
		}
		if !strings.Contains(clean, "/") { // top-level metadata file
			extras[clean] = body
			continue
		}
		ok := false
		for _, a := range allowed {
			if strings.HasPrefix(clean, filepath.ToSlash(a)+"/") {
				ok = true
			}
		}
		if !ok {
			return nil, fmt.Errorf("extensions: package writes outside its folders: %q", name)
		}
		target := filepath.Join(base, filepath.FromSlash(clean))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(target, body, 0o600); err != nil {
			return nil, err
		}
	}
	return extras, nil
}

// registration returns a profile's extension entry and its version folder.
func registration(profile, id string) (map[string]any, *Extension, error) {
	exts, err := List(profile)
	if err != nil {
		return nil, nil, err
	}
	for i := range exts {
		if exts[i].ID == id {
			return exts[i].settingsRaw, &exts[i], nil
		}
	}
	return nil, nil, fmt.Errorf("extensions: %s is not installed", id)
}

// Package bundles an installed Web Store extension (its files and its
// registration, minus device-only state) for another device.
func Package(profile, id string) ([]byte, error) {
	raw, e, err := registration(profile, id)
	if err != nil {
		return nil, err
	}
	if !e.Syncable() || !strings.HasPrefix(e.Path, id+"/") || strings.Contains(e.Path, "..") {
		return nil, fmt.Errorf("extensions: %s cannot be synced", id)
	}
	entry, _ := chromejson.Decode([]byte(chromejson.Compact(raw)))
	m := entry.(map[string]any)
	for _, k := range []string{"service_worker_registration_info", "has_started_service_worker", "serviceworkerevents"} {
		delete(m, k)
	}
	m["path"] = e.Path // forward slashes; converted on install
	reg := []byte(chromejson.Compact(m))
	return tarDirs(profile, []string{filepath.Join("Extensions", filepath.FromSlash(e.Path))}, map[string][]byte{"registration.json": reg})
}

// InstallPackage installs (or updates) an extension from Package's output
// and registers it, signed for this device. Helium must not be running.
func InstallPackage(profile, id string, pkg []byte, deviceID, goos string) error {
	tmp, err := os.MkdirTemp(profile, ".hs-ext-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	extras, err := untar(pkg, tmp, []string{filepath.Join("Extensions", id)})
	if err != nil {
		return err
	}
	var reg map[string]any
	v, err := chromejson.Decode(extras["registration.json"])
	if err != nil {
		return fmt.Errorf("extensions: package has no valid registration: %w", err)
	}
	reg, _ = v.(map[string]any)
	rel := str(reg["path"])
	if !strings.HasPrefix(rel, id+"/") || strings.Contains(rel, "..") {
		return fmt.Errorf("extensions: package for %s has a bad path %q", id, rel)
	}
	if _, err := os.Stat(filepath.Join(tmp, "Extensions", filepath.FromSlash(rel))); err != nil {
		return fmt.Errorf("extensions: package for %s is missing its files", id)
	}
	// Swap in the new version folder; remove older versions of this id.
	extRoot := filepath.Join(profile, "Extensions", id)
	if err := os.RemoveAll(extRoot); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(extRoot), 0o700); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(tmp, "Extensions", id), extRoot); err != nil {
		return err
	}
	sep := "/"
	if goos == "windows" {
		sep = `\`
	}
	reg["path"] = strings.ReplaceAll(rel, "/", sep)
	return updateSecure(profile, deviceID, func(doc map[string]any) {
		chromejson.Set(doc, "extensions.settings."+id, reg)
	}, "extensions.settings."+id)
}

// Remove uninstalls an extension (files, registration and data).
func Remove(profile, id, deviceID string) error {
	if err := updateSecure(profile, deviceID, func(doc map[string]any) {
		if s, ok := chromejson.Lookup(doc, "extensions.settings").(map[string]any); ok {
			delete(s, id)
		}
		if m, ok := chromejson.Lookup(doc, "protection.macs.extensions.settings").(map[string]any); ok {
			delete(m, id)
		}
	}); err != nil {
		return err
	}
	for _, d := range append([]string{filepath.Join("Extensions", id)}, dataDirs(id)...) {
		if err := os.RemoveAll(filepath.Join(profile, d)); err != nil {
			return err
		}
	}
	return nil
}

// DisableUserAction is Chromium's disable reason for "turned off by the user".
const DisableUserAction = 1

// SetEnabled turns an extension on or off like the user would.
func SetEnabled(profile, id string, enabled bool, deviceID string) error {
	return updateSecure(profile, deviceID, func(doc map[string]any) {
		e, ok := chromejson.Lookup(doc, "extensions.settings."+id).(map[string]any)
		if !ok {
			return
		}
		if enabled {
			e["disable_reasons"] = []any{}
		} else {
			one, _ := chromejson.Decode([]byte(fmt.Sprint(DisableUserAction)))
			e["disable_reasons"] = []any{one}
		}
	}, "extensions.settings."+id)
}

// DataHash fingerprints an extension's data ("" if it has none).
func DataHash(profile, id string) (string, error) {
	pkg, n, err := packData(profile, id)
	if err != nil || n == 0 {
		return "", err
	}
	return hashOf(pkg), nil
}

func hashOf(pkg []byte) string {
	sum := sha256.Sum256(pkg)
	return hex.EncodeToString(sum[:])
}

// PackData bundles an extension's data folders.
func PackData(profile, id string) ([]byte, error) {
	pkg, _, err := packData(profile, id)
	return pkg, err
}

func packData(profile, id string) ([]byte, int, error) {
	n := 0
	for _, d := range dataDirs(id) {
		filepath.WalkDir(filepath.Join(profile, d), func(_ string, e fs.DirEntry, err error) error {
			if err == nil && !e.IsDir() && !skipFile(e.Name()) {
				n++
			}
			return nil
		})
	}
	if n == 0 {
		return nil, 0, nil
	}
	pkg, err := tarDirs(profile, dataDirs(id), nil)
	return pkg, n, err
}

// ReplaceData replaces an extension's data folders with a bundle from
// PackData (whole-extension last writer wins).
func ReplaceData(profile, id string, pkg []byte) error {
	tmp, err := os.MkdirTemp(profile, ".hs-extdata-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if _, err := untar(pkg, tmp, dataDirs(id)); err != nil {
		return err
	}
	for _, d := range dataDirs(id) {
		dst := filepath.Join(profile, d)
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
		src := filepath.Join(tmp, d)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := os.Rename(src, dst); err != nil {
			return err
		}
	}
	return nil
}

// updateSecure edits Secure Preferences and re-signs it for this device.
func updateSecure(profile, deviceID string, edit func(doc map[string]any), extraPaths ...string) error {
	path := filepath.Join(profile, "Secure Preferences")
	doc, err := readJSON(path)
	if errors.Is(err, fs.ErrNotExist) {
		doc = map[string]any{"protection": map[string]any{"macs": map[string]any{}}}
	} else if err != nil {
		return err
	}
	edit(doc)
	prefhash.Resign(doc, prefhash.Calculator{DeviceID: deviceID}, true, extraPaths...)
	return writeAtomic(path, []byte(chromejson.Compact(doc)))
}

// RegistrationJSON is exposed for tests.
func RegistrationJSON(profile, id string) (string, error) {
	raw, _, err := registration(profile, id)
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(raw)
	return string(b), nil
}
