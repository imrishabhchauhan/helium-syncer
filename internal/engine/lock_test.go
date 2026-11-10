package engine

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func writeLock(t *testing.T, path string, pid int) {
	t.Helper()
	line := fmt.Sprintf("%d %s\n", pid, time.Now().UTC().Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A lock left by a program that has ended must not block the next sync for
// the full stale time (seen twice: a stopped agent blocked syncing for 30
// minutes). A lock held by a running program must still block.
func TestLockLeftByAnEndedProgramIsCleared(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "run.lock")

	ended := exec.Command("true")
	if runtime.GOOS == "windows" {
		ended = exec.Command("cmd", "/c", "exit 0")
	}
	if err := ended.Run(); err != nil {
		t.Fatal(err)
	}
	writeLock(t, lock, ended.Process.Pid)
	unlock, err := lockState(dir)
	if err != nil {
		t.Fatalf("the lock of ended process %d still blocks: %v", ended.Process.Pid, err)
	}
	unlock()

	running := exec.Command(os.Args[0], "-test.run=^TestHelperSleep$")
	running.Env = append(os.Environ(), "HS_SLEEP=1")
	if err := running.Start(); err != nil {
		t.Fatal(err)
	}
	defer running.Process.Kill()
	writeLock(t, lock, running.Process.Pid)
	if _, err := lockState(dir); err != ErrBusy {
		t.Fatalf("a lock held by a running process was taken: %v", err)
	}
}

func TestHelperSleep(t *testing.T) {
	if os.Getenv("HS_SLEEP") == "" {
		t.Skip("helper process for TestLockLeftByAnEndedProgramIsCleared")
	}
	time.Sleep(30 * time.Second)
}
