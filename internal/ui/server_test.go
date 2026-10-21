package ui

import (
	"net/http"
	"strings"
	"testing"

	"github.com/imrishabhchauhan/helium-syncer/internal/app"
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

func TestSettingsAreChecked(t *testing.T) {
	t.Setenv("HELIUM_SYNCER_HOME", t.TempDir())
	s := &Server{}
	if _, err := s.Start(); err != nil {
		t.Fatal(err)
	}
	post := func(body string) int {
		req, _ := http.NewRequest("POST", "http://"+s.addr+"/api/settings", strings.NewReader(body))
		req.Header.Set("X-Key", s.key)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	for body, want := range map[string]int{
		`{"battery":"plugged"}`: 200,
		`{"battery":"turbo"}`:   400,
		`{"off":["history"]}`:   200,
		`{"off":["passwords"]}`: 400,
		`{"off":["bookmarks","history","tabs","settings","extensions"]}`: 400,
		`{"personName":"Priya"}`:                           200,
		`{"personName":"` + strings.Repeat("a", 41) + `"}`: 400,
		`{"heliumDir":"C:/no/such/folder"}`:                400,
	} {
		if got := post(body); got != want {
			t.Errorf("%s: got %d, want %d", body, got, want)
		}
	}
	cfg, _ := app.Load()
	if cfg.Battery != "plugged" || len(cfg.Off) != 1 || cfg.Off[0] != "history" || cfg.PersonName != "Priya" {
		t.Fatalf("saved settings: %+v", cfg)
	}
	if !strings.HasPrefix(cfg.DeviceName, "Priya") && cfg.DeviceName != "" {
		t.Errorf("the computer name should follow the person's name before joining: %q", cfg.DeviceName)
	}
}
