package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The files a sync may change, relative to a profile folder, and the kind
// of data each holds. A kind the person switched off is never written, so
// backing it up copies a large file for nothing: History alone is 44 MB on
// the owner's Mac, where history is off, and it was copied on every sync.
var profileFiles = map[string][]string{
	"Bookmarks":          {KindBookmarks},
	"History":            {KindHistory},
	"History-journal":    {KindHistory},
	"Preferences":        {KindSettings, KindExtensions},
	"Secure Preferences": {KindSettings, KindExtensions},
	"Sessions":           {KindTabs},
}

// backedUp reports whether a file must be copied before this sync: only
// when at least one kind of data it holds is still on.
func backedUp(name string, off map[string]bool) bool {
	kinds, known := profileFiles[name]
	if !known {
		return true
	}
	for _, k := range kinds {
		if !off[k] {
			return true
		}
	}
	return false
}

// backupOrder keeps the copying in the same order every time, so a failure
// is the same failure twice.
var backupOrder = []string{"Bookmarks", "History", "History-journal", "Preferences", "Secure Preferences", "Sessions"}

// journal is written before a sync touches Helium's files and removed when
// the sync finished. If it exists at startup, the previous sync was cut off
// (power loss, crash): the backup is put back and the sync starts again.
type journal struct {
	Backup  string   `json:"backup"`  // backup folder
	Created []string `json:"created"` // paths this sync created (removed on recovery)
	Written []string `json:"written"` // every file this sync wrote (for the report)
}

// backup copies every file a sync may change into a new dated folder.
func backup(stateDir, userData string, profileDirs []string, off map[string]bool, now time.Time) (string, error) {
	dst := filepath.Join(stateDir, "backups", now.UTC().Format("20060102-150405.000"))
	copyOne := func(rel string) error {
		src := filepath.Join(userData, rel)
		info, err := os.Stat(src)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.IsDir() {
			return copyDir(src, filepath.Join(dst, rel))
		}
		return copyFile(src, filepath.Join(dst, rel))
	}
	if err := copyOne("Local State"); err != nil {
		return "", err
	}
	for _, d := range profileDirs {
		for _, f := range backupOrder {
			if !backedUp(f, off) {
				continue
			}
			if err := copyOne(filepath.Join(d, f)); err != nil {
				return "", err
			}
		}
	}
	// A marker proves the backup is complete; restore refuses half backups.
	return dst, os.WriteFile(filepath.Join(dst, ".complete"), []byte(now.UTC().Format(time.RFC3339)), 0o600)
}

// restore puts a backup back: every backed-up file is restored, Sessions
// folders are restored exactly (files added since are removed), and paths
// the interrupted sync created are removed.
func restore(userData string, j journal) error {
	if _, err := os.Stat(filepath.Join(j.Backup, ".complete")); err != nil {
		return fmt.Errorf("engine: backup %s is incomplete, refusing to restore from it", j.Backup)
	}
	err := filepath.WalkDir(j.Backup, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(j.Backup, p)
		if rel == "." || rel == ".complete" {
			return nil
		}
		target := filepath.Join(userData, rel)
		if d.IsDir() {
			if filepath.Base(rel) == "Sessions" {
				// Remove session files written after the backup.
				entries, _ := os.ReadDir(target)
				for _, e := range entries {
					if _, err := os.Stat(filepath.Join(p, e.Name())); errors.Is(err, fs.ErrNotExist) {
						os.Remove(filepath.Join(target, e.Name()))
					}
				}
			}
			return os.MkdirAll(target, 0o700)
		}
		return copyFile(p, target)
	})
	if err != nil {
		return err
	}
	// Remove what the interrupted sync created (newest first).
	created := append([]string(nil), j.Created...)
	sort.Sort(sort.Reverse(sort.StringSlice(created)))
	for _, c := range created {
		if !strings.HasPrefix(filepath.Clean(c), filepath.Clean(userData)+string(filepath.Separator)) {
			continue // never touch anything outside Helium's folder
		}
		if err := os.RemoveAll(c); err != nil {
			return err
		}
	}
	return nil
}

// pruneBackups keeps at most keep backups, and of those only the ones
// younger than maxAge.
//
// It used to keep the newest keep *and* anything younger than maxAge, which
// on a computer that syncs every few minutes meant keeping a month of them:
// 157 folders and 2.1 GB on the owner's Mac, a laptop.
func pruneBackups(stateDir string, keep int, maxAge time.Duration, now time.Time) error {
	root := filepath.Join(stateDir, "backups")
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for i, n := range names {
		tooMany := i >= keep
		t, err := time.Parse("20060102-150405.000", n)
		tooOld := err == nil && now.Sub(t) >= maxAge
		if !tooMany && !tooOld {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, n)); err != nil {
			return err
		}
	}
	return nil
}

func writeJournal(stateDir string, j journal) error {
	data, _ := json.Marshal(j)
	return writeAtomic(filepath.Join(stateDir, journalFile), data)
}

func readJournal(stateDir string) (*journal, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, journalFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var j journal
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, err
	}
	return &j, nil
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".hs-tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o700)
		}
		return copyFile(p, filepath.Join(dst, rel))
	})
}
