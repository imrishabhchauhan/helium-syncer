package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/imrishabhchauhan/helium-syncer/internal/crdt"
	"github.com/imrishabhchauhan/helium-syncer/internal/extensions"
	"github.com/imrishabhchauhan/helium-syncer/internal/merge"
)

// BlobStore holds encrypted files (extension packages and data). The
// cloud client implements it; a Transport without it syncs no extensions.
type BlobStore interface {
	PutBlob(ctx context.Context, data []byte) (string, error)
	GetBlob(ctx context.Context, id string) ([]byte, error)
}

// packageFor returns the package already uploaded for this extension, and
// remembers it under the version asked for.
//
// Helium updates extensions in place, so the version installed here soon
// differs from the one that was uploaded. The uploaded package still
// installs the extension on the other computer, which then gets the update
// from Helium itself, so a fresh upload of up to 44 MB buys nothing.
//
// Remembering it is what stops a loop. Two passes run over the same
// extensions: one that may upload, and one that only records what is there.
// The first fell back to the older package, the second did not, so the two
// never matched and every sync reported the same extensions as changed and
// sent them again. The owner's Mac sent the same four every five minutes,
// for ever.
func packageFor(blobs map[string]string, id, version string) string {
	key := id + "@" + version
	if b, ok := blobs[key]; ok {
		return b
	}
	var older []string
	for k := range blobs {
		if strings.HasPrefix(k, id+"@") {
			older = append(older, k)
		}
	}
	if len(older) == 0 {
		return ""
	}
	// Sorted, so the answer is the same on every run. Going over the map
	// as it comes could pick a different package each time, which is the
	// same loop again.
	sort.Strings(older)
	b := blobs[older[len(older)-1]]
	blobs[key] = b
	return b
}

