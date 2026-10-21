//go:build helium

// End-to-end test against a real Helium install, on a throwaway profile.
// Run: go test -tags helium ./internal/profiles/
package profiles

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/imrishabhchauhan/helium-syncer/internal/chromejson"
	"github.com/imrishabhchauhan/helium-syncer/internal/testkit"
)

func heliumExe(t *testing.T) string {
	if p := os.Getenv("HELIUM_EXE"); p != "" {
		return p
	}
	switch runtime.GOOS {
	case "windows":
		return `C:\Program Files\imput\Helium\Application\chrome.exe`
	case "darwin":
		return "/Applications/Helium.app/Contents/MacOS/Helium"
	}
	t.Skip("no Helium for this OS")
	return ""
}

func TestAddProfilesWithHelium(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	root, _ := filepath.Abs("../..")
	ud := filepath.Join(root, "test-profiles", fmt.Sprintf("it-profiles-%d", time.Now().Unix()))
	probe := filepath.Join(root, "testing", "probe-extension")
	opts := testkit.Options{Exe: heliumExe(t), UserDataDir: ud, Port: 9354, ProbeDir: probe}

	// 1. A fresh Helium with its default profile, plus a bookmark in it.
	in, err := testkit.Start(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := in.Eval(ctx, `probe.addBookmark("", "Keep me", "https://keep.example/")`, nil); err != nil {
		in.Kill()
		t.Fatal(err)
	}
	if err := in.Close(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := List(ud)
	if err != nil {
		t.Fatal(err)
	}
	defaultBookmarks, _ := os.ReadFile(filepath.Join(ud, "Default", "Bookmarks"))

	// 2. Add the other device's profiles next to it.
	added := map[string]string{}
	for i, name := range []string{"Personal", "Work 💼"} {
		dir, err := Add(ud, name, 26+i)
		if err != nil {
			t.Fatal(err)
		}
		added[dir] = name
	}

	// 3. Helium opens each new profile, and keeps their names.
	for dir := range added {
		o := opts
		o.ProfileDir = dir
		in, err := testkit.Start(ctx, o)
		if err != nil {
			t.Fatalf("open %s: %v", dir, err)
		}
		if err := in.Eval(ctx, `probe.addBookmark("", "In new profile", "https://new.example/")`, nil); err != nil {
			in.Kill()
			t.Fatal(err)
		}
		if err := in.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(ud, dir, "Bookmarks")); err != nil {
			t.Errorf("%s: Helium did not create its files: %v", dir, err)
		}
	}
	after, err := List(ud)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("before: %v  after: %v", before, after)
	names := map[string]string{}
	for _, p := range after {
		names[p.Dir] = p.Name
	}
	for dir, name := range added {
		if names[dir] != name {
			t.Errorf("profile %s is named %q, want %q", dir, names[dir], name)
		}
	}
	for _, p := range before {
		if names[p.Dir] != p.Name {
			t.Errorf("existing profile %s changed name %q -> %q", p.Dir, p.Name, names[p.Dir])
		}
	}
	now, _ := os.ReadFile(filepath.Join(ud, "Default", "Bookmarks"))
	if string(now) != string(defaultBookmarks) {
		t.Error("the existing Default profile's bookmarks changed")
	}
}

// Helium keeps the picture that Helium Syncer writes: for a new profile,
// and after a change, when Helium starts and quits again.
func TestPictureWithHelium(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	root, _ := filepath.Abs("../..")
	ud := filepath.Join(root, "test-profiles", fmt.Sprintf("it-picture-%d", time.Now().Unix()))
	probe := filepath.Join(root, "testing", "probe-extension")
	opts := testkit.Options{Exe: heliumExe(t), UserDataDir: ud, Port: 9355, ProbeDir: probe}
	in, err := testkit.Start(ctx, opts) // a fresh Helium makes its Local State
	if err != nil {
		t.Fatal(err)
	}
	if err := in.Close(ctx); err != nil {
		t.Fatal(err)
	}
	dir, err := Add(ud, "Picture", 31)
	if err != nil {
		t.Fatal(err)
	}
	check := func(want int, wantDefault bool, when string) {
		t.Helper()
		o := opts
		o.ProfileDir = dir
		in, err := testkit.Start(ctx, o)
		if err != nil {
			t.Fatalf("%s: open: %v", when, err)
		}
		time.Sleep(2 * time.Second)
		if err := in.Close(ctx); err != nil {
			t.Fatal(err)
		}
		list, _ := List(ud)
		for _, p := range list {
			if p.Dir == dir {
				if p.Avatar != want || p.DefaultAvatar != wantDefault {
					t.Fatalf("%s: Helium shows picture %d (default %v), want %d (default %v)", when, p.Avatar, p.DefaultAvatar, want, wantDefault)
				}
				return
			}
		}
		t.Fatalf("%s: profile gone", when)
	}
	check(31, false, "new profile")
	if err := SetAvatar(ud, dir, 40, false); err != nil {
		t.Fatal(err)
	}
	check(40, false, "after a change")
}

// Helium keeps the colours that Helium Syncer writes when the profile has
// the same theme colour (the theme syncs as a setting).
func TestColorsWithHelium(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	root, _ := filepath.Abs("../..")
	ud := filepath.Join(root, "test-profiles", fmt.Sprintf("it-colors-%d", time.Now().Unix()))
	probe := filepath.Join(root, "testing", "probe-extension")
	opts := testkit.Options{Exe: heliumExe(t), UserDataDir: ud, Port: 9356, ProbeDir: probe}
	in, err := testkit.Start(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := in.Close(ctx); err != nil {
		t.Fatal(err)
	}
	dir, err := Add(ud, "Colors", 26)
	if err != nil {
		t.Fatal(err)
	}
	// The theme colour, as the settings sync writes it.
	prefsPath := filepath.Join(ud, dir, "Preferences")
	data, _ := os.ReadFile(prefsPath)
	v, _ := chromejson.Decode(data)
	p := v.(map[string]any)
	chromejson.Set(p, "browser.theme.user_color2", json.Number("-7558172"))
	chromejson.Set(p, "browser.theme.color_variant2", json.Number("1"))
	os.WriteFile(prefsPath, []byte(chromejson.Compact(p)), 0o600)
	open := func() map[string]int64 {
		o := opts
		o.ProfileDir = dir
		in, err := testkit.Start(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(3 * time.Second)
		if err := in.Close(ctx); err != nil {
			t.Fatal(err)
		}
		list, _ := List(ud)
		for _, p := range list {
			if p.Dir == dir {
				return p.Colors
			}
		}
		t.Fatal("profile gone")
		return nil
	}
	made := open()
	t.Logf("colours Helium makes from the theme: %v", made)
	if len(made) == 0 {
		t.Fatal("Helium made no colours from the theme colour")
	}
	// Write those colours to a second profile with the same theme: kept.
	dir2, _ := Add(ud, "Colors 2", 26)
	os.WriteFile(filepath.Join(ud, dir2, "Preferences"), []byte(chromejson.Compact(p)), 0o600)
	if err := SetColors(ud, dir2, made); err != nil {
		t.Fatal(err)
	}
	dir = dir2
	if got := open(); fmt.Sprint(got) != fmt.Sprint(made) {
		t.Fatalf("Helium changed the written colours: %v, want %v", got, made)
	}
}
