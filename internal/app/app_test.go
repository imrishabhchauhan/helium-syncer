package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/imrishabhchauhan/helium-syncer/internal/secrets"
)

// A key store that will not open is not the same as "you have not joined a
// sync yet". They used to look the same, and the message sent people to
// join again, which replaces the sync they already have.
func TestALockedKeyStoreIsNotReportedAsNotConnected(t *testing.T) {
	if got := keyStoreError(secrets.ErrNotFound); !errors.Is(got, ErrNotConnected) {
		t.Errorf("nothing saved should read as not connected, got %v", got)
	}
	locked := errors.New("the keychain could not be opened (-25308)")
	got := keyStoreError(locked)
	if errors.Is(got, ErrNotConnected) {
		t.Error("a locked key store was reported as 'not connected to a sync yet'")
	}
	if !errors.Is(got, ErrKeyStore) {
		t.Errorf("a locked key store should read as a key store problem, got %v", got)
	}
	if !strings.Contains(got.Error(), "-25308") {
		t.Errorf("the message should keep what the system said, got %q", got)
	}
}
