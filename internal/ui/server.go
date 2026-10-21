// Package ui serves Helium Syncer's setup and status screens as a local
// web page (127.0.0.1 only). Every request must carry the random key from
// the URL the app opened, and come from that exact origin, so no website
// can talk to it.
package ui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/imrishabhchauhan/helium-syncer/internal/agent"
	"github.com/imrishabhchauhan/helium-syncer/internal/app"
	"github.com/imrishabhchauhan/helium-syncer/internal/cloud"
	"github.com/imrishabhchauhan/helium-syncer/internal/detect"
	"github.com/imrishabhchauhan/helium-syncer/internal/engine"
	"github.com/imrishabhchauhan/helium-syncer/internal/helium"
	"github.com/imrishabhchauhan/helium-syncer/internal/power"
	"github.com/imrishabhchauhan/helium-syncer/internal/profiles"
	"github.com/imrishabhchauhan/helium-syncer/internal/vault"
)

//go:embed page.html
var files embed.FS

// Server is the local UI.
type Server struct {
	OnChange  func() // e.g. refresh the tray title
	key       string
	addr      string
	mu        sync.Mutex
	ag        *agent.Agent // nil until connected
	freshCode string       // shown once after "start", until the user confirms
	pw        power.State
	pwAt      time.Time
	lastRep   *engine.Report // the last sync this window ran itself
}

// SetAgent hands the running background sync to the page.
func (s *Server) SetAgent(a *agent.Agent) { s.mu.Lock(); s.ag = a; s.mu.Unlock() }

// Agent is the running background sync, or nil.
func (s *Server) Agent() *agent.Agent { s.mu.Lock(); defer s.mu.Unlock(); return s.ag }

// Start listens on a free local port and returns the URL to open.
func (s *Server) Start() (string, error) {
	b := make([]byte, 16)
	rand.Read(b)
	s.key = hex.EncodeToString(b)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	s.addr = l.Addr().String()
	srv := &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(l)
	return s.URL(), nil
}

// URL is the page address with its key.
func (s *Server) URL() string { return "http://" + s.addr + "/#k=" + s.key }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only this exact host (blocks DNS rebinding).
	if r.Host != s.addr {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'none'; script-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'")
	if r.URL.Path == "/" {
		data, _ := files.ReadFile("page.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(data)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		http.NotFound(w, r)
		return
	}
	// API: same origin and the key in a header (not a cookie, so other
	// sites cannot ride along).
	if o := r.Header.Get("Origin"); o != "" && o != "http://"+s.addr {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Key")), []byte(s.key)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet && r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "json only", http.StatusUnsupportedMediaType)
		return
	}
	res, err := s.api(r)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": message(err)})
		return
	}
	json.NewEncoder(w).Encode(res)
	if r.Method != http.MethodGet && s.OnChange != nil {
		s.OnChange()
	}
}

func message(err error) string {
	var ae *cloud.APIError
	switch {
	case errors.As(err, &ae):
		return ae.Message
	case errors.Is(err, engine.ErrHeliumRunning):
		return "Helium is open. Quit Helium (Cmd+Q on Mac), then try again."
	case errors.Is(err, vault.ErrBadCode):
		return "That code doesn't look right. A sync code has 8 groups of 4 letters and numbers."
	}
	return err.Error()
}

type profileView struct {
	Dir    string `json:"dir"`
	Name   string `json:"name"`
	Synced bool   `json:"synced"`
	Empty  bool   `json:"empty"`
}

type stateView struct {
	Connected   bool           `json:"connected"`
	DeviceName  string         `json:"deviceName"`
	HeliumFound bool           `json:"heliumFound"`
	HeliumOpen  bool           `json:"heliumOpen"`
	Version     string         `json:"version"`
	Profiles    []profileView  `json:"profiles"`
	LastSync    int64          `json:"lastSync"`
	LastError   string         `json:"lastError"`
	Paused      string         `json:"paused"`
	Syncing     bool           `json:"syncing"`
	Autostart   bool           `json:"autostart"`
	RestoreTabs bool           `json:"restoreTabs"`
	SetupDone   bool           `json:"setupDone"`
	FreshCode   string         `json:"freshCode,omitempty"`
	AppVersion  string         `json:"appVersion"`
	LastReport  *engine.Report `json:"lastReport,omitempty"`
	PersonName  string         `json:"personName"`
	OS          string         `json:"os"` // windows, darwin, linux
	HeliumDir   string         `json:"heliumDir"`
	Kinds       []string       `json:"kinds"` // every kind, in order
	Off         []string       `json:"off"`   // kinds switched off here
	Battery     string         `json:"battery"`
	HasBattery  bool           `json:"hasBattery"`
	OnBattery   bool           `json:"onBattery"`
	PowerNote   string         `json:"powerNote,omitempty"`
}

// powerState is read at most once a minute (on macOS it starts pmset).
func (s *Server) powerState() power.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pwAt.IsZero() || time.Since(s.pwAt) > time.Minute {
		s.pw, s.pwAt = power.Read(), time.Now()
	}
	return s.pw
}

