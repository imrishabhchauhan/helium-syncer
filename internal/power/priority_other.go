//go:build !windows && !darwin

package power

// Efficient does nothing on this system.
func Efficient() {}

// Background does nothing on this system.
func Background() (restore func()) { return func() {} }
