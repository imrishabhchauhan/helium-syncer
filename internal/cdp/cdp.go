// Package cdp is a minimal Chrome DevTools Protocol client, used only by the
// test kit to drive throwaway Helium instances.
package cdp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// Target is one entry from /json/list.
type Target struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
	URL   string `json:"url"`
	WSURL string `json:"webSocketDebuggerUrl"`
}

// Targets lists debuggable targets of the instance on port.
func Targets(ctx context.Context, port int) ([]Target, error) {
	var t []Target
	return t, getJSON(ctx, fmt.Sprintf("http://127.0.0.1:%d/json/list", port), &t)
}

// BrowserWSURL returns the browser-level websocket URL.
func BrowserWSURL(ctx context.Context, port int) (string, error) {
	var v struct {
		WS string `json:"webSocketDebuggerUrl"`
	}
	if err := getJSON(ctx, fmt.Sprintf("http://127.0.0.1:%d/json/version", port), &v); err != nil {
		return "", err
	}
	return v.WS, nil
}

func getJSON(ctx context.Context, url string, out any) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	return json.NewDecoder(res.Body).Decode(out)
}

// Client is one websocket session to a target.
type Client struct {
	conn    *websocket.Conn
	nextID  atomic.Int64
	mu      sync.Mutex
	pending map[int64]chan reply
	done    chan struct{}
}

type reply struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Dial connects to a target websocket URL.
func Dial(ctx context.Context, wsURL string) (*Client, error) {
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(64 << 20)
	c := &Client{conn: conn, pending: map[int64]chan reply{}, done: make(chan struct{})}
	go c.readLoop()
	return c, nil
}

func (c *Client) readLoop() {
	defer close(c.done)
	for {
		_, data, err := c.conn.Read(context.Background())
		if err != nil {
			return
		}
		var msg struct {
			ID int64 `json:"id"`
			reply
		}
		if json.Unmarshal(data, &msg) != nil || msg.ID == 0 {
			continue // events are ignored
		}
		c.mu.Lock()
		ch := c.pending[msg.ID]
		delete(c.pending, msg.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- msg.reply
		}
	}
}

// Call sends one CDP command and waits for its result.
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	ch := make(chan reply, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	msg, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err := c.conn.Write(ctx, websocket.MessageText, msg); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		if r.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, r.Error.Message)
		}
		return r.Result, nil
	case <-c.done:
		return nil, fmt.Errorf("%s: connection closed", method)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Eval evaluates a JavaScript expression (awaiting promises) and decodes the
// returned value into out.
func (c *Client) Eval(ctx context.Context, expr string, out any) error {
	raw, err := c.Call(ctx, "Runtime.evaluate", map[string]any{
		"expression": expr, "awaitPromise": true, "returnByValue": true,
	})
	if err != nil {
		return err
	}
	var r struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Exception *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return err
	}
	if r.Exception != nil {
		return fmt.Errorf("js: %s %s", r.Exception.Text, r.Exception.Exception.Description)
	}
	if out == nil || len(r.Result.Value) == 0 {
		return nil
	}
	return json.Unmarshal(r.Result.Value, out)
}

// Close closes the session.
func (c *Client) Close() error {
	err := c.conn.Close(websocket.StatusNormalClosure, "")
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
	}
	return err
}
