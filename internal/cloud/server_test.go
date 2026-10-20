//go:build server

// Integration test against the real Worker code running locally in
// Cloudflare's runtime (wrangler dev, local D1 + R2, MAIL_MODE=log so no
// email is ever sent). Run: go test -tags server ./internal/cloud/
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
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/imrishabhchauhan/helium-syncer/internal/crdt"
	"github.com/imrishabhchauhan/helium-syncer/internal/vault"
)

type devServer struct {
	base  string
	mu    sync.Mutex
	codes map[string]string // email -> latest code from the dev mail log
}

func (s *devServer) code(t *testing.T, email string) string {
	for i := 0; i < 100; i++ {
		s.mu.Lock()
		c := s.codes[email]
		delete(s.codes, email)
		s.mu.Unlock()
		if c != "" {
			return c
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no code logged for %s", email)
	return ""
}

func startServer(t *testing.T) *devServer {
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
	s := &devServer{base: fmt.Sprintf("http://127.0.0.1:%d", port), codes: map[string]string{}}
	re := regexp.MustCompile(`\[dev mail\] to=(\S+) code=(\d{6})`)
	var logMu sync.Mutex
	var logBuf bytes.Buffer
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			line := sc.Text()
			logMu.Lock()
			logBuf.WriteString(line + "\n")
			logMu.Unlock()
			if m := re.FindStringSubmatch(line); m != nil {
				s.mu.Lock()
				s.codes[m[1]] = m[2]
				s.mu.Unlock()
			}
		}
	}()
	for i := 0; i < 200; i++ {
		if res, err := http.Get(s.base + "/v1/health"); err == nil {
			res.Body.Close()
			if res.StatusCode == 200 {
				return s
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	logMu.Lock()
	defer logMu.Unlock()
	t.Fatalf("server did not start:\n%s", logBuf.String())
	return nil
}

type dev struct {
	c  *Client
	dk *vault.DeviceKey
	r  VerifyResult
}

func signIn(t *testing.T, ctx context.Context, s *devServer, email, name string) *dev {
	t.Helper()
	dk, _ := vault.NewDeviceKey()
	c := &Client{Base: s.base}
	if err := c.StartSignIn(ctx, email); err != nil {
		t.Fatal(err)
	}
	r, err := c.Verify(ctx, email, s.code(t, email), name, "windows", dk.Public())
	if err != nil {
		t.Fatal(err)
	}
	return &dev{c, dk, r}
}

func apiCode(err error) string {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

func TestServerEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	s := startServer(t)
	email := "owner@example.com"

	// 1. First device creates the account and the account key.
	mac := signIn(t, ctx, s, email, "MacBook Air")
	if mac.r.Status != "approved" || !mac.r.NewAccount {
		t.Fatalf("first device should be approved for a new account: %+v", mac.r)
	}
	mac.c.Key, _ = vault.NewAccountKey()
	if err := mac.c.StoreOwnKey(ctx, mac.dk); err != nil {
		t.Fatal(err)
	}
	recovery, _ := vault.NewRecoveryKey()
	if err := mac.c.SetRecovery(ctx, recovery); err != nil {
		t.Fatal(err)
	}

	// 2. Second device waits for approval and can read nothing until then.
	win := signIn(t, ctx, s, email, "Windows PC")
	if win.r.Status != "pending" {
		t.Fatalf("second device must wait for approval: %+v", win.r)
	}
	win.c.Key = make([]byte, 32) // even with a key, a pending device is refused
	if _, _, err := win.c.Pull(ctx, "0"); apiCode(err) != "pending" {
		t.Fatalf("a pending device must not read envelopes, got %v", err)
	}
	win.c.Key = nil
	if k, err := win.c.FetchKey(ctx, win.dk); err != nil || k != nil {
		t.Fatalf("no key before approval: %v %v", k, err)
	}
	devs, _, _ := mac.c.Devices(ctx)
	for _, d := range devs {
		if d.Status == "pending" {
			if err := mac.c.Approve(ctx, d); err != nil {
				t.Fatal(err)
			}
		}
	}
	if k, err := win.c.FetchKey(ctx, win.dk); err != nil || !bytes.Equal(k, mac.c.Key) {
		t.Fatalf("approved device should receive the account key: %v", err)
	}

	// 3. Ops travel encrypted between devices.
	ops := []crdt.Op{
		{Type: "p1/bookmark", Key: "g1", Value: []byte(`{"n":"हिंदी 😀","u":"https://x.example/"}`), Stamp: crdt.Stamp{Wall: 1, Device: "mac"}},
		{Type: "p1/tab", Key: "https://y.example/#0", Deleted: true, Stamp: crdt.Stamp{Wall: 2, Device: "mac"}},
	}
	if err := mac.c.Push(ctx, ops); err != nil {
		t.Fatal(err)
	}
	got, cursor, err := win.c.Pull(ctx, "0")
	if err != nil || len(got) != 2 || string(got[0].Value) != string(ops[0].Value) || !got[1].Deleted {
		t.Fatalf("pull: %v %+v", err, got)
	}
	if again, _, _ := win.c.Pull(ctx, cursor); len(again) != 0 {
		t.Fatalf("the cursor must skip what was already received, got %d", len(again))
	}
	// Big batch (initial history upload) splits into several envelopes.
	var many []crdt.Op
	for i := 0; i < 6000; i++ {
		many = append(many, crdt.Op{Type: "p1/visit", Key: fmt.Sprintf("%d|https://site%d.example/page", 13400000000000000+i, i%300),
			Value: []byte(`{"tr":805306368}`), Stamp: crdt.Stamp{Wall: int64(10 + i), Device: "mac"}})
	}
	if err := mac.c.Push(ctx, many); err != nil {
		t.Fatal(err)
	}
	if got, _, err := win.c.Pull(ctx, cursor); err != nil || len(got) != len(many) {
		t.Fatalf("big push: got %d ops, err %v", len(got), err)
	}

	// 4. Blobs: encrypted, verified on download.
	id, err := mac.c.PutBlob(ctx, []byte("extension package bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if b, err := win.c.GetBlob(ctx, id); err != nil || string(b) != "extension package bytes" {
		t.Fatalf("blob: %q %v", b, err)
	}

	// 5. Version gate data.
	mac.c.Announce(ctx, "", "", "155.0.8059.84")
	if v, _ := win.c.Versions(ctx, ""); v["MacBook Air"] != "155.0.8059.84" {
		t.Fatalf("versions: %v", v)
	}

	// 6. Recovery: a new device with only the recovery key.
	laptop := signIn(t, ctx, s, email, "New laptop")
	if laptop.r.Status != "pending" {
		t.Fatal("new device must start pending")
	}
	if _, err := laptop.c.Recover(ctx, "AAAA-BBBB-CCCC-DDDD-EEEE-FFFF-GGGG-HHHH"); err == nil {
		t.Fatal("a wrong recovery key must be refused")
	}
	if k, err := laptop.c.Recover(ctx, strings.ToLower(recovery)); err != nil || !bytes.Equal(k, mac.c.Key) {
		t.Fatalf("recovery failed: %v", err)
	}
	if got, _, err := laptop.c.Pull(ctx, "0"); err != nil || len(got) != 2+len(many) {
		t.Fatalf("recovered device should read everything: %d %v", len(got), err)
	}

	// 7. Removing a device cuts it off.
	if err := mac.c.Remove(ctx, laptop.r.DeviceID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := laptop.c.Pull(ctx, "0"); apiCode(err) != "unauthorized" {
		t.Fatalf("a removed device must be refused, got %v", err)
	}

	// 8. Another account cannot see this account's data.
	stranger := signIn(t, ctx, s, "stranger@example.com", "Stranger")
	stranger.c.Key, _ = vault.NewAccountKey()
	if got, _, err := stranger.c.Pull(ctx, "0"); err != nil || len(got) != 0 {
		t.Fatalf("another account must see nothing: %d %v", len(got), err)
	}
	if _, err := stranger.c.GetBlob(ctx, id); err == nil {
		t.Fatal("another account must not download this account's blob")
	}
	// A device with the wrong key detects it instead of writing garbage.
	wrong := &Client{Base: s.base, Token: win.c.Token, AccountID: win.c.AccountID}
	wrong.Key, _ = vault.NewAccountKey()
	if _, _, err := wrong.Pull(ctx, "0"); !errors.Is(err, vault.ErrDecrypt) {
		t.Fatalf("wrong key must fail to decrypt, got %v", err)
	}
}

func TestServerSignInAbuse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	s := startServer(t)
	c := &Client{Base: s.base}
	email := "victim@example.com"
	if err := c.StartSignIn(ctx, email); err != nil {
		t.Fatal(err)
	}
	real := s.code(t, email)
	wrong := "000000"
	if real == wrong {
		wrong = "111111"
	}
	dk, _ := vault.NewDeviceKey()
	for i := 0; i < 5; i++ {
		if _, err := c.Verify(ctx, email, wrong, "x", "windows", dk.Public()); apiCode(err) != "code_wrong" {
			t.Fatalf("attempt %d: want code_wrong, got %v", i, err)
		}
	}
	if _, err := c.Verify(ctx, email, real, "x", "windows", dk.Public()); apiCode(err) != "code_locked" {
		t.Fatalf("after 5 wrong codes even the right code must be refused, got %v", err)
	}
	for i := 0; i < 4; i++ { // 1 earlier + 4 = 5 allowed per hour
		if err := c.StartSignIn(ctx, email); err != nil {
			t.Fatalf("code request %d: %v", i, err)
		}
	}
	if err := c.StartSignIn(ctx, email); apiCode(err) != "rate_limited" {
		t.Fatalf("the 6th code request in an hour must be rate limited, got %v", err)
	}
	if _, _, err := (&Client{Base: s.base, Token: "made-up"}).Devices(ctx); apiCode(err) != "unauthorized" {
		t.Fatalf("a made-up token must be refused, got %v", err)
	}
}
