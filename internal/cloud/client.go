// Package cloud is the client for the Helium Syncer server. It implements
// engine.Transport: ops are gzip-compressed, encrypted with the account key
// and sent as opaque envelopes, so the server only ever stores ciphertext.
package cloud

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/imrishabhchauhan/helium-syncer/internal/crdt"
	"github.com/imrishabhchauhan/helium-syncer/internal/vault"
)

// DefaultServer is the hosted server; self-hosters point at their own.
const DefaultServer = "https://helium.aiwithrishabh.com"

// Client talks to one server as one device.
type Client struct {
	Base      string
	Token     string // device token from Verify
	AccountID string
	Key       []byte // account key (nil until the device is approved)
	HTTP      *http.Client
}

// APIError is an error answer from the server, with a message meant for people.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string { return e.Message }

// IsPending reports whether the device still waits for approval.
func IsPending(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Code == "pending"
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, body)
	if err != nil {
		return 0, err
	}
	if in != nil {
		req.Header.Set("content-type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("authorization", "Bearer "+c.Token)
	}
	res, err := c.http().Do(req)
	if err != nil {
		return 0, fmt.Errorf("cannot reach the sync server: %w", err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if res.StatusCode >= 400 {
		var e struct{ Error, Message string }
		json.Unmarshal(data, &e)
		if e.Message == "" {
			e.Message = fmt.Sprintf("the sync server answered %d", res.StatusCode)
		}
		return res.StatusCode, &APIError{Status: res.StatusCode, Code: e.Error, Message: e.Message}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return res.StatusCode, fmt.Errorf("unexpected answer from the sync server: %w", err)
		}
	}
	return res.StatusCode, nil
}

// --- sign-in and devices ---

// StartSignIn asks the server to email a 6-digit code.
func (c *Client) StartSignIn(ctx context.Context, email string) error {
	_, err := c.do(ctx, "POST", "/v1/auth/start", map[string]string{"email": email}, nil)
	return err
}

// VerifyResult is the answer to Verify.
type VerifyResult struct {
	AccountID  string `json:"account_id"`
	DeviceID   string `json:"device_id"`
	Token      string `json:"token"`
	Status     string `json:"status"` // approved | pending
	NewAccount bool   `json:"new_account"`
}

// Verify exchanges the emailed code for a device token.
func (c *Client) Verify(ctx context.Context, email, code, deviceName, platform, publicKey string) (VerifyResult, error) {
	var r VerifyResult
	_, err := c.do(ctx, "POST", "/v1/auth/verify", map[string]any{
		"email": email, "code": code,
		"device": map[string]string{"name": deviceName, "platform": platform, "public_key": publicKey},
	}, &r)
	if err == nil {
		c.Token, c.AccountID = r.Token, r.AccountID
	}
	return r, err
}

// Device is one device of the account.
type Device struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Platform      string `json:"platform"`
	PublicKey     string `json:"public_key"`
	Status        string `json:"status"`
	HeliumVersion string `json:"helium_version"`
	LastSeen      int64  `json:"last_seen"`
}

// Devices lists the account's devices and returns this device's id.
func (c *Client) Devices(ctx context.Context) ([]Device, string, error) {
	var r struct {
		Me      string   `json:"me"`
		Devices []Device `json:"devices"`
	}
	_, err := c.do(ctx, "GET", "/v1/devices", nil, &r)
	return r.Devices, r.Me, err
}

// Approve lets a waiting device in by sealing the account key to it.
func (c *Client) Approve(ctx context.Context, d Device) error {
	if c.Key == nil {
		return errors.New("this device has no account key yet")
	}
	sealed, err := vault.SealTo(d.PublicKey, c.Key)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, "POST", "/v1/devices/"+d.ID+"/approve", map[string]string{"sealed_key": sealed}, nil)
	return err
}

// Remove signs another device out.
func (c *Client) Remove(ctx context.Context, deviceID string) error {
	_, err := c.do(ctx, "POST", "/v1/devices/"+deviceID+"/remove", nil, nil)
	return err
}

