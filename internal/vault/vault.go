// Package vault holds Helium Syncer's end-to-end encryption.
//
// Everything starts from the sync code, a random 160-bit secret shown as
// 8 groups of 4 characters (like "K7QM-2XPA-9RTD-…"). The first device
// creates it; every other device joins by entering it. From the code each
// device derives, locally:
//
//   - the account id (where the encrypted data lives on the server),
//   - the auth secret (proves knowledge of the code; the server keeps only
//     its SHA-256),
//   - the encryption key for every op batch and blob (XChaCha20-Poly1305).
//
// The server never sees the code or the key, so it cannot read anything.
package vault

import (
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
)

// KeySize is the encryption key length.
const KeySize = chacha20poly1305.KeySize

// ErrDecrypt means data was tampered with or the key is wrong.
var ErrDecrypt = errors.New("vault: cannot decrypt (wrong sync code or damaged data)")

// ErrBadCode means the text is not a sync code.
var ErrBadCode = errors.New("that does not look like a Helium Syncer sync code (8 groups of 4 letters and digits)")

// The alphabet leaves out 0, 1, O and I so a written-down code can't be misread.
var codeEncoding = base32.NewEncoding("ABCDEFGHJKLMNPQRSTUVWXYZ23456789").WithPadding(base32.NoPadding)

// NewSyncCode creates a new sync code.
func NewSyncCode() (string, error) {
	b := make([]byte, 20) // 160 bits -> 32 characters
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return format(codeEncoding.EncodeToString(b)), nil
}

func format(s string) string {
	var groups []string
	for i := 0; i < len(s); i += 4 {
		groups = append(groups, s[i:min(i+4, len(s))])
	}
	return strings.Join(groups, "-")
}

// NormalizeCode accepts a code typed with spaces, without dashes or in
// lower case, and returns it in the standard form.
func NormalizeCode(code string) (string, error) {
	s := strings.ToUpper(strings.NewReplacer("-", "", " ", "", "\t", "").Replace(strings.TrimSpace(code)))
	b, err := codeEncoding.DecodeString(s)
	if err != nil || len(b) != 20 {
		return "", ErrBadCode
	}
	return format(s), nil
}

// Secrets are everything a device derives from the sync code.
type Secrets struct {
	AccountID string // hex, sent to the server
	Auth      string // hex, sent to the server when joining (it stores only the hash)
	Key       []byte // never leaves the device
}

// Derive turns a sync code into the account id, auth secret and key.
func Derive(code string) (Secrets, error) {
	norm, err := NormalizeCode(code)
	if err != nil {
		return Secrets{}, err
	}
	raw, _ := codeEncoding.DecodeString(strings.ReplaceAll(norm, "-", ""))
	salt := []byte("helium-syncer sync code v1")
	id, err := hkdf.Key(sha256.New, raw, salt, "account id", 32)
	if err != nil {
		return Secrets{}, err
	}
	auth, err := hkdf.Key(sha256.New, raw, salt, "auth secret", 32)
	if err != nil {
		return Secrets{}, err
	}
	key, err := hkdf.Key(sha256.New, raw, salt, "encryption key", KeySize)
	if err != nil {
		return Secrets{}, err
	}
	return Secrets{AccountID: hex.EncodeToString(id), Auth: hex.EncodeToString(auth), Key: key}, nil
}

// Seal encrypts plaintext with key; ad is authenticated but not encrypted
// (it binds the ciphertext to its context, e.g. the account id).
func Seal(key, plaintext, ad []byte) (nonce, ciphertext []byte, err error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return nonce, aead.Seal(nil, nonce, plaintext, ad), nil
}

// Open decrypts what Seal produced.
func Open(key, nonce, ciphertext, ad []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	if len(nonce) != aead.NonceSize() {
		return nil, ErrDecrypt
	}
	out, err := aead.Open(nil, nonce, ciphertext, ad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return out, nil
}
