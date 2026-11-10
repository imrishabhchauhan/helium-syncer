package extensions

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/imrishabhchauhan/helium-syncer/internal/chromejson"
	"github.com/imrishabhchauhan/helium-syncer/internal/prefhash"
)

// InstallLocal must write the entry exactly as Helium needs it to run the
// extension on the next start, with no flags and nobody clicking anything.
// Each check below is a way the first version failed in a real Helium.
func TestInstallLocalWritesWhatHeliumRuns(t *testing.T) {
	const id = "mhealbgfgbmpmodihicdniadpbkdbkkl"
	const device = "S-1-5-21-TEST"
	profile := t.TempDir()
	os.WriteFile(filepath.Join(profile, "Secure Preferences"), []byte(`{"protection":{"macs":{}}}`), 0o600)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "manifest.json"),
		[]byte(`{"manifest_version":3,"name":"Helium Syncer","version":"0.1.0","permissions":["bookmarks","alarms"]}`), 0o600)

	if err := InstallLocal(profile, id, dir, device, "windows"); err != nil {
		t.Fatal(err)
	}
	doc, err := readJSON(filepath.Join(profile, "Secure Preferences"))
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := chromejson.Lookup(doc, "extensions.settings."+id).(map[string]any)
	if entry == nil {
		t.Fatal("no entry written")
	}

	if got := intOf(entry["location"]); got != LocationUnpacked {
		t.Errorf("location %d, want %d: 5 is removed by Helium and 8 is forgotten on restart", got, LocationUnpacked)
	}
	// Without granted permissions Helium keeps the extension switched off.
	granted, _ := entry["granted_permissions"].(map[string]any)
	if granted == nil || !reflect.DeepEqual(granted["api"], []any{"alarms", "bookmarks"}) {
		t.Errorf("granted_permissions = %v, want api [alarms bookmarks]", entry["granted_permissions"])
	}
	if _, ok := granted["explicit_host"]; !ok {
		t.Error(`permissions must use Helium's key "explicit_host"`)
	}
	if _, ok := entry["manifest"]; ok {
		t.Error("a folder extension's entry must not carry a manifest; Helium reads it from the folder")
	}
	// Helium turns folder extensions off while developer mode is off.
	if chromejson.Lookup(doc, DeveloperModePath) != true {
		t.Error("developer mode not switched on")
	}

	// Both protected values must be signed for this device, or Helium resets
	// them on start.
	c := prefhash.Calculator{DeviceID: device}
	for _, p := range []string{"extensions.settings." + id, DeveloperModePath} {
		want := c.MAC(p, prefhash.ValueString(chromejson.Lookup(doc, p)))
		if got := chromejson.Lookup(doc, "protection.macs."+p); got != want {
			t.Errorf("%s is not signed for this device", p)
		}
	}
	if !LocalInstalled(profile, id, dir, "windows") {
		t.Error("LocalInstalled does not see the install")
	}
}
