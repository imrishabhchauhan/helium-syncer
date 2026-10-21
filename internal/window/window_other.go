//go:build !windows && !darwin

package window

import "os/exec"

// Open opens the page in the default browser (no app window on this system yet).
func Open(url, dataDir string) error { return exec.Command("xdg-open", url).Start() }

// Close does nothing on this system.
func Close() {}
