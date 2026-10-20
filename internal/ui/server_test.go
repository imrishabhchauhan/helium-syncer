package ui

import (
	"net/http"
	"strings"
	"testing"
)

// The local page must refuse anything that is not this page talking to us:
// wrong key, other websites, DNS rebinding, form posts.
func TestOnlyThePageCanTalkToTheServer(t *testing.T) {
	t.Setenv("HELIUM_SYNCER_HOME", t.TempDir())
	s := &Server{}
	if _, err := s.Start(); err != nil {
		t.Fatal(err)
	}
	base := "http://" + s.addr
	// An unknown route reaches the API (400) only after every check passed,
	// and it changes nothing.
	const probe = "/api/no-such-thing"
	cases := []struct {
		name   string
		method string
		path   string
		header map[string]string
		host   string
		want   int
	}{
		{"page loads", "GET", "/", nil, "", 200},
		{"page from another host name", "GET", "/", nil, "evil.example:80", 403},
		{"api with the key", "GET", probe, map[string]string{"X-Key": s.key}, "", 400},
		{"api without the key", "GET", probe, nil, "", 403},
		{"api with a wrong key", "GET", probe, map[string]string{"X-Key": strings.Repeat("0", len(s.key))}, "", 403},
		{"api from another website", "GET", probe, map[string]string{"X-Key": s.key, "Origin": "https://evil.example"}, "", 403},
		{"api through DNS rebinding", "GET", probe, map[string]string{"X-Key": s.key}, "rebind.example:" + strings.Split(s.addr, ":")[1], 403},
		{"form post", "POST", probe, map[string]string{"X-Key": s.key, "Content-Type": "application/x-www-form-urlencoded"}, "", 415},
		{"json post", "POST", probe, map[string]string{"X-Key": s.key, "Content-Type": "application/json", "Origin": base}, "", 400},
		{"other paths", "GET", "/secret", nil, "", 404},
	}
	for _, c := range cases {
		req, _ := http.NewRequest(c.method, base+c.path, strings.NewReader("{}"))
		for k, v := range c.header {
			req.Header.Set(k, v)
		}
		if c.host != "" {
			req.Host = c.host
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		res.Body.Close()
		if res.StatusCode != c.want {
			t.Errorf("%s: got %d, want %d", c.name, res.StatusCode, c.want)
		}
		if c.path == "/" && res.StatusCode == 200 {
			if res.Header.Get("X-Frame-Options") != "DENY" || !strings.Contains(res.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
				t.Errorf("%s: page can be framed", c.name)
			}
		}
	}
}
