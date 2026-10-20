//go:build helium

// End-to-end test against a real Helium install, on a throwaway profile.
// Run: go test -tags helium ./internal/bookmarks/
package bookmarks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
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

func TestBookmarksRoundTripWithHelium(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	root, _ := filepath.Abs("../..")
	ud := filepath.Join(root, "test-profiles", fmt.Sprintf("it-bookmarks-%d", time.Now().Unix()))
	opts := testkit.Options{Exe: heliumExe(t), UserDataDir: ud, Port: 9351, ProbeDir: filepath.Join(root, "testing", "probe-extension")}

	// 1. Helium itself creates bookmarks with tricky titles.
	titles := []struct{ folder, title, url string }{
		{"", "Plain", "https://example.com/"},
		{"हिंदी किताबें", "हिंदी पेज", "https://hi.wikipedia.org/wiki/हिन्दी"},
		{"Emoji 😀🚀", "Rocket 🚀", "https://example.com/?q=a&b=c#frag"},
		{"Emoji 😀🚀", `Tags <b>&</b> "quotes" \ back`, "https://example.com/<x>"},
	}
	in, err := testkit.Start(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range titles {
		expr := fmt.Sprintf("probe.addBookmark(%s, %s, %s)", strconv.Quote(b.folder), strconv.Quote(b.title), strconv.Quote(b.url))
		if err := in.Eval(ctx, expr, nil); err != nil {
			in.Kill()
			t.Fatal(err)
		}
	}
	if err := in.Close(ctx); err != nil {
		t.Fatal(err)
	}

	// 2. Our checksum and serialiser must reproduce Helium's file exactly.
	path := filepath.Join(ud, "Default", "Bookmarks")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	sum, sum256 := f.Checksums()
	if sum != str(f.Doc["checksum"]) {
		t.Errorf("md5 checksum: ours %s, Helium %s", sum, f.Doc["checksum"])
	}
	if s, ok := f.Doc["checksum_sha256"]; ok && sum256 != str(s) {
		t.Errorf("sha256 checksum: ours %s, Helium %s", sum256, s)
	}
	if got := string(f.Encode(runtime.GOOS)); got != string(raw) {
		t.Errorf("re-encoded file differs from Helium's (%d vs %d bytes)", len(got), len(raw))
	}

	// 3. Add a bookmark the way a sync from another device would.
	guid := uuid.NewString()
	now := strconv.FormatInt(time.Now().UnixMicro()+11644473600*1_000_000, 10)
	bar := f.Root("bookmark_bar")
	bar["children"] = append(bar["children"].([]any), map[string]any{
		"id": strconv.FormatInt(f.MaxID()+1, 10), "guid": guid, "name": "Synced from Mac ✓",
		"type": "url", "url": "https://example.com/synced", "date_added": now, "date_last_used": "0",
	})
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}

	// 4. Helium must show it, and keep everything else.
	in, err = testkit.Start(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	var tree []map[string]any
	if err := in.Eval(ctx, "chrome.bookmarks.getTree()", &tree); err != nil {
		in.Kill()
		t.Fatal(err)
	}
	if err := in.Close(ctx); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var collect func(n map[string]any)
	collect = func(n map[string]any) {
		if s, _ := n["title"].(string); s != "" {
			seen[s] = true
		}
		kids, _ := n["children"].([]any)
		for _, k := range kids {
			collect(k.(map[string]any))
		}
	}
	for _, n := range tree {
		collect(n)
	}
	for _, want := range []string{"Synced from Mac ✓", "Plain", "हिंदी पेज", "Rocket 🚀", `Tags <b>&</b> "quotes" \ back`} {
		if !seen[want] {
			t.Errorf("Helium does not show bookmark %q after restart", want)
		}
	}
	after, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.FindGUID(guid) == nil {
		t.Error("Helium dropped or re-keyed the synced bookmark's guid")
	}
}
