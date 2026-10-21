package extensions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDataPolicy(t *testing.T) {
	profile := t.TempDir()
	put := func(id string, size int) {
		d := filepath.Join(profile, "Local Extension Settings", id)
		os.MkdirAll(d, 0o700)
		os.WriteFile(filepath.Join(d, "000003.log"), make([]byte, size), 0o600)
	}
	const small, big, onePassword = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "aeblfdkhhhdcdjpifhhbdiojplfjncoa"
	put(small, 1000)
	put(big, MaxData+1)
	put(onePassword, 10)
	if !SyncsData(profile, small) {
		t.Error("small data must sync")
	}
	if SyncsData(profile, big) {
		t.Error("data over MaxData must not sync")
	}
	if SyncsData(profile, onePassword) || !NeverSyncsData(onePassword) {
		t.Error("a password manager's data must never sync")
	}
	if NeverSyncsData(small) {
		t.Error("only listed extensions are never synced")
	}
	pkg, h, err := PackDataHash(profile, small)
	if err != nil || len(pkg) == 0 || h == "" {
		t.Fatalf("pack: %d bytes, %q, %v", len(pkg), h, err)
	}
	if h2, _ := DataHash(profile, small); h2 != h {
		t.Fatal("PackDataHash and DataHash must agree")
	}
}
