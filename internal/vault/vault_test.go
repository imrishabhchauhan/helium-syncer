package vault

import (
	"bytes"
	"strings"
	"testing"
)

func TestSealOpenAndTamper(t *testing.T) {
	k, _ := NewAccountKey()
	nonce, ct, err := Seal(k, []byte("bookmarks हिंदी 😀"), []byte("acct-1"))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := Open(k, nonce, ct, []byte("acct-1"))
	if err != nil || string(pt) != "bookmarks हिंदी 😀" {
		t.Fatalf("round trip failed: %q %v", pt, err)
	}
	if _, err := Open(k, nonce, ct, []byte("acct-2")); err == nil {
		t.Error("ciphertext must be bound to its account")
	}
	ct[0] ^= 1
	if _, err := Open(k, nonce, ct, []byte("acct-1")); err == nil {
		t.Error("a flipped bit must be detected")
	}
	other, _ := NewAccountKey()
	ct[0] ^= 1
	if _, err := Open(other, nonce, ct, []byte("acct-1")); err == nil {
		t.Error("a wrong key must fail")
	}
}

func TestSealToDevice(t *testing.T) {
	newDev, _ := NewDeviceKey()
	k, _ := NewAccountKey()
	sealed, err := SealTo(newDev.Public(), k)
	if err != nil {
		t.Fatal(err)
	}
	got, err := newDev.OpenSealed(sealed)
	if err != nil || !bytes.Equal(got, k) {
		t.Fatalf("device could not open its sealed key: %v", err)
	}
	stranger, _ := NewDeviceKey()
	if _, err := stranger.OpenSealed(sealed); err == nil {
		t.Error("another device must not open it")
	}
	reloaded, _ := LoadDeviceKey(newDev.Private())
	if got, err := reloaded.OpenSealed(sealed); err != nil || !bytes.Equal(got, k) {
		t.Error("a reloaded device key must still open it")
	}
}

func TestRecoveryKey(t *testing.T) {
	rk, err := NewRecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.Split(rk, "-")) != 8 {
		t.Fatalf("recovery key should be 8 groups: %s", rk)
	}
	k, _ := NewAccountKey()
	wrap, auth, verifier, err := RecoverySecrets(rk, "acct-1")
	if err != nil {
		t.Fatal(err)
	}
	blob, err := WrapAccountKey(wrap, k, "acct-1")
	if err != nil {
		t.Fatal(err)
	}
	// Typed in lower case with spaces, as people do.
	typed := strings.ToLower(strings.ReplaceAll(rk, "-", " "))
	wrap2, auth2, verifier2, err := RecoverySecrets(typed, "acct-1")
	if err != nil {
		t.Fatal(err)
	}
	if auth2 != auth || verifier2 != verifier {
		t.Fatal("the same recovery key must give the same secrets")
	}
	got, err := UnwrapAccountKey(wrap2, blob, "acct-1")
	if err != nil || !bytes.Equal(got, k) {
		t.Fatalf("unwrap failed: %v", err)
	}
	wrongKey, _ := NewRecoveryKey()
	w3, _, _, _ := RecoverySecrets(wrongKey, "acct-1")
	if _, err := UnwrapAccountKey(w3, blob, "acct-1"); err == nil {
		t.Error("a wrong recovery key must fail")
	}
	if _, _, _, err := RecoverySecrets("not a key", "acct-1"); err == nil {
		t.Error("garbage must be rejected with a clear error")
	}
}
