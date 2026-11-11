package agent

import (
	"path/filepath"
	"strings"
	"testing"
)

// Windows must never start a console program at log-in: its black terminal
// window stays on the screen until the computer is turned off. The window
// program is installed next to the command-line one, so that is what the
// log-in entry must name.
func TestLoginStartsTheWindowProgramNotTheConsoleOne(t *testing.T) {
	dir := `C:\Programs\Helium Syncer`
	cli := filepath.Join(dir, "helium-syncer.exe")
	app := filepath.Join(dir, appExe)
	there := func(p string) bool { return strings.EqualFold(p, app) }

	gotExe, gotArgs := loginProgram(cli, there)
	if gotExe != app {
		t.Errorf("log-in would start %q, want the window program %q", gotExe, app)
	}
	if len(gotArgs) != 1 || gotArgs[0] != "--background" {
		t.Errorf("log-in arguments %v, want [--background]", gotArgs)
	}
}

// The check above can fail: with no window program next to it, the
// command-line program is all there is, so that is what is started.
func TestLoginFallsBackToTheRunningProgram(t *testing.T) {
	cli := `C:\go-build\abc\helium-syncer.exe`
	gotExe, gotArgs := loginProgram(cli, func(string) bool { return false })
	if gotExe != cli {
		t.Errorf("log-in would start %q, want %q", gotExe, cli)
	}
	if len(gotArgs) != len(AutostartArgs) || gotArgs[0] != AutostartArgs[0] {
		t.Errorf("log-in arguments %v, want %v", gotArgs, AutostartArgs)
	}
}

// The window program must not be redirected to itself with the wrong
// arguments when it is the one asking.
func TestTheWindowProgramKeepsItsOwnArguments(t *testing.T) {
	old := AutostartArgs
	AutostartArgs = []string{"--background"}
	defer func() { AutostartArgs = old }()

	app := `C:\Programs\Helium Syncer\` + appExe
	gotExe, gotArgs := loginProgram(app, func(string) bool { return true })
	if gotExe != app {
		t.Errorf("log-in would start %q, want %q", gotExe, app)
	}
	if len(gotArgs) != 1 || gotArgs[0] != "--background" {
		t.Errorf("log-in arguments %v, want [--background]", gotArgs)
	}
}
