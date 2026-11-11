package agent

import (
	"path/filepath"
	"strings"
	"testing"
)

// The command-line program has no icon and no window, so a person whose
// computer starts that one at log-in cannot see whether sync works at all.
// When the menu-bar app is installed, that is what must start.
func TestLoginStartsTheMenuBarApp(t *testing.T) {
	cli := "/Users/x/bin/helium-syncer"
	gotExe, gotArgs := loginProgram(cli, func(p string) bool { return p == appPath })
	if gotExe != appPath {
		t.Errorf("log-in would start %q, want the menu-bar app %q", gotExe, appPath)
	}
	if len(gotArgs) != 1 || gotArgs[0] != "--background" {
		t.Errorf("log-in arguments %v, want [--background]", gotArgs)
	}

	// Inside the app bundle, the one next to it wins (a copy that is not in
	// /Applications still starts its own menu bar app).
	inBundle := "/Volumes/Test/Helium Syncer.app/Contents/MacOS/helium-syncer"
	next := filepath.Join(filepath.Dir(inBundle), appExe)
	if gotExe, _ := loginProgram(inBundle, func(p string) bool { return p == next || p == appPath }); gotExe != next {
		t.Errorf("log-in would start %q, want the app next to it %q", gotExe, next)
	}
}

// The check above can fail: with no menu-bar app anywhere, the command-line
// program is all there is.
func TestLoginFallsBackToTheRunningProgram(t *testing.T) {
	cli := "/Users/x/bin/helium-syncer"
	gotExe, gotArgs := loginProgram(cli, func(string) bool { return false })
	if gotExe != cli {
		t.Errorf("log-in would start %q, want %q", gotExe, cli)
	}
	if len(gotArgs) != len(AutostartArgs) || gotArgs[0] != AutostartArgs[0] {
		t.Errorf("log-in arguments %v, want %v", gotArgs, AutostartArgs)
	}
}

// Anything the program prints at log-in must land in a file. Without one,
// a program that cannot start leaves no trace to look at.
func TestStartupLogHasAPath(t *testing.T) {
	p := startupLog()
	if !strings.HasSuffix(p, ".log") || !strings.Contains(p, "Logs") {
		t.Errorf("startup log path is %q", p)
	}
}
