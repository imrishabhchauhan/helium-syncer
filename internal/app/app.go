// Package app ties the pieces together for the command line and the tray
// app: where Helium Syncer keeps its own files, how this device is
// connected, and one call to sync.
package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/imrishabhchauhan/helium-syncer/internal/cloud"
	"github.com/imrishabhchauhan/helium-syncer/internal/deviceid"
	"github.com/imrishabhchauhan/helium-syncer/internal/engine"
	"github.com/imrishabhchauhan/helium-syncer/internal/secrets"
	"github.com/imrishabhchauhan/helium-syncer/internal/vault"
)

// Version of Helium Syncer.
const Version = "0.1.0-dev"

// Config is this device's connection (no secrets: the sync code lives in
// the OS key store).
type Config struct {
	Server      string    `json:"server"`
	DeviceID    string    `json:"device_id"`
	DeviceName  string    `json:"device_name"`
	AccountID   string    `json:"account_id"`
	UserData    string    `json:"user_data"`
	RestoreTabs bool      `json:"restore_tabs"`
	LastSync    time.Time `json:"last_sync"`
	LastError   string    `json:"last_error,omitempty"`
}

// ErrNotConnected: run start or join first.
var ErrNotConnected = errors.New("this device is not connected to a sync yet; start one or join with your sync code")

// Dir is Helium Syncer's own folder (never inside Helium's).
func Dir() string {
	if d := os.Getenv("HELIUM_SYNCER_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		if a := os.Getenv("APPDATA"); a != "" {
			return filepath.Join(a, "Helium Syncer")
		}
		return filepath.Join(home, "AppData", "Roaming", "Helium Syncer")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Helium Syncer")
	default:
		return filepath.Join(home, ".config", "helium-syncer")
	}
}

func configPath() string { return filepath.Join(Dir(), "config.json") }

// Load reads the config (empty if this device was never connected).
func Load() (*Config, error) {
	c := &Config{Server: cloud.DefaultServer}
	data, err := os.ReadFile(configPath())
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("config is damaged: %w", err)
	}
	return c, nil
}

// Save writes the config.
func (c *Config) Save() error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	tmp := configPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, configPath())
}

// StateDir is where the engine keeps state for this user data folder.
func (c *Config) StateDir() string {
	sum := sha256.Sum256([]byte(filepath.Clean(c.UserData)))
	return filepath.Join(Dir(), "state", hex.EncodeToString(sum[:6]))
}

const codeSecret = "sync-code"
const tokenSecret = "device-token"

// Code returns the stored sync code.
func Code() (string, error) {
	b, err := secrets.Load(Dir(), codeSecret)
	if errors.Is(err, secrets.ErrNotFound) {
		return "", ErrNotConnected
	}
	return string(b), err
}

// Connected reports whether this device has joined a sync.
func (c *Config) Connected() bool { return c.DeviceID != "" && c.AccountID != "" }

// Client builds a server client from the stored code and token.
func (c *Config) Client() (*cloud.Client, error) {
	if !c.Connected() {
		return nil, ErrNotConnected
	}
	code, err := Code()
	if err != nil {
		return nil, err
	}
	s, err := vault.Derive(code)
	if err != nil {
		return nil, err
	}
	tok, err := secrets.Load(Dir(), tokenSecret)
	if err != nil {
		return nil, ErrNotConnected
	}
	return &cloud.Client{Base: c.Server, Token: string(tok), AccountID: s.AccountID, Key: s.Key}, nil
}

// Connect starts a new sync (create) or joins one with code, and stores
// the code and device token in the OS key store.
func (c *Config) Connect(ctx context.Context, code string, create bool) (cloud.JoinResult, error) {
	norm, err := vault.NormalizeCode(code)
	if err != nil {
		return cloud.JoinResult{}, err
	}
	cl := &cloud.Client{Base: c.Server}
	r, err := cl.Join(ctx, norm, c.DeviceName, platform(), create)
	if err != nil {
		return r, err
	}
	if err := secrets.Save(Dir(), codeSecret, []byte(norm)); err != nil {
		return r, fmt.Errorf("could not store the sync code safely: %w", err)
	}
	if err := secrets.Save(Dir(), tokenSecret, []byte(r.Token)); err != nil {
		return r, fmt.Errorf("could not store the device token safely: %w", err)
	}
	c.DeviceID, c.AccountID = r.DeviceID, cl.AccountID
	return r, c.Save()
}

// Disconnect forgets the sync on this device (Helium's data is untouched).
func (c *Config) Disconnect() error {
	secrets.Delete(Dir(), codeSecret)
	secrets.Delete(Dir(), tokenSecret)
	c.DeviceID, c.AccountID = "", ""
	return c.Save()
}

func platform() string {
	switch runtime.GOOS {
	case "darwin":
		return "macos"
	default:
		return runtime.GOOS
	}
}

// EngineConfig is the engine setup for this device.
func (c *Config) EngineConfig() (engine.Config, error) {
	cl, err := c.Client()
	if err != nil {
		return engine.Config{}, err
	}
	mid, err := deviceid.Get()
	if err != nil {
		return engine.Config{}, fmt.Errorf("cannot read this machine's id: %w", err)
	}
	if c.UserData == "" {
		return engine.Config{}, errors.New("no Helium folder chosen yet")
	}
	return engine.Config{UserData: c.UserData, StateDir: c.StateDir(), DeviceID: c.DeviceID, DeviceName: c.DeviceName,
		MachineID: mid, Transport: cl, RestoreTabs: c.RestoreTabs}, nil
}

// DefaultDeviceName is a friendly name like "Rishabh's MacBook Air (macOS)".
func DefaultDeviceName() string {
	h, _ := os.Hostname()
	if h == "" {
		h = "This computer"
	}
	return fmt.Sprintf("%s (%s)", h, map[string]string{"windows": "Windows", "darwin": "macOS", "linux": "Linux"}[runtime.GOOS])
}
