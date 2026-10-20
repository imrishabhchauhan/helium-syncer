//go:build !windows

package helium

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// IsRunning reports whether a Helium process holds this user data folder.
// Chromium on macOS/Linux keeps a "SingletonLock" symlink pointing at
// "<hostname>-<pid>"; it is running if that process is alive. A stale lock
// (left by a crash) counts as not running. Nothing is written.
func IsRunning(userData string) (bool, error) {
	target, err := os.Readlink(filepath.Join(userData, "SingletonLock"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	i := strings.LastIndexByte(target, '-')
	if i < 0 {
		return true, nil // unknown format: be safe
	}
	pid, err := strconv.Atoi(target[i+1:])
	if err != nil || pid <= 0 {
		return true, nil
	}
	switch err := syscall.Kill(pid, 0); {
	case err == nil, errors.Is(err, syscall.EPERM):
		return true, nil
	default:
		return false, nil
	}
}
