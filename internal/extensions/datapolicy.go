package extensions

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"sort"
)

// noDataSync are extensions whose data never syncs. Password managers keep
// keys and sessions that belong to one device: a copy can lock the person
// out or mix up two devices. The extension itself still installs; the
// person signs in to it on each computer.
var noDataSync = map[string]string{
	"aeblfdkhhhdcdjpifhhbdiojplfjncoa": "1Password",
	"nngceckbapebfimnlniiiahkandclblb": "Bitwarden",
	"hdokiejnpimakedhajhdlcegeplioahd": "LastPass",
	"fdjamakpfbbddfjaooikfcpapjohcfmg": "Dashlane",
	"bfogiafebfohielmmehodmfbbebbbpei": "Keeper",
	"ghmbeldphafepmbegfdlkpapadhbakde": "Proton Pass",
	"fooolghllnmhmmndgjiamiiodkpenpbb": "NordPass",
	"pnlccmojcmeohlpggmfnbbiapkmbliob": "RoboForm",
	"kmcfomidfpdkfieipokbalgegidffkal": "Enpass",
	"oboonakemofpalcgghocfoadofidjkkk": "KeePassXC-Browser",
}

// MaxData is the largest extension data that syncs. Bigger data (for
// example a downloaded block list) costs a large upload on every change.
const MaxData = 8 << 20

// MaxPackage is the largest extension package that syncs. The server
// refuses any single file over 25 MB, so without this one very large
// extension stops the whole sync. A skipped extension still syncs its
// name and whether it is on; the person installs it from the Web Store
// on the other computer, which is what Helium would do anyway.
const MaxPackage = 20 << 20

// SyncsData reports whether this extension's data may sync.
func SyncsData(profile, id string) bool {
	if _, no := noDataSync[id]; no {
		return false
	}
	return dataSize(profile, id) <= MaxData
}

// SyncsPackage reports whether this extension's own files may sync.
func SyncsPackage(profile, id string) bool {
	return packageSize(profile, id) <= MaxPackage
}

func packageSize(profile, id string) int64 {
	var n int64
	filepath.WalkDir(filepath.Join(profile, "Extensions", id), func(_ string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() && !skipFile(e.Name()) {
			if info, err := e.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// NeverSyncsData reports whether data from another computer must not be
// written for this extension, whatever its size.
func NeverSyncsData(id string) bool {
	_, no := noDataSync[id]
	return no
}

func dataSize(profile, id string) int64 {
	var n int64
	for _, d := range dataDirs(id) {
		filepath.WalkDir(filepath.Join(profile, d), func(_ string, e fs.DirEntry, err error) error {
			if err == nil && !e.IsDir() && !skipFile(e.Name()) {
				if info, err := e.Info(); err == nil {
					n += info.Size()
				}
			}
			return nil
		})
	}
	return n
}

// PackDataHash bundles an extension's data and returns the bundle with its
// hash, so one sync packs the data only once.
func PackDataHash(profile, id string) (pkg []byte, hash string, err error) {
	pkg, n, err := packData(profile, id)
	if err != nil || n == 0 {
		return nil, "", err
	}
	return pkg, hashOf(pkg), nil
}

// DataStamp is a cheap mark of an extension's data: the name, size and
// change time of every file in its folders. It answers "did anything
// change?" without reading or compressing a byte.
//
// This matters most on a laptop. On the owner's Mac the two profiles hold
// 165 MB of extension data in 83 files. Packing and compressing all of it
// to work out that nothing had changed took 4.3 seconds of the processor
// every five minutes, for ever. Looking at the same files takes about 7
// milliseconds.
//
// It is safe because extension data only changes while Helium runs, and
// nothing of Helium's is read or written while it runs.
func DataStamp(profile, id string) string {
	h := sha256.New()
	for _, d := range dataDirs(id) {
		root := filepath.Join(profile, d)
		var lines []string
		filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() || skipFile(e.Name()) {
				return nil
			}
			info, err := e.Info()
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(profile, p)
			lines = append(lines, fmt.Sprintf("%s\x00%d\x00%d", filepath.ToSlash(rel), info.Size(), info.ModTime().UnixNano()))
			return nil
		})
		// A folder read gives no order, so sort: the same files must give
		// the same mark every time, or the check is worthless.
		sort.Strings(lines)
		for _, l := range lines {
			io.WriteString(h, l)
			io.WriteString(h, "\n")
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}
