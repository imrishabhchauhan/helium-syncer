package app

import (
	"os"

	"golang.org/x/sys/windows"
)

// CatchCrashes sends anything the Go runtime prints when the program dies -
// a panic and its stack - into f.
//
// A window program on Windows has no console, so that output goes nowhere:
// the program vanishes and leaves not one line to say why. The runtime
// writes to the system's error handle, not to Go's os.Stderr, so both have
// to be pointed at the file.
func CatchCrashes(f *os.File) error {
	if err := windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(f.Fd())); err != nil {
		return err
	}
	os.Stderr = f
	return nil
}
