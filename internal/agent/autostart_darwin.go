package agent

import (
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const label = "com.aiwithrishabh.helium-syncer"

func plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist")
}

// SetAutostart installs or removes a LaunchAgent that runs the agent at login.
func SetAutostart(on bool) error {
	uid := strconv.Itoa(os.Getuid())
	if !on {
		exec.Command("/bin/launchctl", "bootout", "gui/"+uid+"/"+label).Run()
		err := os.Remove(plistPath())
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key><array>%s</array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>ProcessType</key><string>Interactive</string>
</dict>
</plist>
`, label, plistArgs(append([]string{exe}, AutostartArgs...)))
	if err := os.MkdirAll(filepath.Dir(plistPath()), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(plistPath(), []byte(plist), 0o644); err != nil {
		return err
	}
	exec.Command("/bin/launchctl", "bootout", "gui/"+uid+"/"+label).Run()
	if out, err := exec.Command("/bin/launchctl", "bootstrap", "gui/"+uid, plistPath()).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl: %v: %s", err, out)
	}
	return nil
}

func plistArgs(args []string) string {
	var b strings.Builder
	for _, a := range args {
		b.WriteString("<string>")
		xml.EscapeText(&b, []byte(a))
		b.WriteString("</string>")
	}
	return b.String()
}

// Autostart reports whether start-at-login is on.
func Autostart() bool {
	_, err := os.Stat(plistPath())
	return err == nil
}

// StartDetached starts the agent in the background.
func StartDetached() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return exec.Command(exe, AutostartArgs...).Start()
}

// LaunchHelium opens Helium by its bundle id (wherever it is installed).
func LaunchHelium() error {
	return exec.Command("/usr/bin/open", "-b", "net.imput.helium").Run()
}

// OpenBrowser opens a URL in the default browser.
func OpenBrowser(url string) error { return exec.Command("/usr/bin/open", url).Start() }
