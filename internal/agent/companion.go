package agent

import (
	"bytes"
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
func (c Companion) WriteFiles() error {
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return err
	}
	for _, name := range extension.Names {
		want, err := extension.Files.ReadFile(name)
		if err != nil {
			return err
		}
		p := filepath.Join(c.Dir, name)
		if have, err := os.ReadFile(p); err == nil && bytes.Equal(have, want) {
			continue
		}
		if err := os.WriteFile(p, want, 0o600); err != nil {
			return err
		}
	}
	return nil
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
