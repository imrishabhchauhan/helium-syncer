// Package detect finds Helium's user data folders on this machine by
// looking where each OS keeps them, never assuming a single path.
package detect

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/imrishabhchauhan/helium-syncer/internal/helium"
	"github.com/imrishabhchauhan/helium-syncer/internal/profiles"
)

// Install is one Helium user data folder.
type Install struct {
	UserData string
	Version  string // Chromium version that last wrote it
	Profiles []profiles.Profile
}

// Candidates are the folders Helium uses on this OS (first = default).
func Candidates() []string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			local = filepath.Join(home, "AppData", "Local")
		}
		return []string{filepath.Join(local, "imput", "Helium", "User Data")}
	case "darwin":
		as := filepath.Join(home, "Library", "Application Support")
		return []string{filepath.Join(as, "net.imput.helium"), filepath.Join(as, "imput", "Helium"), filepath.Join(as, "Helium")}
	default:
		cfg := os.Getenv("XDG_CONFIG_HOME")
		if cfg == "" {
			cfg = filepath.Join(home, ".config")
		}
		return []string{filepath.Join(cfg, "net.imput.helium"), filepath.Join(cfg, "imput", "Helium")}
	}
}

// Find returns every Helium user data folder that exists.
func Find() []Install {
	var out []Install
	seen := map[string]bool{}
	for _, c := range Candidates() {
		if seen[c] {
			continue
		}
		seen[c] = true
		if in, ok := Inspect(c); ok {
			out = append(out, in)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].Profiles) > len(out[j].Profiles) })
	return out
}

// Inspect describes one folder, if it is a Helium user data folder.
func Inspect(dir string) (Install, bool) {
	if _, err := os.Stat(filepath.Join(dir, "Local State")); err != nil {
		return Install{}, false
	}
	in := Install{UserData: dir}
	in.Version, _ = helium.DataVersion(dir)
	in.Profiles, _ = profiles.List(dir)
	return in, true
}
