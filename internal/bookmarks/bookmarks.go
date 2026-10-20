// Package bookmarks reads and writes Helium's <profile>/Bookmarks file
// (components/bookmarks/browser/bookmark_codec.cc, Chromium 155).
//
// The document is kept as a generic JSON tree so fields this code does not
// know about (added by future Helium versions) survive a rewrite untouched.
package bookmarks

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"unicode/utf16"

	"github.com/imrishabhchauhan/helium-syncer/internal/chromejson"
)

// Roots in the order Chromium encodes (and checksums) them.
var Roots = []string{"bookmark_bar", "other", "synced"}

// File is a decoded Bookmarks document.
type File struct {
	Doc map[string]any
}

// Load reads and decodes a Bookmarks file.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse decodes Bookmarks JSON.
func Parse(data []byte) (*File, error) {
	v, err := chromejson.Decode(data)
	if err != nil {
		return nil, err
	}
	doc, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("bookmarks: top level is not an object")
	}
	if _, ok := doc["roots"].(map[string]any); !ok {
		return nil, errors.New("bookmarks: no roots")
	}
	return &File{Doc: doc}, nil
}

// Root returns a permanent folder node ("bookmark_bar", "other", "synced").
func (f *File) Root(name string) map[string]any {
	roots, _ := f.Doc["roots"].(map[string]any)
	n, _ := roots[name].(map[string]any)
	return n
}

// Walk visits every node depth-first, pre-order, in Chromium's encode order.
func (f *File) Walk(fn func(node map[string]any, depth int)) {
	var visit func(n map[string]any, d int)
	visit = func(n map[string]any, d int) {
		fn(n, d)
		kids, _ := n["children"].([]any)
		for _, k := range kids {
			if km, ok := k.(map[string]any); ok {
				visit(km, d+1)
			}
		}
	}
	for _, r := range Roots {
		if n := f.Root(r); n != nil {
			visit(n, 0)
		}
	}
}

// Checksums returns the MD5 ("checksum") and SHA-256 ("checksum_sha256")
// values Chromium computes: for each node in encode order, the UTF-8 id, the
// UTF-16LE title, then "url"+url or "folder".
func (f *File) Checksums() (md5hex, sha256hex string) {
	m, s := md5.New(), sha256.New()
	both := []hash.Hash{m, s}
	put := func(b []byte) {
		for _, h := range both {
			h.Write(b)
		}
	}
	f.Walk(func(n map[string]any, _ int) {
		put([]byte(str(n["id"])))
		put(utf16le(str(n["name"])))
		if str(n["type"]) == "url" {
			put([]byte("url"))
			put([]byte(str(n["url"])))
		} else {
			put([]byte("folder"))
		}
	})
	return hex.EncodeToString(m.Sum(nil)), hex.EncodeToString(s.Sum(nil))
}

// MaxID returns the largest numeric node id.
func (f *File) MaxID() int64 {
	var max int64
	f.Walk(func(n map[string]any, _ int) {
		if id, err := strconv.ParseInt(str(n["id"]), 10, 64); err == nil && id > max {
			max = id
		}
	})
	return max
}

// FindGUID returns the node with this guid, or nil.
func (f *File) FindGUID(guid string) map[string]any {
	var found map[string]any
	f.Walk(func(n map[string]any, _ int) {
		if found == nil && str(n["guid"]) == guid {
			found = n
		}
	})
	return found
}

// Encode recomputes the checksums and serialises the file the way Chromium
// writes it on goos ("windows" uses CRLF line endings).
func (f *File) Encode(goos string) []byte {
	sum, sum256 := f.Checksums()
	f.Doc["checksum"] = sum
	if _, ok := f.Doc["checksum_sha256"]; ok {
		f.Doc["checksum_sha256"] = sum256
	}
	return []byte(chromejson.Pretty(f.Doc, chromejson.EOL(goos)))
}

// Save writes the file atomically (temp file in the same folder + rename).
// The caller must make sure Helium is not running on this profile.
func (f *File) Save(path string) error {
	data := f.Encode(runtime.GOOS)
	tmp, err := os.CreateTemp(filepath.Dir(path), ".Bookmarks.hs-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("bookmarks: replace %s: %w", path, err)
	}
	return nil
}

func str(v any) string { s, _ := v.(string); return s }

func utf16le(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		b[2*i] = byte(c)
		b[2*i+1] = byte(c >> 8)
	}
	return b
}
