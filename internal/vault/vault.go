// Package vault holds Helium Syncer's end-to-end encryption.
//
//   - Account key: 32 random bytes, created on the first device. Every op
//     batch and blob is sealed with it (XChaCha20-Poly1305). The server
//     never sees it.
//   - Device key: an X25519 key pair per device. A new device is approved
//     by an existing device sealing the account key to its public key.
//   - Recovery key: 128 random bits shown once as 8 groups of 4 characters.
//     It wraps the account key for the case that no old device is left.
package vault

import (
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
)

// KeySize is the account key length.
const KeySize = chacha20poly1305.KeySize

// ErrDecrypt means data was tampered with or the key is wrong.
var ErrDecrypt = errors.New("vault: cannot decrypt (wrong key or damaged data)")

// NewAccountKey creates a fresh account key.
func NewAccountKey() ([]byte, error) {
	k := make([]byte, KeySize)
	_, err := rand.Read(k)
	return k, err
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

// DeviceKey is a device's X25519 key pair.
type DeviceKey struct{ priv *ecdh.PrivateKey }

// NewDeviceKey creates a key pair.
func NewDeviceKey() (*DeviceKey, error) {
	p, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &DeviceKey{p}, nil
}

// LoadDeviceKey restores a key pair from Private().
func LoadDeviceKey(private []byte) (*DeviceKey, error) {
	p, err := ecdh.X25519().NewPrivateKey(private)
	if err != nil {
		return nil, err
	}
	return &DeviceKey{p}, nil
}

// Private returns the private key bytes (store them in the OS key store).
func (d *DeviceKey) Private() []byte { return d.priv.Bytes() }

// Public returns the public key, base64.
func (d *DeviceKey) Public() string {
	return base64.StdEncoding.EncodeToString(d.priv.PublicKey().Bytes())
}

const sealInfo = "helium-syncer sealed key v1"

// SealTo encrypts data so only the holder of publicKey (base64) can open it.
// Format: ephemeral public key (32) || nonce (24) || ciphertext.
func SealTo(publicKey string, data []byte) (string, error) {
	pubBytes, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil {
		return "", err
	}
	pub, err := ecdh.X25519().NewPublicKey(pubBytes)
	if err != nil {
		return "", err
	}
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	shared, err := eph.ECDH(pub)
	if err != nil {
		return "", err
	}
	key, err := hkdf.Key(sha256.New, shared, append(eph.PublicKey().Bytes(), pubBytes...), sealInfo, KeySize)
	if err != nil {
		return "", err
	}
	nonce, ct, err := Seal(key, data, []byte(sealInfo))
	if err != nil {
		return "", err
	}
	out := append(append(eph.PublicKey().Bytes(), nonce...), ct...)
	return base64.StdEncoding.EncodeToString(out), nil
}

// OpenSealed opens what SealTo produced for this device.
func (d *DeviceKey) OpenSealed(sealed string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil || len(raw) < 32+chacha20poly1305.NonceSizeX+chacha20poly1305.Overhead {
		return nil, ErrDecrypt
	}
	eph, err := ecdh.X25519().NewPublicKey(raw[:32])
	if err != nil {
		return nil, ErrDecrypt
	}
	shared, err := d.priv.ECDH(eph)
	if err != nil {
		return nil, ErrDecrypt
	}
	key, err := hkdf.Key(sha256.New, shared, append(raw[:32:32], d.priv.PublicKey().Bytes()...), sealInfo, KeySize)
	if err != nil {
		return nil, err
	}
	return Open(key, raw[32:32+chacha20poly1305.NonceSizeX], raw[32+chacha20poly1305.NonceSizeX:], []byte(sealInfo))
}

// --- recovery key ---

var recoveryEncoding = base32.NewEncoding("ABCDEFGHJKLMNPQRSTUVWXYZ23456789").WithPadding(base32.NoPadding)

// NewRecoveryKey returns a printable recovery key like "K7QM-2XPA-...".
func NewRecoveryKey() (string, error) {
	b := make([]byte, 20) // 160 bits -> 32 characters
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	s := recoveryEncoding.EncodeToString(b)
	var groups []string
	for i := 0; i < len(s); i += 4 {
		groups = append(groups, s[i:i+4])
	}
	return strings.Join(groups, "-"), nil
}

func parseRecoveryKey(k string) ([]byte, error) {
	// Dashes, spaces and case don't matter. The alphabet leaves out 0, 1,
	// O and I so they can't be confused when written down.
	s := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(k)))
	b, err := recoveryEncoding.DecodeString(s)
	if err != nil || len(b) != 20 {
		return nil, errors.New("vault: that does not look like a Helium Syncer recovery key")
	}
	return b, nil
}

// RecoverySecrets derives, from a recovery key, the key that wraps the
// account key and the secret that proves possession to the server (the
// server stores only its SHA-256).
func RecoverySecrets(recoveryKey, accountID string) (wrapKey []byte, authSecret, verifier string, err error) {
	raw, err := parseRecoveryKey(recoveryKey)
	if err != nil {
		return nil, "", "", err
	}
	wrapKey, err = hkdf.Key(sha256.New, raw, []byte(accountID), "helium-syncer recovery wrap v1", KeySize)
	if err != nil {
		return nil, "", "", err
	}
	auth, err := hkdf.Key(sha256.New, raw, []byte(accountID), "helium-syncer recovery auth v1", 32)
	if err != nil {
		return nil, "", "", err
	}
	authSecret = hex.EncodeToString(auth)
	sum := sha256.Sum256([]byte(authSecret))
	return wrapKey, authSecret, hex.EncodeToString(sum[:]), nil
}

// WrapAccountKey seals the account key with a recovery wrap key.
func WrapAccountKey(wrapKey, accountKey []byte, accountID string) (string, error) {
	nonce, ct, err := Seal(wrapKey, accountKey, []byte("recovery:"+accountID))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(append(nonce, ct...)), nil
}

// UnwrapAccountKey reverses WrapAccountKey.
func UnwrapAccountKey(wrapKey []byte, blob, accountID string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil || len(raw) < chacha20poly1305.NonceSizeX {
		return nil, ErrDecrypt
	}
	k, err := Open(wrapKey, raw[:chacha20poly1305.NonceSizeX], raw[chacha20poly1305.NonceSizeX:], []byte("recovery:"+accountID))
	if err != nil {
		return nil, fmt.Errorf("vault: the recovery key does not match this account: %w", err)
	}
	return k, nil
}
