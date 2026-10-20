// Package helium answers questions about a Helium install that every sync
// step depends on: is it running on this profile folder, and which version
// wrote it.
package helium

import (
	"errors"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// IsRunning reports whether a Helium process holds this user data folder.
// Chromium keeps "lockfile" open without sharing while it runs, so opening
// it exclusively fails exactly then. Nothing is written.
func IsRunning(userData string) (bool, error) {
	p, err := windows.UTF16PtrFromString(filepath.Join(userData, "lockfile"))
	if err != nil {
		return false, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	switch {
	case err == nil:
		windows.CloseHandle(h)
		return false, nil
	case errors.Is(err, windows.ERROR_FILE_NOT_FOUND), errors.Is(err, windows.ERROR_PATH_NOT_FOUND):
		return false, nil
	case errors.Is(err, windows.ERROR_SHARING_VIOLATION), errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return true, nil
	}
	return false, err
}
