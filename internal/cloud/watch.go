package cloud

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// ErrNoWatch means the server does not offer /v1/watch (an older server):
// the caller keeps asking at intervals instead.
var ErrNoWatch = errors.New("the sync server cannot tell this computer about changes; asking at intervals instead")

// WatchPing is how often Watch pings the server. Cloudflare closes a
// connection after 400 s without traffic; a protocol ping is answered by
// Cloudflare itself, costs nothing and wakes nothing on the server. 12 an
// hour is far less network wake-up on a laptop than asking every few seconds.
const WatchPing = 270 * time.Second

// Watch opens a WebSocket on which the server says when another computer of
// this sync sent changes, and calls news for each, and once at the start
// (changes may have arrived while it was not connected). It returns when ctx
// ends or the connection fails; the caller decides when to try again.
func (c *Client) Watch(ctx context.Context, ping time.Duration, news func()) error {
	if c.Token == "" {
		return errors.New("this device is not connected to a sync yet")
	}
	u := strings.TrimRight(c.Base, "/")
	u = strings.Replace(strings.Replace(u, "https://", "wss://", 1), "http://", "ws://", 1) + "/v1/watch"
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	// No client-wide timeout here: it would end the connection itself.
	ws, res, err := websocket.Dial(dctx, u, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + c.Token}},
	})
	cancel()
	if err != nil {
		if res != nil && res.StatusCode == http.StatusNotFound {
			return ErrNoWatch
		}
		return err
	}
	defer ws.CloseNow()
	ws.SetReadLimit(4096)
	rctx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		t := time.NewTicker(ping)
		defer t.Stop()
		for {
			select {
			case <-rctx.Done():
				return
			case <-t.C:
				pctx, done := context.WithTimeout(rctx, 30*time.Second)
				err := ws.Ping(pctx)
				done()
				if err != nil {
					ws.CloseNow() // the read below fails, and Watch returns
					return
				}
			}
		}
	}()
	news()
	for {
		if _, _, err := ws.Read(rctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		news()
	}
}
