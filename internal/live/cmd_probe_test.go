package live

import (
	"context"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestProbeServer is not a test of the code; it is a way to run a bare live
// server against a real browser and see whether the companion extension
// connects. It does nothing unless HELIUM_SYNCER_PROBE is set, so an
// ordinary `go test ./...` skips it.
//
//	HELIUM_SYNCER_PROBE=127.0.0.1:47811 go test ./internal/live -run TestProbeServer -v -timeout 5m
//
// It prints a line for every connection and every tree a browser reports,
// then stops when HELIUM_SYNCER_PROBE_SECONDS have passed.
func TestProbeServer(t *testing.T) {
	addr := os.Getenv("HELIUM_SYNCER_PROBE")
	if addr == "" {
		t.Skip("set HELIUM_SYNCER_PROBE to run the probe against a real browser")
	}
	seconds := 60
	if s := os.Getenv("HELIUM_SYNCER_PROBE_SECONDS"); s != "" {
		var n int
		if _, err := fmtSscan(s, &n); err == nil && n > 0 {
			seconds = n
		}
	}
	userData := os.Getenv("HELIUM_SYNCER_PROBE_USERDATA")

	s := New(userData)
	s.AllowOrigin = func(origin string) bool {
		t.Logf("connection from origin %q", origin)
		return origin == "chrome-extension://"+os.Getenv("HELIUM_SYNCER_PROBE_EXTENSION")
	}
	s.Changed = func(profile string) {
		f, ok := s.ReadBookmarks(profile)
		if !ok {
			t.Logf("tree reported for %s but it could not be read", profile)
			return
		}
		n := 0
		walkFile(f, func(_ map[string]any, url, _ string) {
			if url != "" {
				n++
			}
		})
		t.Logf("BROWSER CONNECTED: profile %s, %d bookmark(s)", profile, n)
	}

	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("cannot listen on %s: %v (is the Helium Syncer agent still running?)", addr, err)
	}
	defer l.Close()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Logf("request %s %s origin=%q", r.Method, r.URL.Path, r.Header.Get("Origin"))
		if r.URL.Path == Path {
			s.Handler().ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})}
	go srv.Serve(l)
	t.Logf("listening on %s%s for %d seconds", addr, Path, seconds)
	time.Sleep(time.Duration(seconds) * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	if got := s.Profiles(); len(got) == 0 {
		t.Log("RESULT: no browser connected")
	} else {
		t.Logf("RESULT: connected for %v", got)
	}
}

// fmtSscan keeps the import list short.
func fmtSscan(s string, n *int) (int, error) {
	v := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errNotNumber
		}
		v = v*10 + int(c-'0')
	}
	*n = v
	return 1, nil
}

var errNotNumber = errNot("not a number")

type errNot string

func (e errNot) Error() string { return string(e) }
