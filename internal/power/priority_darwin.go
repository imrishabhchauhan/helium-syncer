package power

import "syscall"

// Efficient: macOS already keeps menu-bar apps cheap; nothing to do.
func Efficient() {}

// Background puts the process in the macOS background band (low CPU, disk
// and network priority) until restore is called.
func Background() (restore func()) {
	const prioDarwinProcess, prioDarwinBG = 4, 0x1000
	if syscall.Setpriority(prioDarwinProcess, 0, prioDarwinBG) != nil {
		return func() {}
	}
	return func() { syscall.Setpriority(prioDarwinProcess, 0, 0) }
}
