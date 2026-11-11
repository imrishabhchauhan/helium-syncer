package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A program with no console - a tray program on Windows, a menu bar
// program on a Mac - prints the reason it died into nowhere. It then
// simply vanishes, and nobody can tell whether it crashed or was closed.
// The reason must land in the log file instead.
func TestACrashLandsInTheFile(t *testing.T) {
	if os.Getenv("HELIUM_SYNCER_CRASH_ON_PURPOSE") == "1" {
		// The child: point crashes at the file, then crash.
		f, err := os.Create(os.Getenv("HELIUM_SYNCER_CRASH_FILE"))
		if err != nil {
			os.Exit(3)
		}
		if err := CatchCrashes(f); err != nil {
			os.Exit(4)
		}
		var m map[string]string
		m["this blows up"] = "on purpose"
		return
	}

	file := filepath.Join(t.TempDir(), "crash.log")
	cmd := exec.Command(os.Args[0], "-test.run=TestACrashLandsInTheFile")
	cmd.Env = append(os.Environ(), "HELIUM_SYNCER_CRASH_ON_PURPOSE=1", "HELIUM_SYNCER_CRASH_FILE="+file)
	// Everything the child would normally print goes nowhere, exactly as it
	// does for a program with no console.
	cmd.Stdout, cmd.Stderr = nil, nil
	err := cmd.Run()
	if err == nil {
		t.Fatal("the child was supposed to crash and did not")
	}

	body, readErr := os.ReadFile(file)
	if readErr != nil {
		t.Fatalf("no crash file at all: %v", readErr)
	}
	got := string(body)
	if !strings.Contains(got, "panic") {
		t.Errorf("the file does not say the program panicked:\n%s", got)
	}
	if !strings.Contains(got, "nil map") {
		t.Errorf("the file does not say what went wrong:\n%s", got)
	}
	if !strings.Contains(got, "goroutine") {
		t.Errorf("the file has no stack to work from:\n%s", got)
	}
}
