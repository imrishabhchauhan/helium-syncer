package extensions

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/imrishabhchauhan/helium-syncer/internal/chromejson"
	"github.com/imrishabhchauhan/helium-syncer/internal/prefhash"
)

const extID = "eimadpbcbfnmbkopoojfekhnkhdbieeh"

// fakeProfile makes a profile with one Web Store extension and data.
func fakeProfile(t *testing.T, sep string) string {
	p := t.TempDir()
	ver := filepath.Join(p, "Extensions", extID, "4.9.133_0")
	os.MkdirAll(filepath.Join(ver, "js"), 0o700)
	os.WriteFile(filepath.Join(ver, "manifest.json"), []byte(`{"name":"Dark Reader","version":"4.9.133"}`), 0o600)
	os.WriteFile(filepath.Join(ver, "js", "main.js"), []byte("console.log('hi')"), 0o600)
	data := filepath.Join(p, "Local Extension Settings", extID)
	os.MkdirAll(data, 0o700)
	os.WriteFile(filepath.Join(data, "000003.log"), []byte("leveldb data हिंदी"), 0o600)
	os.WriteFile(filepath.Join(data, "LOCK"), nil, 0o600)
	secure := `{"extensions":{"settings":{"` + extID + `":{"location":1,"from_webstore":true,"path":"` + extID + sep + `4.9.133_0",` +
		`"manifest":{"name":"Dark Reader","version":"4.9.133"},"disable_reasons":[],"has_started_service_worker":true}}},"protection":{"macs":{}}}`
	os.WriteFile(filepath.Join(p, "Secure Preferences"), []byte(secure), 0o600)
	return p
}

func TestPackageInstallAcrossOS(t *testing.T) {
	src := fakeProfile(t, `\\`) // registered on Windows
	pkg, err := Package(src, extID)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := Package(src, extID)
	if !bytes.Equal(pkg, again) {
		t.Error("packing the same extension twice must give the same bytes")
	}
	dst := t.TempDir()
	if err := InstallPackage(dst, extID, pkg, "MAC-UUID", "darwin"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "Extensions", extID, "4.9.133_0", "js", "main.js")); err != nil {
		t.Fatal("extension files not installed")
	}
	list, _ := List(dst)
	if len(list) != 1 || list[0].Path != extID+"/4.9.133_0" || list[0].Disabled {
		t.Fatalf("registration wrong on the Mac: %+v", list)
	}
	doc, _ := readJSON(filepath.Join(dst, "Secure Preferences"))
	entry := chromejson.Lookup(doc, "extensions.settings."+extID).(map[string]any)
	if entry["has_started_service_worker"] != nil {
		t.Error("device-only service worker state must not travel")
	}
	want := prefhash.Calculator{DeviceID: "MAC-UUID"}.MAC("extensions.settings."+extID, prefhash.ValueString(entry))
	if got := chromejson.Lookup(doc, "protection.macs.extensions.settings."+extID); got != want {
		t.Error("registration must be signed for the target device")
	}

	// Data: pack, replace, same fingerprint; LOCK is never carried.
	h1, _ := DataHash(src, extID)
	data, _ := PackData(src, extID)
	if err := ReplaceData(dst, extID, data); err != nil {
		t.Fatal(err)
	}
	h2, _ := DataHash(dst, extID)
	if h1 == "" || h1 != h2 {
		t.Fatalf("data fingerprint should match after replace: %s vs %s", h1, h2)
	}
	if _, err := os.Stat(filepath.Join(dst, "Local Extension Settings", extID, "LOCK")); err == nil {
		t.Error("LOCK file must not be copied")
	}

	// Disable, enable, remove.
	SetEnabled(dst, extID, false, "MAC-UUID")
	if l, _ := List(dst); !l[0].Disabled {
		t.Error("disable did not stick")
	}
	SetEnabled(dst, extID, true, "MAC-UUID")
	if l, _ := List(dst); l[0].Disabled {
		t.Error("enable did not stick")
	}
	if err := Remove(dst, extID, "MAC-UUID"); err != nil {
		t.Fatal(err)
	}
	if l, _ := List(dst); len(l) != 0 {
		t.Error("extension still registered after remove")
	}
	if _, err := os.Stat(filepath.Join(dst, "Local Extension Settings", extID)); err == nil {
		t.Error("extension data still there after remove")
	}
}

func evilPackage(t *testing.T, name string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "registration.json", Mode: 0o600, Size: 2})
	tw.Write([]byte("{}"))
	tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: 4})
	tw.Write([]byte("evil"))
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestMaliciousPackagesAreRefused(t *testing.T) {
	for _, name := range []string{"../outside.txt", "Extensions/../../outside.txt", "/abs.txt", "Bookmarks/x", "Extensions/otherid/x.js", `Extensions\` + extID + `\x`} {
		dst := t.TempDir()
		if err := InstallPackage(dst, extID, evilPackage(t, name), "dev", "windows"); err == nil {
			t.Errorf("package with %q was accepted", name)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(dst), "outside.txt")); err == nil {
			t.Fatalf("%q escaped the profile folder", name)
		}
	}
}
