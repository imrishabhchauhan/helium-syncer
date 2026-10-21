package tray

import (
	"fmt"
	"time"
)

// View is what the tray needs to know to describe the sync.
type View struct {
	Connected  bool
	SetupDone  bool
	HeliumOpen bool
	Syncing    bool
	LastSync   time.Time
	LastError  string
	Paused     string
	Elsewhere  bool // the command-line agent runs the sync, not this app
}

// Line is the short status shown in the menu and tooltip, and whether it
// needs the person's attention (amber icon).
func Line(v View, now time.Time) (text string, attention bool) {
	switch {
	case !v.Connected:
		return "Not set up yet", true
	case !v.SetupDone:
		return "Finish setup to start syncing", true
	case v.Syncing:
		return "Syncing…", false
	case v.LastError != "":
		return "Problem syncing · open Helium Syncer", true
	case v.Paused != "":
		return "Paused · open Helium Syncer", true
	case v.Elsewhere:
		return "Syncing from the command-line agent", false
	case v.HeliumOpen:
		return "Syncs after you quit Helium", false
	case v.LastSync.IsZero():
		return "Waiting for the first sync", false
	}
	return "Up to date · synced " + ago(now.Sub(v.LastSync)), false
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute") + " ago"
	case d < 24*time.Hour:
		return plural(int(d/time.Hour), "hour") + " ago"
	}
	return plural(int(d/(24*time.Hour)), "day") + " ago"
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}
