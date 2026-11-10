// formatcheck checks, read only, that Helium Syncer still understands the
// formats Helium wrote in a profile: the settings signature and the
// bookmarks checksum. Usage: formatcheck <profile-dir>...
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/imrishabhchauhan/helium-syncer/internal/bookmarks"
	"github.com/imrishabhchauhan/helium-syncer/internal/chromejson"
	"github.com/imrishabhchauhan/helium-syncer/internal/deviceid"
	"github.com/imrishabhchauhan/helium-syncer/internal/prefhash"
)

func main() {
	id, err := deviceid.Get()
	if err != nil {
		panic(err)
	}
	for _, p := range os.Args[1:] {
		raw, err := os.ReadFile(filepath.Join(p, "Secure Preferences"))
		if err != nil {
			fmt.Printf("%s: settings: %v\n", filepath.Base(p), err)
		} else if v, err := chromejson.Decode(raw); err != nil {
			fmt.Printf("%s: settings: %v\n", filepath.Base(p), err)
		} else {
			fmt.Printf("%s: settings signature verifies: %v\n", filepath.Base(p), prefhash.Verify(v.(map[string]any), prefhash.Calculator{DeviceID: id}))
		}
		f, err := bookmarks.Load(filepath.Join(p, "Bookmarks"))
		if err != nil {
			fmt.Printf("%s: bookmarks: %v\n", filepath.Base(p), err)
			continue
		}
		md5, _ := f.Checksums()
		fmt.Printf("%s: bookmarks checksum matches: %v\n", filepath.Base(p), md5 == f.Doc["checksum"])
	}
}
