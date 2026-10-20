package vault

import (
	"bytes"
	"strings"
	"testing"
)

func TestSealOpenAndTamper(t *testing.T) {
	s, _ := Derive(mustCode(t))
	nonce, ct, err := Seal(s.Key, []byte("bookmarks हिंदी 😀"), []byte("acct-1"))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := Open(s.Key, nonce, ct, []byte("acct-1"))
	if err != nil || string(pt) != "bookmarks हिंदी 😀" {
		t.Fatalf("round trip failed: %q %v", pt, err)
	}
	if _, err := Open(s.Key, nonce, ct, []byte("acct-2")); err == nil {
		t.Error("ciphertext must be bound to its account")
	}
	ct[0] ^= 1
	if _, err := Open(s.Key, nonce, ct, []byte("acct-1")); err == nil {
		t.Error("a flipped bit must be detected")
	}
	ct[0] ^= 1
	other, _ := Derive(mustCode(t))
	if _, err := Open(other.Key, nonce, ct, []byte("acct-1")); err == nil {
		t.Error("another sync code's key must fail")
	}
}

func mustCode(t *testing.T) string {
	c, err := NewSyncCode()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSyncCode(t *testing.T) {
	code := mustCode(t)
	if g := strings.Split(code, "-"); len(g) != 8 || len(g[0]) != 4 {
		t.Fatalf("sync code should be 8 groups of 4: %s", code)
	}
	a, err := Derive(code)
	if err != nil {
		t.Fatal(err)
	}
	// Typed in lower case with spaces, as people do.
	b, err := Derive(strings.ToLower(strings.ReplaceAll(code, "-", " ")))
	if err != nil {
		t.Fatal(err)
	}
	if a.AccountID != b.AccountID || a.Auth != b.Auth || !bytes.Equal(a.Key, b.Key) {
		t.Fatal("the same code must give the same secrets")
	}
	if a.AccountID == a.Auth || bytes.Equal([]byte(a.Auth), a.Key) {
		t.Fatal("derived values must be independent")
	}
	c, _ := Derive(mustCode(t))
	if c.AccountID == a.AccountID {
		t.Fatal("different codes must give different accounts")
	}
	for _, bad := range []string{"", "not a code", "AAAA-BBBB", strings.Replace(code, code[:1], "0", 1)} {
		if _, err := Derive(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}
