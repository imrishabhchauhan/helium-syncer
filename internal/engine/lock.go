package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// ErrBusy means another Helium Syncer program is syncing this folder now.
var ErrBusy = errors.New("another Helium Syncer sync is running; try again in a moment")

// staleLock: a lock older than this is left over from a crash.
const staleLock = 30 * time.Minute

// lockState makes sure only one process syncs a user data folder at a time
// (the background agent and the app window could otherwise collide).
func lockState(stateDir string) (func(), error) {
	p := filepath.Join(stateDir, "run.lock")
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
			f.Close()
			return func() { os.Remove(p) }, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		info, serr := os.Stat(p)
		if serr == nil && time.Since(info.ModTime()) > staleLock {
			os.Remove(p) // left over from a crash
			continue
		}
		return nil, ErrBusy
	}
	return nil, ErrBusy
}
