package testkit

import (
	"os/exec"
	"syscall"
)

// quitLikeTheSystem quits Helium the way macOS does at logout: a polite stop
// signal, which Helium answers by quitting and keeping its windows for next
// time, like Cmd+Q. Closing the browser through the debugging protocol
// closes the windows one by one first, and a Mac app forgets windows that
// were closed (seen on the newest Helium for Mac: 4 tabs opened, 1 saved).
func quitLikeTheSystem(cmd *exec.Cmd) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}
	return cmd.Process.Signal(syscall.SIGTERM) == nil
}
