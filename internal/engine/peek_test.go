package engine

import (
	"context"
	"testing"
)

// Peek says "run" only for a change from another computer, and walks past
// this computer's own changes without loading the state again.
func TestPeek(t *testing.T) {
	ctx := context.Background()
	server := NewMemory()
	macUD, winUD := fakeUserData(t, ver), fakeUserData(t, ver)
	mac, win := cfgFor(t, "mac", macUD, server), cfgFor(t, "win", winUD, server)
	LinkProfile(mac.StateDir, mac.DeviceID, "Default")
	if _, err := Run(ctx, mac); err != nil {
		t.Fatal(err)
	}
	rep, err := Run(ctx, win)
	if err != nil {
		t.Fatal(err)
	}
	cursor := rep.Cursor
	if cursor == "" {
		t.Fatal("a clean run should leave a cursor")
	}
	if news, err := Peek(ctx, server, win.StateDir, win.DeviceID, &cursor); err != nil || news {
		t.Fatalf("nothing happened: news=%v err=%v", news, err)
	}
	// Windows's own change: still no news for Windows.
	addBookmark(t, winUD, "fromwin", "From Windows")
	LinkProfile(win.StateDir, win.DeviceID, "Default")
	rep, err = Run(ctx, win)
	if err != nil {
		t.Fatal(err)
	}
	if news, err := Peek(ctx, server, win.StateDir, win.DeviceID, &cursor); err != nil || news {
		t.Fatalf("own change counted as news: news=%v err=%v", news, err)
	}
	if cursor == rep.Cursor {
		t.Fatalf("cursor %q should have moved past the own change", cursor)
	}
	// The Mac's change is news.
	addBookmark(t, macUD, "frommac", "From the Mac")
	if _, err := Run(ctx, mac); err != nil {
		t.Fatal(err)
	}
	if news, err := Peek(ctx, server, win.StateDir, win.DeviceID, &cursor); err != nil || !news {
		t.Fatalf("the Mac's change was missed: news=%v err=%v", news, err)
	}
	// No cursor yet: read from the saved state.
	empty := ""
	if news, err := Peek(ctx, server, win.StateDir, win.DeviceID, &empty); err != nil || !news {
		t.Fatalf("from the saved state, the Mac's change was missed: news=%v err=%v", news, err)
	}
}
