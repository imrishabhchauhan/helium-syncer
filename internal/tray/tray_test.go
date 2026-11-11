package tray

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLine(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	ok := View{Connected: true, SetupDone: true}
	cases := []struct {
		v         View
		want      string
		attention bool
	}{
		{View{}, "Not set up yet", true},
		{View{Connected: true}, "Finish setup to start syncing", true},
		{with(ok, func(v *View) { v.Syncing = true; v.LastError = "x" }), "Syncing…", false},
		{with(ok, func(v *View) { v.LastError = "boom" }), "Problem syncing · open Helium Syncer", true},
		{with(ok, func(v *View) { v.Paused = "update Helium" }), "Paused · open Helium Syncer", true},
		{with(ok, func(v *View) { v.HeliumOpen = true }), "Syncs after you quit Helium", false},
		// With the helper extension connected, bookmarks and open tabs do
		// sync while Helium runs: saying "after you quit" here told the
		// person the opposite of what was happening.
		{with(ok, func(v *View) { v.HeliumOpen = true; v.Live = true }), "Syncing while Helium is open", false},
		// The reason for waiting comes from the agent, so the tray and the
		// window say the same thing.
		{with(ok, func(v *View) { v.PowerNote = "On battery: syncs when you plug in." }), "On battery: syncs when you plug in", false},
		{with(ok, func(v *View) { v.PowerNote = "Battery is low: syncs when you plug in." }), "Battery is low: syncs when you plug in", false},
		{ok, "Waiting for the first sync", false},
		{with(ok, func(v *View) { v.LastSync = now.Add(-20 * time.Second) }), "Up to date · synced just now", false},
		{with(ok, func(v *View) { v.LastSync = now.Add(-1 * time.Minute) }), "Up to date · synced 1 minute ago", false},
		{with(ok, func(v *View) { v.LastSync = now.Add(-5 * time.Hour) }), "Up to date · synced 5 hours ago", false},
		{with(ok, func(v *View) { v.LastSync = now.Add(-49 * time.Hour) }), "Up to date · synced 2 days ago", false},
	}
	for _, c := range cases {
		got, att := Line(c.v, now)
		if got != c.want || att != c.attention {
			t.Errorf("%+v: got %q/%v, want %q/%v", c.v, got, att, c.want, c.attention)
		}
	}
}

func with(v View, f func(*View)) View { f(&v); return v }

func TestIcons(t *testing.T) {
	for _, withBg := range []bool{true, false} {
		img := mark(32, blue, white, withBg)
		if _, err := png.Decode(bytes.NewReader(pngBytes(img))); err != nil {
			t.Fatal(err)
		}
		// Corners transparent without a background, centre empty (inside
		// the ring), and the ring itself drawn.
		if a := img.NRGBAAt(16, 16).A; withBg && img.NRGBAAt(16, 16).R != blue.R || !withBg && a != 0 {
			t.Errorf("withBg=%v: centre should be background, got %+v", withBg, img.NRGBAAt(16, 16))
		}
		if !withBg && img.NRGBAAt(0, 0).A != 0 {
			t.Error("template icon corner is not transparent")
		}
		drawn := 0
		for y := 0; y < 32; y++ {
			for x := 0; x < 32; x++ {
				c := img.NRGBAAt(x, y)
				if withBg && c.R > 200 && c.B > 200 || !withBg && c.A > 200 {
					drawn++
				}
			}
		}
		if drawn < 60 || drawn > 500 {
			t.Errorf("withBg=%v: mark covers %d pixels, looks wrong", withBg, drawn)
		}
	}
	ico := icoBytes(mark(16, blue, white, true), mark(32, blue, white, true))
	if binary.LittleEndian.Uint16(ico[2:]) != 1 || binary.LittleEndian.Uint16(ico[4:]) != 2 {
		t.Fatal("bad ico header")
	}
	size := binary.LittleEndian.Uint32(ico[6+16+8:])
	off := binary.LittleEndian.Uint32(ico[6+16+12:])
	if int(off+size) != len(ico) {
		t.Fatalf("ico entries do not add up: %d+%d vs %d", off, size, len(ico))
	}
	if want := 40 + 32*32*4 + 4*32; int(size) != want {
		t.Fatalf("32px image is %d bytes, want %d", size, want)
	}
}

// HS_ICON_PREVIEW=<folder> writes the icons large, to look at them.
func TestWriteIconPreview(t *testing.T) {
	dir := os.Getenv("HS_ICON_PREVIEW")
	if dir == "" {
		t.Skip("set HS_ICON_PREVIEW to a folder")
	}
	for name, img := range map[string]*image.NRGBA{
		"tray-blue.png":  mark(256, blue, white, true),
		"tray-amber.png": mark(256, amber, white, true),
		"menubar.png":    mark(256, black, black, false),
		"tray-16.png":    mark(16, blue, white, true),
		"tray-32.png":    mark(32, blue, white, true),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), pngBytes(img), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Waking up every few seconds to redraw a line nobody is reading costs
// battery on a laptop. Plugged in, or while a sync is running, it stays
// quick.
func TestItWaitsLongerOnBattery(t *testing.T) {
	ok := View{Connected: true, SetupDone: true}
	cases := []struct {
		what string
		v    View
		want time.Duration
	}{
		{"plugged in", ok, 5 * time.Second},
		{"on battery, nothing happening", with(ok, func(v *View) { v.OnBattery = true }), 30 * time.Second},
		{"on battery, syncing", with(ok, func(v *View) { v.OnBattery = true; v.Syncing = true }), 5 * time.Second},
	}
	for _, c := range cases {
		if got := Every(c.v); got != c.want {
			t.Errorf("%s: waits %v, want %v", c.what, got, c.want)
		}
	}
}
