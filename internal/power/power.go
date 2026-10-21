// Package power tells whether this computer runs on battery, so Helium
// Syncer can do less work on laptops.
package power

import (
	"os"
	"strconv"
	"strings"
)

// State of the power supply. Desktops have HasBattery false.
type State struct {
	HasBattery bool
	OnBattery  bool
	Saver      bool // Battery Saver (Windows) or Low Power Mode (macOS) is on
	Percent    int  // battery charge, -1 when unknown
}

// Low means the battery is low or the person asked the computer to save
// power: Helium Syncer then does only what cannot wait.
func (s State) Low() bool {
	return s.OnBattery && (s.Saver || (s.Percent >= 0 && s.Percent <= 20))
}

// Read returns the current state. On any error it reports "plugged in", so
// a broken reading never stops syncing on a desktop.
func Read() State {
	if f := os.Getenv("HELIUM_SYNCER_FAKE_POWER"); f != "" { // tests on desktops only
		return fake(f)
	}
	s, err := read()
	if err != nil {
		return State{Percent: -1}
	}
	return s
}

// fake reads HELIUM_SYNCER_FAKE_POWER: "plugged:80", "battery:70",
// "battery:15:saver" or "desktop".
func fake(f string) State {
	parts := strings.Split(f, ":")
	s := State{Percent: -1}
	if parts[0] == "desktop" {
		return s
	}
	s.HasBattery, s.OnBattery = true, parts[0] == "battery"
	if len(parts) > 1 {
		s.Percent, _ = strconv.Atoi(parts[1])
	}
	s.Saver = len(parts) > 2 && parts[2] == "saver"
	return s
}
