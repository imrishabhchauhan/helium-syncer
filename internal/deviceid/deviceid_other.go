//go:build !windows && !darwin

package deviceid

import "errors"

// Get is not supported on this platform yet.
func Get() (string, error) { return "", errors.New("deviceid: unsupported platform") }
