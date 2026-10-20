package agent

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const runValue = "HeliumSyncer"

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
	return k.SetStringValue(runValue, `"`+exe+`" agent`)
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
func StartDetached() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "agent")
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
