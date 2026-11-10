//go:build !windows

package engine

import (
	"errors"
	"syscall"
)

// processAlive reports whether a process with this id is still running.
// Signal 0 checks without sending anything; "not permitted" means it exists.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
