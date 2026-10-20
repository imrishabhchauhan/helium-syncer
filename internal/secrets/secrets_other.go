//go:build !windows && !darwin

package secrets

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// ErrNotFound means nothing is stored under that name.
var ErrNotFound = errors.New("secrets: not found")

// Save stores data in a file only this user can read (no OS key store yet).
func Save(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name+".secret"), data, 0o600)
}

// Load returns what Save stored.
func Load(dir, name string) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(dir, name+".secret"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	return b, err
}

// Delete removes a stored secret.
func Delete(dir, name string) error {
	err := os.Remove(filepath.Join(dir, name+".secret"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
