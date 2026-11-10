package helium

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Tested lists the Chromium versions (as written to "Last Version") that the
// real-Helium tests ran on. It only informs: a version not in it still syncs,
// because each file is checked before it is written (see engine.Run), so a
// Helium update does not stop sync. The daily GitHub workflow runs the
// real-Helium tests on the newest Helium.
var Tested = map[string]bool{
	"155.0.8059.84": true, // Helium 0.19.4.1, Windows + macOS, 20 Oct 2026
	"157.0.8103.50": true, // Helium 0.21.2.1, Windows, 10 Nov 2026 (all real-Helium tests)
}

// DataVersion returns the Chromium version that last wrote this user data.
func DataVersion(userData string) (string, error) {
	b, err := os.ReadFile(filepath.Join(userData, "Last Version"))
	if err != nil {
		return "", fmt.Errorf("helium: cannot read version: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}
