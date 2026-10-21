package extensions

import (
	"io/fs"
	"path/filepath"
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

// SyncsData reports whether this extension's data may sync.
func SyncsData(profile, id string) bool {
	if _, no := noDataSync[id]; no {
		return false
	}
	return dataSize(profile, id) <= MaxData
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
