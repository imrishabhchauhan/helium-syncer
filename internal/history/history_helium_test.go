//go:build helium

// End-to-end test against a real Helium install, on a throwaway profile.
// Run: go test -tags helium ./internal/history/
package history

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

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

func TestHistoryInsertWithHelium(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	root, _ := filepath.Abs("../..")
	ud := filepath.Join(root, "test-profiles", fmt.Sprintf("it-history-%d", time.Now().Unix()))
	opts := testkit.Options{Exe: heliumExe(t), UserDataDir: ud, Port: 9352, ProbeDir: filepath.Join(root, "testing", "probe-extension")}

	// 1. Local history made by Helium itself.
	in, err := testkit.Start(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"https://local.example/one", "https://local.example/two"} {
		if err := in.Eval(ctx, fmt.Sprintf("chrome.history.addUrl({url:%q})", u), nil); err != nil {
			in.Kill()
			t.Fatal(err)
		}
	}
	if err := in.Close(ctx); err != nil {
		t.Fatal(err)
	}

	// 2. Write visits "from the Mac", 3 days ago, with original timestamps.
	path := filepath.Join(ud, "Default", "History")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	local, err := db.Visits(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(local) < 2 {
		t.Fatalf("expected Helium's own visits, got %d", len(local))
	}
	base := time.Now().Add(-72 * time.Hour).UnixMilli()
	remote := []Visit{
		// Real visits carry chain flags; the second one deliberately lacks them
		// to prove AddVisits normalises.
		{URL: "https://mac.example/hindi", Title: "हिंदी समाचार", VisitTime: FromUnixMillis(base), Transition: 0x30000001, OriginDevice: "mac-test", OriginVisitID: 11},
		{URL: "https://mac.example/emoji", Title: "Rocket 🚀 <b>", VisitTime: FromUnixMillis(base + 60_000), Transition: 0, OriginDevice: "mac-test", OriginVisitID: 12},
		{URL: "https://local.example/one", Title: "", VisitTime: FromUnixMillis(base + 120_000), Transition: 0, OriginDevice: "mac-test", OriginVisitID: 13},
	}
	n, err := db.AddVisits(remote)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("added %d visits, want 3", n)
	}
	if n, _ := db.AddVisits(remote); n != 0 {
		t.Errorf("re-applying the same batch added %d visits, want 0", n)
	}
	db.Close()

	// 3. Helium must show them with the original times and keep its own.
	in, err = testkit.Start(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	var items []struct {
		URL           string  `json:"url"`
		Title         string  `json:"title"`
		LastVisitTime float64 `json:"lastVisitTime"`
		VisitCount    int     `json:"visitCount"`
		TypedCount    int     `json:"typedCount"`
	}
	err = in.Eval(ctx, "probe.history(1000)", &items)
	in.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byURL := map[string]int{}
	for i, it := range items {
		byURL[it.URL] = i
	}
	check := func(url, title string, ms int64) {
		i, ok := byURL[url]
		if !ok {
			t.Errorf("Helium does not show %s", url)
			return
		}
		if title != "" && items[i].Title != title {
			t.Errorf("%s title %q, want %q", url, items[i].Title, title)
		}
		if ms != 0 && int64(items[i].LastVisitTime) != ms {
			t.Errorf("%s lastVisitTime %d, want %d", url, int64(items[i].LastVisitTime), ms)
		}
	}
	check("https://mac.example/hindi", "हिंदी समाचार", base)
	check("https://mac.example/emoji", "Rocket 🚀 <b>", base+60_000)
	check("https://local.example/one", "", 0)
	check("https://local.example/two", "", 0)
	if i, ok := byURL["https://mac.example/hindi"]; ok && items[i].TypedCount != 1 {
		t.Errorf("typed visit should count as typed, got typedCount %d", items[i].TypedCount)
	}

	// 4. After Helium ran, the synced visits are still on disk with origin tags.
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	all, _ := db.Visits(0)
	tagged := 0
	for _, v := range all {
		if v.OriginDevice == "mac-test" {
			tagged++
		}
	}
	if tagged != 3 {
		t.Errorf("found %d visits tagged from mac-test after Helium ran, want 3", tagged)
	}
}
