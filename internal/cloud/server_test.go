//go:build server

// Integration test against the real Worker code running locally in
// Cloudflare's runtime (wrangler dev with local D1 + R2).
// Run: go test -tags server ./internal/cloud/
package cloud

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/imrishabhchauhan/helium-syncer/internal/crdt"
	"github.com/imrishabhchauhan/helium-syncer/internal/vault"
)

func startServer(t *testing.T) string {
	root, _ := filepath.Abs("../../server")
	wrangler := filepath.Join(root, "node_modules", "wrangler", "bin", "wrangler.js")
	if _, err := os.Stat(wrangler); err != nil {
		t.Skip("run npm install in server/ first")
	}
	persist := t.TempDir()
	schema := exec.Command("node", wrangler, "d1", "execute", "helium-syncer-dev", "--env", "dev", "--local",
		"--persist-to", persist, "--file", "schema.sql", "--yes")
	schema.Dir = root
	if out, err := schema.CombinedOutput(); err != nil {
		t.Fatalf("schema: %v\n%s", err, out)
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	cmd := exec.Command("node", wrangler, "dev", "--env", "dev", "--local", "--persist-to", persist,
		"--port", fmt.Sprint(port), "--ip", "127.0.0.1", "--inspector-port", "0", "--show-interactive-dev-session=false")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "WRANGLER_SEND_METRICS=false", "CI=1")
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	var logMu sync.Mutex
	var logBuf bytes.Buffer
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			logMu.Lock()
			logBuf.WriteString(sc.Text() + "\n")
			logMu.Unlock()
		}
	}()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	for i := 0; i < 200; i++ {
		if res, err := http.Get(base + "/v1/health"); err == nil {
			res.Body.Close()
			if res.StatusCode == 200 {
				return base
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	logMu.Lock()
	defer logMu.Unlock()
	t.Fatalf("server did not start:\n%s", logBuf.String())
	return ""
}

func apiCode(err error) string {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

func newCode(t *testing.T) string {
	c, err := vault.NewSyncCode()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestServerEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	base := startServer(t)
	code := newCode(t)

	// 1. The first device starts the sync; the second joins with the code.
	mac := &Client{Base: base}
	r, err := mac.Join(ctx, code, "MacBook Air", "macos", true)
	if err != nil || !r.Created {
		t.Fatalf("first device should start the sync: %+v %v", r, err)
	}
	win := &Client{Base: base}
	typed := strings.ToLower(strings.ReplaceAll(code, "-", " ")) // as a person might type it
	if r, err := win.Join(ctx, typed, "Windows PC", "windows", false); err != nil || r.Created {
		t.Fatalf("second device should join the existing sync: %+v %v", r, err)
	}
	if !bytes.Equal(win.Key, mac.Key) || win.AccountID != mac.AccountID {
		t.Fatal("both devices must derive the same account and key")
	}

	// 2. A mistyped (other valid) code must not silently start an empty sync.
	if _, err := (&Client{Base: base}).Join(ctx, newCode(t), "Laptop", "windows", false); apiCode(err) != "no_sync" {
		t.Fatalf("an unknown code must be refused, got %v", err)
	}
	// Someone who knows the account id but not the code is refused.
	other, _ := vault.Derive(newCode(t))
	_, err = mac.do(ctx, "POST", "/v1/join", map[string]any{"account_id": mac.AccountID, "auth": other.Auth, "create": false,
		"device": map[string]string{"name": "x", "platform": "windows"}}, nil)
	if apiCode(err) != "wrong_code" {
		t.Fatalf("a wrong auth secret must be refused, got %v", err)
	}

	// 3. Ops travel encrypted between devices.
	ops := []crdt.Op{
		{Type: "p1/bookmark", Key: "g1", Value: []byte(`{"n":"हिंदी 😀","u":"https://x.example/"}`), Stamp: crdt.Stamp{Wall: 1, Device: "mac"}},
		{Type: "p1/tab", Key: "https://y.example/#0", Deleted: true, Stamp: crdt.Stamp{Wall: 2, Device: "mac"}},
	}
	if err := mac.Push(ctx, ops); err != nil {
		t.Fatal(err)
	}
	got, cursor, err := win.Pull(ctx, "0")
	if err != nil || len(got) != 2 || string(got[0].Value) != string(ops[0].Value) || !got[1].Deleted {
		t.Fatalf("pull: %v %+v", err, got)
	}
	if again, _, _ := win.Pull(ctx, cursor); len(again) != 0 {
		t.Fatalf("the cursor must skip what was already received, got %d", len(again))
	}
	// A big first upload (lots of history) splits into several envelopes.
	var many []crdt.Op
	for i := 0; i < 6000; i++ {
		many = append(many, crdt.Op{Type: "p1/visit", Key: fmt.Sprintf("%d|https://site%d.example/page", 13400000000000000+i, i%300),
			Value: []byte(`{"tr":805306368}`), Stamp: crdt.Stamp{Wall: int64(10 + i), Device: "mac"}})
	}
	if err := mac.Push(ctx, many); err != nil {
		t.Fatal(err)
	}
	if got, _, err := win.Pull(ctx, cursor); err != nil || len(got) != len(many) {
		t.Fatalf("big push: got %d ops, err %v", len(got), err)
	}

	// 4. Blobs: encrypted, checked on download.
	id, err := mac.PutBlob(ctx, []byte("extension package bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if b, err := win.GetBlob(ctx, id); err != nil || string(b) != "extension package bytes" {
		t.Fatalf("blob: %q %v", b, err)
	}

	// 5. Version gate data.
	if err := mac.Announce(ctx, "", "", "155.0.8059.84"); err != nil {
		t.Fatal(err)
	}
	if v, _ := win.Versions(ctx, ""); v["MacBook Air"] != "155.0.8059.84" {
		t.Fatalf("versions: %v", v)
	}

	// 6. Removing a device cuts it off; it can come back with the code.
	laptop := &Client{Base: base}
	lr, err := laptop.Join(ctx, code, "Old laptop", "windows", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := mac.Remove(ctx, lr.DeviceID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := laptop.Pull(ctx, "0"); apiCode(err) != "unauthorized" {
		t.Fatalf("a removed device must be refused, got %v", err)
	}
	if _, err := laptop.Join(ctx, code, "Old laptop", "windows", false); err != nil {
		t.Fatalf("entering the code again must reconnect: %v", err)
	}

	// 7. Another sync cannot see this one.
	stranger := &Client{Base: base}
	if _, err := stranger.Join(ctx, newCode(t), "Stranger", "windows", true); err != nil {
		t.Fatal(err)
	}
	if got, _, err := stranger.Pull(ctx, "0"); err != nil || len(got) != 0 {
		t.Fatalf("another sync must see nothing: %d %v", len(got), err)
	}
	if _, err := stranger.GetBlob(ctx, id); err == nil {
		t.Fatal("another sync must not download this sync's blob")
	}
	// A device with the wrong key notices instead of writing garbage.
	wrong := &Client{Base: base, Token: win.Token, AccountID: win.AccountID, Key: other.Key}
	if _, _, err := wrong.Pull(ctx, "0"); !errors.Is(err, vault.ErrDecrypt) {
		t.Fatalf("a wrong key must fail to decrypt, got %v", err)
	}
}

func TestServerJoinAbuse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	base := startServer(t)
	c := &Client{Base: base}
	_, err := c.do(ctx, "POST", "/v1/join", map[string]any{"account_id": "zz", "auth": "zz", "create": true,
		"device": map[string]string{"name": "x", "platform": "windows"}}, nil)
	if apiCode(err) != "bad_code" {
		t.Fatalf("malformed ids must be refused, got %v", err)
	}
	// Guessing codes: after 30 attempts an hour from one address, refused.
	var last error
	for i := 0; i < 31; i++ {
		_, last = (&Client{Base: base}).Join(ctx, newCode(t), "guesser", "windows", false)
	}
	if apiCode(last) != "rate_limited" {
		t.Fatalf("the 31st join attempt in an hour must be rate limited, got %v", last)
	}
	if _, _, err := (&Client{Base: base, Token: "made-up"}).Devices(ctx); apiCode(err) != "unauthorized" {
		t.Fatalf("a made-up token must be refused, got %v", err)
	}
}
