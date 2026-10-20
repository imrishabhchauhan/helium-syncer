//go:build !windows && !darwin

package agent

import (
	"errors"
	"os"
	"os/exec"
)

// SetAutostart is not supported on this OS yet.
func SetAutostart(bool) error { return errors.New("start at login is not supported on this OS yet") }

// Autostart reports whether start-at-login is on.
func Autostart() bool { return false }

// StartDetached starts the agent in the background.
func StartDetached() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return exec.Command(exe, "agent").Start()
}

// LaunchHelium opens Helium.
func LaunchHelium() error { return exec.Command("helium").Start() }

// OpenBrowser opens a URL in the default browser.
func OpenBrowser(url string) error { return exec.Command("xdg-open", url).Start() }