func (s *Server) state() (stateView, error) {
	cfg, err := app.LoadCached()
	if err != nil {
		return stateView{}, err
	}
	if cfg.UserData == "" {
		if found := detect.Find(); len(found) > 0 {
			cfg.UserData = found[0].UserData
		}
	}
	if cfg.DeviceName == "" || (!cfg.Connected() && cfg.DeviceName == app.DefaultDeviceName("")) {
		cfg.DeviceName = app.DefaultDeviceName(cfg.PersonName)
	}
	pw := s.powerState()
	battery := cfg.Battery
	if battery == "" {
		battery = agent.BatteryBalanced
	}
	v := stateView{Connected: cfg.Connected(), DeviceName: cfg.DeviceName, RestoreTabs: cfg.RestoreTabs, SetupDone: cfg.SetupDone,
		Autostart: agent.Autostart(), AppVersion: app.Version, LastError: cfg.LastError,
		PersonName: cfg.PersonName, OS: runtime.GOOS, HeliumDir: cfg.UserData, Kinds: engine.Kinds, Off: append([]string{}, cfg.Off...),
		Battery: battery, HasBattery: pw.HasBattery, OnBattery: pw.OnBattery}
	if !cfg.LastSync.IsZero() {
		v.LastSync = cfg.LastSync.UnixMilli()
	}
	if in, ok := detect.Inspect(cfg.UserData); ok {
		v.HeliumFound, v.Version = true, in.Version
		v.HeliumOpen, _ = helium.IsRunning(cfg.UserData)
		links := map[string]string{}
		if cfg.Connected() {
			links, _ = engine.Links(cfg.StateDir(), cfg.DeviceID)
		}
		for _, p := range in.Profiles {
			_, synced := links[p.Dir]
			v.Profiles = append(v.Profiles, profileView{p.Dir, p.Name, synced, profileEmpty(cfg.UserData, p.Dir)})
		}
	}
	if ag := s.Agent(); ag != nil {
		st := ag.Status.Snapshot()
		v.Syncing, v.Paused, v.PowerNote = st.Syncing, st.Paused, st.PowerNote
		if st.LastError != "" {
			v.LastError = st.LastError
		}
		if !st.LastSync.IsZero() {
			v.LastSync = st.LastSync.UnixMilli()
			rep := st.LastReport
			v.LastReport = &rep
		}
	}
	s.mu.Lock()
	v.FreshCode = s.freshCode
	if v.LastReport == nil && s.lastRep != nil {
		rep := *s.lastRep
		v.LastReport = &rep
	}
	s.mu.Unlock()
	return v, nil
}

// profileEmpty: no bookmarks file and little history means a fresh profile.
func profileEmpty(ud, dir string) bool {
	if _, err := os.Stat(ud + string(os.PathSeparator) + dir + string(os.PathSeparator) + "Bookmarks"); err == nil {
		return false
	}
	info, err := os.Stat(ud + string(os.PathSeparator) + dir + string(os.PathSeparator) + "History")
	return err != nil || info.Size() < 300*1024
}

func decode(r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<16)).Decode(v)
}

