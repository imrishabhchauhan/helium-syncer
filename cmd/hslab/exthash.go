package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/imrishabhchauhan/helium-syncer/internal/extensions"
)

// extHash shows, for a real profile, what each of the two ways of asking
// "did this extension's data change?" costs: packing and compressing all
// of it, or looking at the name, size and change time of each file. Both
// are run twice, because an answer that is not the same twice is no use:
// it would report a change that is not there on every sync.
func extHash(profile string) error {
	ents, err := os.ReadDir(filepath.Join(profile, "Local Extension Settings"))
	if err != nil {
		return err
	}
	var ids []string
	for _, e := range ents {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	fmt.Printf("%-34s %10s %11s %11s\n", "extension", "packed", "pack it", "look at it")
	var packTotal, stampTotal time.Duration
	var bytes int64
	for _, id := range ids {
		if !extensions.SyncsData(profile, id) {
			fmt.Printf("%-34s %10s   (data never sent)\n", id, "-")
			continue
		}
		start := time.Now()
		pkg, h1, err := extensions.PackDataHash(profile, id)
		packed := time.Since(start)
		if err != nil {
			fmt.Printf("%-34s  error: %v\n", id, err)
			continue
		}
		start = time.Now()
		s1 := extensions.DataStamp(profile, id)
		stamped := time.Since(start)

		_, h2, _ := extensions.PackDataHash(profile, id)
		s2 := extensions.DataStamp(profile, id)
		warn := ""
		if h1 != h2 {
			warn += "  PACK IS NOT STEADY"
		}
		if s1 != s2 {
			warn += "  MARK IS NOT STEADY"
		}
		packTotal += packed
		stampTotal += stamped
		bytes += int64(len(pkg))
		fmt.Printf("%-34s %9.1fM %10.0fms %10.1fms%s\n", id, float64(len(pkg))/1e6,
			float64(packed.Microseconds())/1000, float64(stamped.Microseconds())/1000, warn)
	}
	fmt.Printf("\n%-34s %9.1fM %10.0fms %10.1fms\n", "TOTAL for this profile", float64(bytes)/1e6,
		float64(packTotal.Microseconds())/1000, float64(stampTotal.Microseconds())/1000)
	return nil
}
