package live

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// dialExtension connects to the live server the way the extension does.
func dialExtension(t *testing.T, s *Server) (*websocket.Conn, context.Context) {
	t.Helper()
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+Path, &websocket.DialOptions{
		HTTPHeader: map[string][]string{"Origin": {"chrome-extension://abcdefghijklmnopabcdefghijklmnop"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.CloseNow() })
	if err := wsjson.Write(ctx, ws, Msg{Type: "hello", Version: Version}); err != nil {
		t.Fatal(err)
	}
	var hello Msg
	if err := wsjson.Read(ctx, ws, &hello); err != nil {
		t.Fatal(err)
	}
	if hello.Type != "hello" {
		t.Fatalf("first answer was %q, want hello", hello.Type)
	}
	return ws, ctx
}

// On battery the extension must be told, so it can stop holding itself
// awake. Holding a browser worker awake on a laptop costs power and buys
// nothing: the app already waits minutes between syncs on battery.
func TestABrowserIsToldWhenThisComputerIsOnBattery(t *testing.T) {
	s := New(t.TempDir())
	s.AllowOrigin = OriginOf("abcdefghijklmnopabcdefghijklmnop")
	ws, ctx := dialExtension(t, s)

	s.SetMode(true)
	var m Msg
	if err := wsjson.Read(ctx, ws, &m); err != nil {
		t.Fatal(err)
	}
	if m.Type != "power" || m.Mode != ModeBattery {
		t.Fatalf("got %q/%q, want power/%s", m.Type, m.Mode, ModeBattery)
	}

	// Plugged in again, it must be told to come back.
	s.SetMode(false)
	if err := wsjson.Read(ctx, ws, &m); err != nil {
		t.Fatal(err)
	}
	if m.Type != "power" || m.Mode != ModePlugged {
		t.Fatalf("got %q/%q, want power/%s", m.Type, m.Mode, ModePlugged)
	}
}

// A browser that connects while this computer is already on battery must
// be told at once, not at the next change of power. Otherwise an extension
// that started during a long battery stretch holds itself awake for hours.
func TestABrowserThatConnectsOnBatteryIsToldAtOnce(t *testing.T) {
	s := New(t.TempDir())
	s.AllowOrigin = OriginOf("abcdefghijklmnopabcdefghijklmnop")
	s.SetMode(true)

	ws, ctx := dialExtension(t, s)
	var m Msg
	if err := wsjson.Read(ctx, ws, &m); err != nil {
		t.Fatal(err)
	}
	if m.Type != "power" || m.Mode != ModeBattery {
		t.Fatalf("a browser connecting on battery got %q/%q", m.Type, m.Mode)
	}
	_ = ws
}

// Saying the same thing again must not send a message: the power state is
// read every few minutes and almost never changes.
func TestTheSamePowerStateIsNotSentAgain(t *testing.T) {
	s := New(t.TempDir())
	if s.Mode() != "" {
		t.Fatalf("a new server already has a mode: %q", s.Mode())
	}
	s.SetMode(true)
	if s.Mode() != ModeBattery {
		t.Fatalf("mode is %q, want %q", s.Mode(), ModeBattery)
	}
	s.AllowOrigin = OriginOf("abcdefghijklmnopabcdefghijklmnop")
	ws, ctx := dialExtension(t, s)
	var first Msg
	if err := wsjson.Read(ctx, ws, &first); err != nil {
		t.Fatal(err)
	}

	s.SetMode(true) // the same again
	s.SetMode(false)
	var m Msg
	if err := wsjson.Read(ctx, ws, &m); err != nil {
		t.Fatal(err)
	}
	if m.Mode != ModePlugged {
		t.Errorf("the same state was sent again: got %q before %q", m.Mode, ModePlugged)
	}
}
