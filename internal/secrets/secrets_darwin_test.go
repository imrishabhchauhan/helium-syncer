package secrets

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// Reading a secret must tell "nothing is saved" apart from "it is saved but
// the keychain would not open". They used to look the same, so a program
// that could not open the keychain told the person they had never joined a
// sync - which sends them to join again and replace the sync they have.
func TestLoadSeparatesNothingSavedFromAKeychainThatWillNotOpen(t *testing.T) {
	dir := t.TempDir()
	name := "helium-syncer-test-" + strings.ReplaceAll(t.Name(), "/", "-")

	// Nothing saved under this name.
	if _, err := Load(dir, name); !errors.Is(err, ErrNotFound) {
		t.Errorf("nothing saved should read as not found, got %v", err)
	}

	// Saved, then read back. On a machine with no desktop session (a build
	// machine, an SSH session) the keychain refuses, and that must NOT look
	// like "nothing saved".
	if err := Save(dir, name, []byte("secret-value")); err != nil {
		t.Skipf("cannot write to this keychain here: %v", err)
	}
	t.Cleanup(func() { Delete(dir, name) })
	got, err := Load(dir, name)
	switch {
	case err == nil:
		if string(got) != "secret-value" {
			t.Error("the value read back is not the one saved")
		}
	case errors.Is(err, ErrNotFound):
		t.Error("a saved secret that could not be read was reported as 'nothing saved'")
	default:
		if !strings.Contains(err.Error(), "keychain") {
			t.Errorf("the message should say the keychain is the problem, got %v", err)
		}
		t.Logf("the keychain refused here, with a message that says so: %v", err)
	}
	_ = os.Getenv
}