// FetchKey gets this device's sealed account key once it is approved and
// opens it. It returns (nil, nil) while the device still waits.
func (c *Client) FetchKey(ctx context.Context, dk *vault.DeviceKey) ([]byte, error) {
	var r struct {
		Status    string `json:"status"`
		SealedKey string `json:"sealed_key"`
	}
	if _, err := c.do(ctx, "GET", "/v1/keys/mine", nil, &r); err != nil {
		return nil, err
	}
	if r.SealedKey == "" {
		return nil, nil
	}
	k, err := dk.OpenSealed(r.SealedKey)
	if err != nil {
		return nil, err
	}
	c.Key = k
	return k, nil
}

// StoreOwnKey keeps a sealed copy of the account key for this device.
func (c *Client) StoreOwnKey(ctx context.Context, dk *vault.DeviceKey) error {
	sealed, err := vault.SealTo(dk.Public(), c.Key)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, "PUT", "/v1/keys/mine", map[string]string{"sealed_key": sealed}, nil)
	return err
}

// SetRecovery stores the account key wrapped by a recovery key.
func (c *Client) SetRecovery(ctx context.Context, recoveryKey string) error {
	wrap, _, verifier, err := vault.RecoverySecrets(recoveryKey, c.AccountID)
	if err != nil {
		return err
	}
	blob, err := vault.WrapAccountKey(wrap, c.Key, c.AccountID)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, "PUT", "/v1/recovery", map[string]string{"blob": blob, "verifier": verifier}, nil)
	return err
}

// Recover approves this waiting device with the recovery key.
func (c *Client) Recover(ctx context.Context, recoveryKey string) ([]byte, error) {
	wrap, secret, _, err := vault.RecoverySecrets(recoveryKey, c.AccountID)
	if err != nil {
		return nil, err
	}
	var r struct {
		Blob string `json:"blob"`
	}
	if _, err := c.do(ctx, "POST", "/v1/recovery/claim", map[string]string{"secret": secret}, &r); err != nil {
		return nil, err
	}
	k, err := vault.UnwrapAccountKey(wrap, r.Blob, c.AccountID)
	if err != nil {
		return nil, err
	}
	c.Key = k
	return k, nil
}

// --- engine.Transport ---

const maxPlainBatch = 256 << 10 // compress+encrypt ops in batches of about this size

func (c *Client) ad(kind string) []byte { return []byte(kind + ":" + c.AccountID) }

func (c *Client) seal(ops []crdt.Op) (map[string]string, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zw).Encode(ops); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	nonce, ct, err := vault.Seal(c.Key, buf.Bytes(), c.ad("ops"))
	if err != nil {
		return nil, err
	}
	return map[string]string{"nonce": base64.StdEncoding.EncodeToString(nonce), "ciphertext": base64.StdEncoding.EncodeToString(ct)}, nil
}

// Push sends ops as encrypted envelopes.
func (c *Client) Push(ctx context.Context, ops []crdt.Op) error {
	if c.Key == nil {
		return errors.New("this device is not approved yet")
	}
	var items []map[string]string
	var batch []crdt.Op
	size := 0
	flushBatch := func() error {
		if len(batch) == 0 {
			return nil
		}
		it, err := c.seal(batch)
		if err != nil {
			return err
		}
		items = append(items, it)
		batch, size = nil, 0
		return nil
	}
	send := func() error {
		if len(items) == 0 {
			return nil
		}
		_, err := c.do(ctx, "POST", "/v1/envelopes", map[string]any{"items": items}, nil)
		items = nil
		return err
	}
	for _, op := range ops {
		b, _ := json.Marshal(op)
		if size+len(b) > maxPlainBatch && len(batch) > 0 {
			if err := flushBatch(); err != nil {
				return err
			}
			if len(items) == 50 {
				if err := send(); err != nil {
					return err
				}
			}
		}
		batch = append(batch, op)
		size += len(b)
	}
	if err := flushBatch(); err != nil {
		return err
	}
	return send()
}

