package helium

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Tested lists the Chromium versions (as written to "Last Version") whose
// file formats Helium Syncer has verified end to end. A device on any other
// version does not sync until Helium Syncer is updated (version gate).
var Tested = map[string]bool{
	"155.0.8059.84": true, // Helium 0.19.4.1, Windows + macOS, 20 Oct 2026
}

// DataVersion returns the Chromium version that last wrote this user data.
func DataVersion(userData string) (string, error) {
	b, err := os.ReadFile(filepath.Join(userData, "Last Version"))
	if err != nil {
		return "", fmt.Errorf("helium: cannot read version: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// GateError explains, in words a person can act on, why sync is paused.
type GateError struct{ Message string }

func (e *GateError) Error() string { return e.Message }

// CheckVersions applies the version gate: this device and every other
// device must be on a tested version, and on the same one. Changes stay
// queued while paused, so nothing is lost.
func CheckVersions(local string, peers map[string]string) error {
	if !Tested[local] {
		return &GateError{fmt.Sprintf("Helium on this device is version %s, which Helium Syncer has not been tested with yet. "+
			"Sync is paused until Helium Syncer is updated. Your changes are kept and will sync then.", local)}
	}
	for name, v := range peers {
		if v == local {
			continue
		}
		if !Tested[v] {
			return &GateError{fmt.Sprintf("Helium on %q is version %s, which Helium Syncer has not been tested with yet. "+
				"Sync is paused; update Helium Syncer on that device. Your changes are kept.", name, v)}
		}
		return &GateError{fmt.Sprintf("%q has Helium %s and this device has %s. Update Helium on the older device "+
			"(Settings → About Helium) to keep syncing. Your changes are kept and will sync after the update.", name, v, local)}
	}
	return nil
}
