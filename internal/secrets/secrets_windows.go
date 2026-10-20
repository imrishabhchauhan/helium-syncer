// Package secrets keeps the sync code in the operating system's protected
// storage: DPAPI on Windows (only this Windows user can decrypt it), the
// login Keychain on macOS.
package secrets

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ErrNotFound means nothing is stored under that name.
var ErrNotFound = errors.New("secrets: not found")

func blobPath(dir, name string) string { return filepath.Join(dir, name+".dpapi") }

// Save stores data under name, encrypted with DPAPI for the current user.
func Save(dir, name string, data []byte) error {
	in := windows.DataBlob{Size: uint32(len(data))}
	if len(data) > 0 {
		in.Data = &data[0]
	}
	var out windows.DataBlob
	desc, _ := windows.UTF16PtrFromString("Helium Syncer")
	if err := windows.CryptProtectData(&in, desc, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	enc := unsafe.Slice(out.Data, out.Size)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := blobPath(dir, name) + ".tmp"
	if err := os.WriteFile(tmp, enc, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, blobPath(dir, name))
}

// Load returns what Save stored.
func Load(dir, name string) ([]byte, error) {
	enc, err := os.ReadFile(blobPath(dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(enc) == 0 {
		return nil, ErrNotFound
	}
	in := windows.DataBlob{Size: uint32(len(enc)), Data: &enc[0]}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

// Delete removes a stored secret.
func Delete(dir, name string) error {
	err := os.Remove(blobPath(dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
