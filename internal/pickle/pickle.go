// Package pickle reads and writes Chromium base::Pickle buffers
// (base/pickle.cc): a uint32 payload-size header, then fields aligned to
// 4 bytes, little-endian. Strings are an int32 length plus bytes (UTF-16
// code units for String16), padded to 4.
package pickle

import (
	"encoding/binary"
	"errors"
	"unicode/utf16"
)

// ErrShort is returned when a read runs past the payload.
var ErrShort = errors.New("pickle: read past end")

// Writer builds a pickle.
type Writer struct{ buf []byte }

func (w *Writer) pad() {
	for len(w.buf)%4 != 0 {
		w.buf = append(w.buf, 0)
	}
}

func (w *Writer) Int(v int32)     { w.buf = binary.LittleEndian.AppendUint32(w.buf, uint32(v)) }
func (w *Writer) Uint32(v uint32) { w.buf = binary.LittleEndian.AppendUint32(w.buf, v) }
func (w *Writer) Int64(v int64)   { w.buf = binary.LittleEndian.AppendUint64(w.buf, uint64(v)) }
func (w *Writer) Uint64(v uint64) { w.buf = binary.LittleEndian.AppendUint64(w.buf, v) }

func (w *Writer) Bool(v bool) {
	if v {
		w.Int(1)
	} else {
		w.Int(0)
	}
}

func (w *Writer) String(s string) {
	w.Int(int32(len(s)))
	w.buf = append(w.buf, s...)
	w.pad()
}

func (w *Writer) String16(s string) {
	u := utf16.Encode([]rune(s))
	w.Int(int32(len(u)))
	for _, c := range u {
		w.buf = binary.LittleEndian.AppendUint16(w.buf, c)
	}
	w.pad()
}

// Bytes returns the full pickle: header + payload.
func (w *Writer) Bytes() []byte {
	out := binary.LittleEndian.AppendUint32(nil, uint32(len(w.buf)))
	return append(out, w.buf...)
}

// Reader iterates over a pickle's payload.
type Reader struct {
	p   []byte
	off int
}

// NewReader parses the header of data.
func NewReader(data []byte) (*Reader, error) {
	if len(data) < 4 {
		return nil, ErrShort
	}
	n := int(binary.LittleEndian.Uint32(data))
	if n > len(data)-4 {
		return nil, ErrShort
	}
	return &Reader{p: data[4 : 4+n]}, nil
}

// Remaining reports whether unread payload is left.
func (r *Reader) Remaining() int { return len(r.p) - r.off }

func (r *Reader) take(n int) ([]byte, error) {
	if n < 0 || r.off+n > len(r.p) {
		return nil, ErrShort
	}
	b := r.p[r.off : r.off+n]
	r.off += (n + 3) &^ 3
	if r.off > len(r.p) {
		r.off = len(r.p)
	}
	return b, nil
}

func (r *Reader) Int() (int32, error) {
	b, err := r.take(4)
	if err != nil {
		return 0, err
	}
	return int32(binary.LittleEndian.Uint32(b)), nil
}

func (r *Reader) Uint32() (uint32, error) {
	v, err := r.Int()
	return uint32(v), err
}

func (r *Reader) Int64() (int64, error) {
	b, err := r.take(8)
	if err != nil {
		return 0, err
	}
	return int64(binary.LittleEndian.Uint64(b)), nil
}

func (r *Reader) Uint64() (uint64, error) {
	v, err := r.Int64()
	return uint64(v), err
}

func (r *Reader) Bool() (bool, error) {
	v, err := r.Int()
	return v != 0, err
}

func (r *Reader) String() (string, error) {
	n, err := r.Int()
	if err != nil {
		return "", err
	}
	b, err := r.take(int(n))
	return string(b), err
}

func (r *Reader) String16() (string, error) {
	n, err := r.Int()
	if err != nil {
		return "", err
	}
	b, err := r.take(int(n) * 2)
	if err != nil {
		return "", err
	}
	u := make([]uint16, n)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return string(utf16.Decode(u)), nil
}
