package engine

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/imrishabhchauhan/helium-syncer/internal/crdt"
	"github.com/imrishabhchauhan/helium-syncer/internal/helium"
	"github.com/imrishabhchauhan/helium-syncer/internal/profiles"
)

// DryRunReport is what a sync would do, without doing it.
type DryRunReport struct {
	Report
	WouldChange []string // files in Helium's folder that would change (relative)
	WouldCreate []string // files or folders that would be created
}

// noSend wraps a transport so nothing is sent or announced.
type noSend struct{ Transport }

func (noSend) Push(context.Context, []crdt.Op) error                  { return nil }
func (noSend) Announce(context.Context, string, string, string) error { return nil }

// DryRun runs a complete sync on a temporary copy of the user data and
// state, with sending switched off, and reports exactly which of Helium's
// files would change. The real folders are only read.
func DryRun(ctx context.Context, cfg Config) (DryRunReport, error) {
	var out DryRunReport
	if running, err := helium.IsRunning(cfg.UserData); err != nil {
		return out, err
	} else if running {
		return out, ErrHeliumRunning
	}
	tmp, err := os.MkdirTemp("", "helium-syncer-dryrun-")
	if err != nil {
		return out, err
	}
	defer os.RemoveAll(tmp)
	ud, sd := filepath.Join(tmp, "User Data"), filepath.Join(tmp, "state")

	// Copy only what a sync reads or writes.
	for _, f := range []string{"Local State", "Last Version"} {
		if err := copyIfExists(filepath.Join(cfg.UserData, f), filepath.Join(ud, f)); err != nil {
			return out, err
		}
	}
	list, err := profiles.List(cfg.UserData)
	if err != nil {
		return out, err
	}
	for _, p := range list {
		for _, f := range append(append([]string(nil), profileFiles...), "Sessions") {
			if err := copyIfExists(filepath.Join(cfg.UserData, p.Dir, f), filepath.Join(ud, p.Dir, f)); err != nil {
				return out, err
			}
		}
	}
	if err := copyIfExists(cfg.StateDir, sd); err != nil {
		return out, err
	}

	c := cfg
	c.UserData, c.StateDir, c.Transport = ud, sd, noSend{cfg.Transport}
	rep, err := Run(ctx, c)
	if err != nil {
		return out, err
	}
	out.Report = rep
	out.Written = nil

	// Compare the copy with the real folder.
	err = filepath.WalkDir(ud, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(ud, p)
		if rel == "." {
			return nil
		}
		real := filepath.Join(cfg.UserData, rel)
		if d.IsDir() {
			if _, err := os.Stat(real); errors.Is(err, fs.ErrNotExist) {
				out.WouldCreate = append(out.WouldCreate, rel+string(filepath.Separator))
				return filepath.SkipDir
			}
			return nil
		}
		a, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(real)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			out.WouldCreate = append(out.WouldCreate, rel)
		case err != nil:
			return err
		case !bytes.Equal(a, b):
			out.WouldChange = append(out.WouldChange, rel)
		}
		return nil
	})
	sort.Strings(out.WouldChange)
	sort.Strings(out.WouldCreate)
	return out, err
}

func copyIfExists(src, dst string) error {
	info, err := os.Stat(src)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return copyDir(src, dst)
	}
	return copyFile(src, dst)
}
