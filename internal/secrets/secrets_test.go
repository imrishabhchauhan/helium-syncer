package secrets

import (
	"os"
	"testing"
)

func TestSaveLoadDelete(t *testing.T) {
	dir := t.TempDir()
	name := "test-" + t.Name()
	defer Delete(dir, name)
	if _, err := Load(dir, name); err != ErrNotFound {
		t.Fatalf("missing secret should be ErrNotFound, got %v", err)
	}
	want := []byte("K7QM-2XPA-9RTD-LMNP-QRST-UVWX-YZ23-4567 हिंदी")
	if err := Save(dir, name, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir, name)
	if err != nil || string(got) != string(want) {
		t.Fatalf("got %q, %v", got, err)
	}
	// On Windows the file on disk must not contain the secret in clear.
	if entries, _ := os.ReadDir(dir); len(entries) > 0 {
		raw, _ := os.ReadFile(dir + string(os.PathSeparator) + entries[0].Name())
		if string(raw) == string(want) {
			t.Error("secret stored in clear text")
		}
	}
	if err := Delete(dir, name); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir, name); err != ErrNotFound {
		t.Fatalf("deleted secret should be gone, got %v", err)
	}
}