// Pull returns every op after cursor, decrypted.
func (c *Client) Pull(ctx context.Context, cursor string) ([]crdt.Op, string, error) {
	if c.Key == nil {
		return nil, cursor, errors.New("this device is not approved yet")
	}
	after, _ := strconv.ParseInt(cursor, 10, 64)
	var all []crdt.Op
	for {
		var r struct {
			Items []struct {
				Seq        int64  `json:"seq"`
				Nonce      string `json:"nonce"`
				Ciphertext string `json:"ciphertext"`
			} `json:"items"`
			Next int64 `json:"next"`
			More bool  `json:"more"`
		}
		if _, err := c.do(ctx, "GET", "/v1/envelopes?"+url.Values{"after": {strconv.FormatInt(after, 10)}, "limit": {"200"}}.Encode(), nil, &r); err != nil {
			return nil, cursor, err
		}
		for _, it := range r.Items {
			nonce, err1 := base64.StdEncoding.DecodeString(it.Nonce)
			ct, err2 := base64.StdEncoding.DecodeString(it.Ciphertext)
			if err1 != nil || err2 != nil {
				return nil, cursor, fmt.Errorf("envelope %d is damaged", it.Seq)
			}
			pt, err := vault.Open(c.Key, nonce, ct, c.ad("ops"))
			if err != nil {
				return nil, cursor, fmt.Errorf("envelope %d: %w", it.Seq, err)
			}
			zr, err := gzip.NewReader(bytes.NewReader(pt))
			if err != nil {
				return nil, cursor, err
			}
			var ops []crdt.Op
			if err := json.NewDecoder(io.LimitReader(zr, 256<<20)).Decode(&ops); err != nil {
				return nil, cursor, fmt.Errorf("envelope %d: %w", it.Seq, err)
			}
			all = append(all, ops...)
		}
		after = r.Next
		if !r.More {
			break
		}
	}
	return all, strconv.FormatInt(after, 10), nil
}

// Versions reports the Helium version of every other approved device.
func (c *Client) Versions(ctx context.Context, exceptDevice string) (map[string]string, error) {
	devs, me, err := c.Devices(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, d := range devs {
		if d.ID == me || d.Status != "approved" || d.HeliumVersion == "" {
			continue
		}
		out[d.Name] = d.HeliumVersion
	}
	return out, nil
}

// Announce records this device's Helium version.
func (c *Client) Announce(ctx context.Context, _, _ string, version string) error {
	_, err := c.do(ctx, "PUT", "/v1/devices/me", map[string]string{"helium_version": version}, nil)
	return err
}

// --- blobs (extension packages and data) ---

// PutBlob encrypts and uploads data; it returns the blob id to reference.
func (c *Client) PutBlob(ctx context.Context, data []byte) (string, error) {
	nonce, ct, err := vault.Seal(c.Key, data, c.ad("blob"))
	if err != nil {
		return "", err
	}
	payload := append(nonce, ct...)
	sum := sha256.Sum256(payload)
	id := hex.EncodeToString(sum[:])
	req, err := http.NewRequestWithContext(ctx, "PUT", c.Base+"/v1/blobs/"+id, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("authorization", "Bearer "+c.Token)
	res, err := c.http().Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		var e struct{ Error, Message string }
		json.NewDecoder(res.Body).Decode(&e)
		return "", &APIError{Status: res.StatusCode, Code: e.Error, Message: e.Message}
	}
	return id, nil
}

// GetBlob downloads and decrypts a blob, checking its id.
func (c *Client) GetBlob(ctx context.Context, id string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.Base+"/v1/blobs/"+id, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("authorization", "Bearer "+c.Token)
	res, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return nil, &APIError{Status: res.StatusCode, Code: "blob", Message: "could not download a synced file"}
	}
	payload, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if sum := sha256.Sum256(payload); hex.EncodeToString(sum[:]) != id || len(payload) < 24 {
		return nil, errors.New("a synced file arrived damaged")
	}
	return vault.Open(c.Key, payload[:24], payload[24:], c.ad("blob"))
}
