//go:build server

package cloud

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imrishabhchauhan/helium-syncer/internal/crdt"
)

// The server tells a watching computer about the other computer's batch,
// within a moment, and not about its own.
func TestServerWatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	base := startServer(t)
	code := newCode(t)
	mac := &Client{Base: base}
	if _, err := mac.Join(ctx, code, "MacBook Air", "macos", true); err != nil {
		t.Fatal(err)
	}
	win := &Client{Base: base}
	if _, err := win.Join(ctx, code, "Windows PC", "windows", false); err != nil {
		t.Fatal(err)
	}
	// Someone else's sync must never hear about this one.
	stranger := &Client{Base: base}
	if _, err := stranger.Join(ctx, newCode(t), "Other", "windows", true); err != nil {
		t.Fatal(err)
	}

	var macNews, strangerNews atomic.Int32
	wctx, stop := context.WithCancel(ctx)
	defer stop()
	go mac.Watch(wctx, time.Second, func() { macNews.Add(1) })
	go stranger.Watch(wctx, time.Second, func() { strangerNews.Add(1) })
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		for i := 0; i < 100; i++ {
			if cond() {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("%s: never happened", what)
	}
	waitFor("connected", func() bool { return macNews.Load() == 1 && strangerNews.Load() == 1 })
	time.Sleep(1500 * time.Millisecond) // a ping or two go through without news

	op := []crdt.Op{{Type: "p1/tab", Key: "https://x.example/#0", Value: []byte(`{"w":0,"o":"m"}`), Stamp: crdt.Stamp{Wall: 1, Device: "win"}}}
	start := time.Now()
	if err := win.Push(ctx, op); err != nil {
		t.Fatal(err)
	}
	waitFor("news of the Windows batch", func() bool { return macNews.Load() == 2 })
	t.Logf("news arrived %s after the push", time.Since(start).Round(time.Millisecond))

	if err := mac.Push(ctx, op); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if n := macNews.Load(); n != 2 {
		t.Fatalf("the Mac heard about its own batch (%d news)", n)
	}
	if n := strangerNews.Load(); n != 1 {
		t.Fatalf("another sync heard about this one (%d news)", n)
	}
	// Without a valid token, no socket.
	if err := (&Client{Base: base, Token: "nope"}).Watch(ctx, time.Second, func() {}); err == nil {
		t.Fatal("a watch without a valid token was accepted")
	}
}
