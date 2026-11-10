//go:build !darwin

package testkit

import "os/exec"

// quitLikeTheSystem is only needed on macOS; elsewhere closing the browser
// through the debugging protocol keeps the session (tested on Windows).
func quitLikeTheSystem(*exec.Cmd) bool { return false }
