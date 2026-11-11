package agent

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const runValue = "HeliumSyncer"

// appExe is the window program. It has no console, so Windows shows nothing
// when it starts.
const appExe = "Helium Syncer.exe"

// loginProgram picks what Windows should start at login, given the program
// that is running now. The command-line program is a console program: if
// Windows starts it, a black terminal window sits on the screen until the
// computer is turned off. The window program is installed next to it and
// does the same syncing, so start that instead when it is there.
func loginProgram(exe string, exists func(string) bool) (string, []string) {
	if strings.EqualFold(filepath.Base(exe), appExe) {
		return exe, AutostartArgs
	}
	app := filepath.Join(filepath.Dir(exe), appExe)
	if exists(app) {
		return app, []string{"--background"}
	}
	return exe, AutostartArgs
}

// onDisk says whether a file is there.
func onDisk(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// SetAutostart starts the agent at login (current user only) or stops that.
func SetAutostart(on bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue(runValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, args := loginProgram(exe, onDisk)
	return k.SetStringValue(runValue, `"`+exe+`" `+strings.Join(args, " "))
}

// Autostart reports whether start-at-login is on.
func Autostart() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runValue)
	return err == nil
}

// StartDetached starts the agent in the background without a console window.
// It starts the same program that start-at-login would, so what runs now and
// what runs after the next log-in are the same thing.
func StartDetached() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, args := loginProgram(exe, onDisk)
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x00000008} // DETACHED_PROCESS
	return cmd.Start()
}

// HeliumExecutable finds Helium's program from the uninstall registry entry,
// then the usual folders.
func HeliumExecutable() (string, error) {
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		for _, base := range []string{`Software\Microsoft\Windows\CurrentVersion\Uninstall`, `Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`} {
			k, err := registry.OpenKey(root, base, registry.ENUMERATE_SUB_KEYS)
			if err != nil {
				continue
			}
			names, _ := k.ReadSubKeyNames(-1)
			k.Close()
			for _, n := range names {
				sk, err := registry.OpenKey(root, base+`\`+n, registry.QUERY_VALUE)
				if err != nil {
					continue
				}
				dn, _, _ := sk.GetStringValue("DisplayName")
				pub, _, _ := sk.GetStringValue("Publisher")
				loc, _, _ := sk.GetStringValue("InstallLocation")
				sk.Close()
				if (dn == "Helium" || pub == "The Helium Authors") && loc != "" {
					p := filepath.Join(loc, "chrome.exe")
					if _, err := os.Stat(p); err == nil {
						return p, nil
					}
				}
			}
		}
	}
	for _, p := range []string{filepath.Join(os.Getenv("LOCALAPPDATA"), `imput\Helium\Application\chrome.exe`),
		filepath.Join(os.Getenv("ProgramFiles"), `imput\Helium\Application\chrome.exe`)} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", errors.New("Helium is not installed (or not where Windows records it)")
}

// LaunchHelium opens Helium.
func LaunchHelium() error {
	exe, err := HeliumExecutable()
	if err != nil {
		return err
	}
	return exec.Command(exe).Start()
}

// OpenBrowser opens a URL in the default browser.
func OpenBrowser(url string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}
