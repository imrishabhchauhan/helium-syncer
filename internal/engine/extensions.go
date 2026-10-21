package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

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
			key := id + "@" + version
			if b, ok := ps.ExtBlobs[key]; ok || !upload {
				return b, nil
			}
			pkg, err := extensions.Package(dir, id)
			if err != nil {
				return "", err
			}
			b, err := blobs.PutBlob(ctx, pkg)
			if err != nil {
				return "", fmt.Errorf("uploading extension %s: %w", id, err)
			}
			ps.ExtBlobs[key] = b
			return b, nil
		}
	}
	dataFor := func(upload bool) func(id string) (merge.ExtData, error) {
		return func(id string) (merge.ExtData, error) {
			h, err := extensions.DataHash(dir, id)
			if err != nil || h == "" {
				return merge.ExtData{}, err
			}
			if d := ps.DataBlobs[id]; d.Hash == h || !upload {
				return merge.ExtData{Hash: h, Blob: d.Blob}, nil
			}
			data, err := extensions.PackData(dir, id)
			if err != nil {
				return merge.ExtData{}, err
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