// syncExtensions uploads this profile's extensions (once per version) and
// their data (when it changed), then installs, updates, removes, enables,
// disables and loads data as decided by the merge. Every folder it changes
// is copied into the run's backup first.
func syncExtensions(ctx context.Context, cfg Config, ps *ProfileState, dir string, clock *crdt.Clock, incoming []crdt.Op, j *journal) ([]crdt.Op, error) {
	blobs, ok := cfg.Transport.(BlobStore)
	if !ok {
		return nil, nil
	}
	if ps.ExtBlobs == nil {
		ps.ExtBlobs = map[string]string{}
	}
	if ps.DataBlobs == nil {
		ps.DataBlobs = map[string]merge.ExtData{}
	}
	pkgFor := func(upload bool) func(id, version string) (string, error) {
		return func(id, version string) (string, error) {
			if b := packageFor(ps.ExtBlobs, id, version); b != "" {
				return b, nil
			}
			if !upload {
				return "", nil
			}
			if !extensions.SyncsPackage(dir, id) {
				return "", nil // too big for the server: name and state still sync
			}
			pkg, err := extensions.Package(dir, id)
			if err != nil {
				return "", err
			}
			b, err := blobs.PutBlob(ctx, pkg)
			if err != nil {
				return "", fmt.Errorf("uploading extension %s: %w", id, err)
			}
			ps.ExtBlobs[id+"@"+version] = b
			return b, nil
		}
	}
	dataFor := func(upload bool) func(id string) (merge.ExtData, error) {
		return func(id string) (merge.ExtData, error) {
			if !extensions.SyncsData(dir, id) {
				return merge.ExtData{}, nil // password manager, or too big: never sent
			}
			data, h, err := extensions.PackDataHash(dir, id) // packed once per sync
			if err != nil || h == "" {
				return merge.ExtData{}, err
			}
			if d := ps.DataBlobs[id]; d.Hash == h || !upload {
				return merge.ExtData{Hash: h, Blob: d.Blob}, nil
			}
			b, err := blobs.PutBlob(ctx, data)
			if err != nil {
				return merge.ExtData{}, fmt.Errorf("uploading data of extension %s: %w", id, err)
			}
			ps.DataBlobs[id] = merge.ExtData{Hash: h, Blob: b}
			return ps.DataBlobs[id], nil
		}
	}
	curExts, curData, err := merge.ExtractExtensions(dir, pkgFor(true), dataFor(true))
	if err != nil {
		return nil, err
	}
	out := crdt.Diff(merge.TypeExtension, ps.ExtBase, curExts, clock)
	out = append(out, crdt.Diff(merge.TypeExtData, ps.ExtDataBase, curData, clock)...)
	plan := merge.PlanExtensions(curExts, curData, ps.Exts, ps.ExtData, clock, incoming, out)

	saveFolders := func(id string) error {
		for _, rel := range []string{filepath.Join("Extensions", id), filepath.Join("Local Extension Settings", id),
			filepath.Join("Sync Extension Settings", id)} {
			src := filepath.Join(dir, rel)
			if exists(src) {
				profileRel, _ := filepath.Rel(cfg.UserData, src)
				if err := copyDir(src, filepath.Join(j.Backup, profileRel)); err != nil {
					return err
				}
			} else {
				j.Created = append(j.Created, src)
			}
		}
		return writeJournal(cfg.StateDir, *j)
	}
	item := func(id string) (merge.ExtItem, bool) {
		var it merge.ExtItem
		v, ok := ps.Exts.Get(id)
		return it, ok && json.Unmarshal(v, &it) == nil
	}
	secure := filepath.Join(dir, "Secure Preferences")
	changed := false
	for _, id := range plan.Install {
		it, ok := item(id)
		if !ok || it.Package == "" {
			continue // the other device has not uploaded the package yet
		}
		pkg, err := blobs.GetBlob(ctx, it.Package)
		if err != nil {
			return nil, fmt.Errorf("downloading extension %s: %w", it.Name, err)
		}
		if err := saveFolders(id); err != nil {
			return nil, err
		}
		if err := closed(cfg); err != nil {
			return nil, err
		}
		if err := extensions.InstallPackage(dir, id, pkg, cfg.MachineID, cfg.GOOS); err != nil {
			return nil, fmt.Errorf("installing extension %s: %w", it.Name, err)
		}
		ps.ExtBlobs[id+"@"+it.Version] = it.Package
		changed = true
	}
	for _, id := range plan.Remove {
		if err := saveFolders(id); err != nil {
			return nil, err
		}
		if err := closed(cfg); err != nil {
			return nil, err
		}
		if err := extensions.Remove(dir, id, cfg.MachineID); err != nil {
			return nil, err
		}
		delete(ps.DataBlobs, id)
		changed = true
	}
	for _, id := range plan.Enable {
		if err := closed(cfg); err != nil {
			return nil, err
		}
		if err := extensions.SetEnabled(dir, id, true, cfg.MachineID); err != nil {
			return nil, err
		}
		changed = true
	}
	for _, id := range plan.Disable {
		if err := closed(cfg); err != nil {
			return nil, err
		}
		if err := extensions.SetEnabled(dir, id, false, cfg.MachineID); err != nil {
			return nil, err
		}
		changed = true
	}
	for _, id := range plan.LoadData {
		if extensions.NeverSyncsData(id) {
			continue // a password manager's data stays on its own computer
		}
		raw, _ := ps.ExtData.Get(id)
		var d merge.ExtData
		if json.Unmarshal(raw, &d) != nil || d.Blob == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "Extensions", id)); err != nil {
			continue // not installed here (yet)
		}
		data, err := blobs.GetBlob(ctx, d.Blob)
		if err != nil {
			return nil, fmt.Errorf("downloading extension data: %w", err)
		}
		if err := saveFolders(id); err != nil {
			return nil, err
		}
		if err := closed(cfg); err != nil {
			return nil, err
		}
		if err := extensions.ReplaceData(dir, id, data); err != nil {
			return nil, err
		}
		ps.DataBlobs[id] = d
		changed = true
	}
	if changed {
		j.Written = append(j.Written, secure)
	}
	// New base: what is installed now, referring to the blobs it came from.
	ps.ExtBase, ps.ExtDataBase, err = merge.ExtractExtensions(dir, pkgFor(false), dataFor(false))
	return out, err
}
