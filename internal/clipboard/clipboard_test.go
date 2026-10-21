package clipboard

import (
	"os"
	"testing"
)

// Writes to the real clipboard, so it runs only when asked.
func TestWrite(t *testing.T) {
	if os.Getenv("HELIUM_SYNCER_CLIPBOARD_TEST") != "1" {
		t.Skip("set HELIUM_SYNCER_CLIPBOARD_TEST=1 to write to the clipboard")
	}
	if err := Write("Helium Syncer test ✓"); err != nil {
		t.Fatal(err)
	}
}
