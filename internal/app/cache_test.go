package app

import (
	"strings"
	"testing"
	"time"
)

func TestLoadCachedSeesChanges(t *testing.T) {
	t.Setenv("HELIUM_SYNCER_HOME", t.TempDir())
	c, err := LoadCached()
	if err != nil || c.DeviceName != "" {
		t.Fatalf("empty config: %+v %v", c, err)
	}
	c.DeviceName = "One"
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if got, _ := LoadCached(); got.DeviceName != "One" {
		t.Fatalf("first read: %q", got.DeviceName)
	}
	got, _ := LoadCached()
	got.DeviceName = "changed in memory only"
	if again, _ := LoadCached(); again.DeviceName != "One" {
		t.Fatal("callers must get a copy, not the cache itself")
	}
	time.Sleep(20 * time.Millisecond) // a new modification time
	c.DeviceName = "Two"
	c.Battery = "plugged"
	c.Save()
	if got, _ := LoadCached(); got.DeviceName != "Two" || got.Battery != "plugged" {
		t.Fatalf("change not seen: %+v", got)
	}
}

func TestMacModel(t *testing.T) {
	for in, want := range map[string]string{
		"Rishabh's MacBook Air":      "MacBook Air",
		"Rishabh\u2019s MacBook Pro": "MacBook Pro",
		"MacBook Air":                "MacBook Air",
		"Studio":                     "Studio",
		"Ann's ":                     "Ann's",
	} {
		if got := macModel(strings.TrimSpace(in)); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}
