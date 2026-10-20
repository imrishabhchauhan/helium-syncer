// Package deviceid returns the machine id Chromium mixes into preference
// MACs (services/preferences/tracked/device_id_*.cc).
package deviceid

import "golang.org/x/sys/windows"

// Get returns the machine SID string (S-1-5-21-...), looked up from the
// computer name exactly like GetDeterministicMachineSpecificId on Windows.
func Get() (string, error) {
	name, err := windows.ComputerName()
	if err != nil {
		return "", err
	}
	sid, _, _, err := windows.LookupSID("", name)
	if err != nil {
		return "", err
	}
	return sid.String(), nil
}
