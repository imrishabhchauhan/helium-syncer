package helium

import (
	"os"
	"path/filepath"
	"testing"
)

// DataVersion reads the version Helium last wrote, without the line end.
func TestDataVersion(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Last Version"), []byte("155.0.8059.84\r\n"), 0o600)
	if v, err := DataVersion(dir); err != nil || v != "155.0.8059.84" {
		t.Fatalf("got %q, %v", v, err)
	}
	if _, err := DataVersion(t.TempDir()); err == nil {
		t.Fatal("a folder without Last Version must be an error")
	}
}
