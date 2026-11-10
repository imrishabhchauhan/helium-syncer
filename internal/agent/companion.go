package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/imrishabhchauhan/helium-syncer/extension"
	"github.com/imrishabhchauhan/helium-syncer/internal/extensions"
)

// Companion keeps the companion extension installed in every synced
// profile, so bookmarks sync while Helium is open without anyone visiting
// the extensions page. It only writes while Helium is closed: Helium keeps
// its settings in memory and would throw the change away.
type Companion struct {
	Dir       string               // where the extension's files are written
	MachineID string               // signs Secure Preferences for this computer
	GOOS      string               // file conventions of this computer
	Running   func() (bool, error) // whether Helium is open right now
}

// WriteFiles puts the extension's files in c.Dir, rewriting only those that
// changed. A new app version brings a new extension; Helium picks it up from
// the folder the next time it starts.
//
// The background script is written as background-<version>.js, and the
// manifest points there. Helium keeps a copy of an extension's background
// script and goes on running it after the file changes, even across
// restarts (seen on both computers: 0.1.0 still running with 0.1.2 on disk).
// A new script name is what makes it load the new code.
func (c Companion) WriteFiles() error {
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return err
	}
	files, script, err := companionFiles()
	if err != nil {
		return err
	}
	for name, want := range files {
		p := filepath.Join(c.Dir, name)
		if have, err := os.ReadFile(p); err == nil && bytes.Equal(have, want) {
			continue
		}
		if err := os.WriteFile(p, want, 0o600); err != nil {
			return err
		}
	}
	// Older scripts are no longer referenced; leave nothing stale behind.
	old, _ := filepath.Glob(filepath.Join(c.Dir, "background*.js"))
	for _, p := range old {
		if filepath.Base(p) != script {
			os.Remove(p)
		}
	}
	return nil
}

// companionFiles is the extension as written to disk: the manifest with its
// service worker renamed for this version, and the script under that name.
func companionFiles() (files map[string][]byte, script string, err error) {
	raw, err := extension.Files.ReadFile("manifest.json")
	if err != nil {
		return nil, "", err
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, "", err
	}
	version, _ := manifest["version"].(string)
	if version == "" {
		return nil, "", fmt.Errorf("companion extension manifest has no version")
	}
	script = "background-" + version + ".js"
	bg, _ := manifest["background"].(map[string]any)
	if bg == nil {
		return nil, "", fmt.Errorf("companion extension manifest has no background")
	}
	bg["service_worker"] = script
	m, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, "", err
	}
	code, err := extension.Files.ReadFile("background.js")
	if err != nil {
		return nil, "", err
	}
	return map[string][]byte{"manifest.json": m, script: code}, script, nil
}

// Ensure installs the extension in each profile folder that lacks it, and
// returns the ones it installed. It stops, without error, the moment Helium
// is open, so a person who reopens Helium mid-way loses nothing; the rest
// are done after the next quit.
func (c Companion) Ensure(profileDirs []string) ([]string, error) {
	if err := c.WriteFiles(); err != nil {
		return nil, fmt.Errorf("companion extension files: %w", err)
	}
	dirs := append([]string(nil), profileDirs...)
	sort.Strings(dirs)
	var done []string
	for _, p := range dirs {
		if extensions.LocalInstalled(p, ExtensionID, c.Dir, c.GOOS) {
			continue
		}
		if open, err := c.Running(); err != nil || open {
			return done, err
		}
		if err := extensions.InstallLocal(p, ExtensionID, c.Dir, c.MachineID, c.GOOS); err != nil {
			return done, fmt.Errorf("companion extension in %s: %w", filepath.Base(p), err)
		}
		done = append(done, p)
	}
	return done, nil
}
