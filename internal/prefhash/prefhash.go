// Package prefhash reproduces Chromium's tracked-preference integrity hashes
// (services/preferences/tracked/pref_hash_calculator.cc, Chromium 155).
//
//	legacy MAC     = UPPERHEX(HMAC-SHA256(key=seed, device_id || path || value))
//	encrypted hash = os_crypt.Encrypt(SHA256(seed || path || value))
//
// value is the preference serialised like base::WriteJson, after removing
// empty dictionaries and lists nested inside dictionaries.
package prefhash

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/imrishabhchauhan/helium-syncer/internal/chromejson"
)

// Calculator holds the per-build seed and the per-machine device id.
// Helium's seed is empty (verified in phase 0).
type Calculator struct {
	Seed     []byte
	DeviceID string
}

// MAC returns the legacy HMAC for path given its serialised value.
func (c Calculator) MAC(path, value string) string {
	m := hmac.New(sha256.New, c.Seed)
	m.Write([]byte(c.DeviceID))
	m.Write([]byte(path))
	m.Write([]byte(value))
	return strings.ToUpper(hex.EncodeToString(m.Sum(nil)))
}

// PlainHash is the value that gets encrypted into an *_encrypted_hash entry.
func (c Calculator) PlainHash(path, value string) []byte {
	h := sha256.New()
	h.Write(c.Seed)
	h.Write([]byte(path))
	h.Write([]byte(value))
	return h.Sum(nil)
}

// ValueString serialises v like Chromium's ValueAsString: nil -> "",
// dictionaries cleaned of empty nested containers, then base::WriteJson.
func ValueString(v any) string {
	if v == nil {
		return ""
	}
	if m, ok := v.(map[string]any); ok {
		v = chromejson.Clean(m)
	}
	return chromejson.Compact(v)
}
