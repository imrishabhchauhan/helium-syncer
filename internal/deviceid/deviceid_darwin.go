package deviceid

import (
	"errors"
	"os/exec"
	"regexp"
)

var uuidRe = regexp.MustCompile(`"IOPlatformUUID"\s*=\s*"([^"]+)"`)

// Get returns IOPlatformUUID from IOPlatformExpertDevice, like
// GetDeterministicMachineSpecificId on macOS.
func Get() (string, error) {
	out, err := exec.Command("/usr/sbin/ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return "", err
	}
	m := uuidRe.FindSubmatch(out)
	if m == nil {
		return "", errors.New("deviceid: IOPlatformUUID not found")
	}
	return string(m[1]), nil
}
