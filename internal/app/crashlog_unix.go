//go:build !windows

package app

import (
	"os"

	"golang.org/x/sys/unix"
)

// CatchCrashes sends anything the Go runtime prints when the program dies -
// a panic and its stack - into f.
//
// Started by launchd or from a menu bar, the program has nowhere to print,
// so without this it vanishes and leaves not one line to say why. The
// runtime writes to file number 2, not to Go's os.Stderr, so the file takes
// that number.
func CatchCrashes(f *os.File) error {
	if err := unix.Dup2(int(f.Fd()), 2); err != nil {
		return err
	}
	os.Stderr = f
	return nil
}