func (s *Server) api(r *http.Request) (any, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Minute)
	defer cancel()
	cfg, err := app.Load()
	if err != nil {
		return nil, err
	}
	if cfg.UserData == "" {
		if found := detect.Find(); len(found) > 0 {
			cfg.UserData = found[0].UserData
		}
	}
	if cfg.DeviceName == "" {
		cfg.DeviceName = app.DefaultDeviceName(cfg.PersonName)
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /api/state":
		return s.state()

	case "POST /api/start", "POST /api/join":
		var in struct {
			Code string `json:"code"`
			Name string `json:"name"`
		}
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		if strings.TrimSpace(in.Name) != "" {
			cfg.DeviceName = strings.TrimSpace(in.Name)
		}
		if cfg.Connected() {
			return nil, errors.New("this computer is already in a sync")
		}
		create := r.URL.Path == "/api/start"
		code := in.Code
		if create {
			if code, err = vault.NewSyncCode(); err != nil {
				return nil, err
			}
		}
		if _, err := cfg.Connect(ctx, code, create); err != nil {
			return nil, err
		}
		if create {
			s.mu.Lock()
			s.freshCode = code
			s.mu.Unlock()
		}
		return s.state()

	case "POST /api/code-saved":
		s.mu.Lock()
		s.freshCode = ""
		s.mu.Unlock()
		return s.state()

	case "GET /api/code":
		code, err := app.Code()
		if err != nil {
			return nil, err
		}
		return map[string]string{"code": code}, nil

	case "POST /api/profiles":
		var in struct {
			Sync []string `json:"sync"` // folders to sync; others are not synced
		}
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		if !cfg.Connected() {
			return nil, app.ErrNotConnected
		}
		want := map[string]bool{}
		for _, d := range in.Sync {
			want[d] = true
		}
		list, err := profiles.List(cfg.UserData)
		if err != nil {
			return nil, err
		}
		links, _ := engine.Links(cfg.StateDir(), cfg.DeviceID)
		for _, p := range list {
			_, linked := links[p.Dir]
			switch {
			case want[p.Dir] && !linked:
				if _, err := engine.LinkProfile(cfg.StateDir(), cfg.DeviceID, p.Dir); err != nil {
					return nil, err
				}
			case !want[p.Dir] && linked:
				if err := engine.UnlinkProfile(cfg.StateDir(), cfg.DeviceID, p.Dir); err != nil {
					return nil, err
				}
			}
		}
		cfg.Save()
		return s.state()

	case "POST /api/preview":
		var in struct {
			RestoreTabs *bool `json:"restoreTabs"`
		}
		decode(r, &in)
		if in.RestoreTabs != nil {
			cfg.RestoreTabs = *in.RestoreTabs // preview exactly what Start syncing will do
		}
		ec, err := cfg.EngineConfig()
		if err != nil {
			return nil, err
		}
		return engine.DryRun(ctx, ec)

	case "POST /api/sync":
		if ag := s.Agent(); ag != nil {
			select {
			case ag.Trigger <- struct{}{}:
			default:
			}
			return s.state()
		}
		ec, err := cfg.EngineConfig()
		if err != nil {
			return nil, err
		}
		rep, err := engine.Run(ctx, ec)
		if err != nil {
			return nil, err
		}
		cfg.LastSync, cfg.LastError = time.Now(), ""
		cfg.Save()
		s.mu.Lock()
		s.lastRep = &rep
		s.mu.Unlock()
		return rep, nil

	case "POST /api/settings":
		var in struct {
			Autostart   *bool     `json:"autostart"`
			RestoreTabs *bool     `json:"restoreTabs"`
			SetupDone   *bool     `json:"setupDone"`
			Name        *string   `json:"name"`
			PersonName  *string   `json:"personName"`
			Battery     *string   `json:"battery"`
			Off         *[]string `json:"off"`
			HeliumDir   *string   `json:"heliumDir"`
		}
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		if in.PersonName != nil {
			n := strings.TrimSpace(*in.PersonName)
			if len([]rune(n)) > 40 {
				return nil, errors.New("Type a name of 40 letters or less.")
			}
			// The computer name follows the person's name until they change it.
			if cfg.DeviceName == "" || cfg.DeviceName == app.DefaultDeviceName(cfg.PersonName) || !cfg.Connected() {
				cfg.DeviceName = app.DefaultDeviceName(n)
			}
			cfg.PersonName = n
		}
		if in.Battery != nil {
			switch *in.Battery {
			case agent.BatteryBalanced, agent.BatteryPlugged, agent.BatteryAlways:
				cfg.Battery = *in.Battery
			default:
				return nil, errors.New("unknown battery mode")
			}
		}
		if in.Off != nil {
			known := map[string]bool{}
			for _, k := range engine.Kinds {
				known[k] = true
			}
			var off []string
			for _, k := range *in.Off {
				if !known[k] {
					return nil, fmt.Errorf("unknown kind %q", k)
				}
				off = append(off, k)
			}
			if len(off) == len(engine.Kinds) {
				return nil, errors.New("Keep at least one item on. To stop all sync, use Leave this sync.")
			}
			cfg.Off = off
		}
		if in.HeliumDir != nil {
			if cfg.Connected() && cfg.SetupDone {
				return nil, errors.New("Leave this sync first, then choose a different Helium folder.")
			}
			found, ok := detect.Inspect(strings.TrimSpace(*in.HeliumDir))
			if !ok {
				return nil, errors.New("No Helium data is in that folder. Find the folder that contains the file \"Local State\".")
			}
			cfg.UserData = found.UserData
		}
		if in.Autostart != nil {
			if err := agent.SetAutostart(*in.Autostart); err != nil {
				return nil, err
			}
		}
		if in.RestoreTabs != nil {
			cfg.RestoreTabs = *in.RestoreTabs
		}
		if in.SetupDone != nil {
			cfg.SetupDone = *in.SetupDone
		}
		if in.Name != nil && strings.TrimSpace(*in.Name) != "" {
			cfg.DeviceName = strings.TrimSpace(*in.Name)
		}
		if err := cfg.Save(); err != nil {
			return nil, err
		}
		return s.state()

	case "GET /api/devices":
		cl, err := cfg.Client()
		if err != nil {
			return nil, err
		}
		devs, me, err := cl.Devices(ctx)
		if err != nil {
			return nil, err
		}
		sort.SliceStable(devs, func(i, j int) bool { return devs[i].ID == me })
		return map[string]any{"me": me, "devices": devs}, nil

	case "POST /api/remove-device":
		var in struct {
			ID string `json:"id"`
		}
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		cl, err := cfg.Client()
		if err != nil {
			return nil, err
		}
		return map[string]bool{"removed": true}, cl.Remove(ctx, in.ID)

	case "POST /api/disconnect":
		if err := cfg.Disconnect(); err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.freshCode = ""
		s.mu.Unlock()
		return s.state()

	case "POST /api/open-helium":
		return map[string]bool{"ok": true}, agent.LaunchHelium()
	}
	return nil, fmt.Errorf("unknown request %s %s", r.Method, r.URL.Path)
}
