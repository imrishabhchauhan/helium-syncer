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

// appExe is the menu-bar program inside Helium Syncer.app. The command-line
// program has no icon and no window, so a person whose computer starts that
// one has no way to see whether sync is working. appPath is where the app
// normally lives.
const appExe = "helium-syncer-app"
const appPath = "/Applications/Helium Syncer.app/Contents/MacOS/" + appExe

// loginProgram picks what to start at log-in, given the program running
// now: the menu-bar program when it is there, so the person has an icon and
// a window to look at, and the command-line one only when it is not.
func loginProgram(exe string, exists func(string) bool) (string, []string) {
	if filepath.Base(exe) == appExe {
		return exe, AutostartArgs
	}
	for _, p := range []string{filepath.Join(filepath.Dir(exe), appExe), appPath} {
		if exists(p) {
			return p, []string{"--background"}
		}
	}
	return exe, AutostartArgs
}

// onDisk says whether a file is there.
func onDisk(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

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
	exe, args := loginProgram(exe, onDisk)
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key><array>%s</array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>ProcessType</key><string>Interactive</string>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, label, plistArgs(append([]string{exe}, args...)), startupLog(), startupLog())
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

// startupLog is where anything the program prints at log-in goes. Without
// it, a program that cannot start at all leaves no trace anywhere.
func startupLog() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Logs", "Helium Syncer startup.log")
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

// StartsNow says that turning start-at-login on also starts the program
// right away, in the person's desktop session. launchd does that on macOS,
// which matters: a copy started from anywhere else (an SSH session, a
// script) cannot open the Keychain, so it would hold the lock and then fail
// every sync with "this device is not connected".
func StartsNow() bool { return true }

// Autostart reports whether start-at-login is on.
func Autostart() bool {
	_, err := os.Stat(plistPath())
	return err == nil
}

// StartDetached starts the agent in the background. It starts the same
// program that start-at-login would, so what runs now and what runs after
// the next log-in are the same thing.
func StartDetached() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, args := loginProgram(exe, onDisk)
	return exec.Command(exe, args...).Start()
}

// LaunchHelium opens Helium by its bundle id (wherever it is installed).
func LaunchHelium() error {
	return exec.Command("/usr/bin/open", "-b", "net.imput.helium").Run()
}

// OpenBrowser opens a URL in the default browser.
func OpenBrowser(url string) error { return exec.Command("/usr/bin/open", url).Start() }
