// Package chromejson reads and writes JSON exactly like Chromium's
// base::JSONReader / base::JSONWriter (Chromium 155), so files Helium wrote
// can be re-created byte for byte.
package chromejson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Decode parses JSON keeping numbers as json.Number, so integers and doubles
// can be re-serialised exactly the way Chromium would.
func Decode(data []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// Clean returns a copy of v with empty dictionaries and lists nested inside
// it removed, like RemoveEmptyValueDictEntries in pref_hash_calculator.cc.
func Clean(v any) any { return clean(v) }

// clean removes empty dicts/lists nested inside dictionaries (recursively),
// mirroring RemoveEmptyValueDictEntries / RemoveEmptyValueListEntries.
func clean(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			x = clean(x)
			if isEmptyContainer(x) {
				continue
			}
			out[k] = x
		}
		return out
	case []any:
		out := make([]any, 0, len(t))
		for _, x := range t {
			x = clean(x)
			if isEmptyContainer(x) {
				continue
			}
			out = append(out, x)
		}
		return out
	}
	return v
}

func isEmptyContainer(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		return len(t) == 0
	case []any:
		return len(t) == 0
	}
	return false
}

func write(b *strings.Builder, v any) {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(t))
	case json.Number:
		b.WriteString(number(t))
	case string:
		writeString(b, t)
	case []any:
		b.WriteByte('[')
		for i, x := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			write(b, x)
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys) // base::DictValue is a sorted flat_map (byte order)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeString(b, k)
			b.WriteByte(':')
			write(b, t[k])
		}
		b.WriteByte('}')
	default:
		panic(fmt.Sprintf("prefhash: unexpected type %T", v))
	}
}

// number re-creates base::Value's int/double split: integer literals that fit
// in int32 stay ints; everything else is a double, written with a trailing
// ".0" when it has no fraction or exponent (base::JSONWriter behaviour).
func number(n json.Number) string {
	s := n.String()
	if !strings.ContainsAny(s, ".eE") {
		if i, err := strconv.ParseInt(s, 10, 64); err == nil && i >= math.MinInt32 && i <= math.MaxInt32 {
			return strconv.FormatInt(i, 10)
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return s
	}
	out := shortestDouble(f)
	if !strings.ContainsAny(out, ".eE") {
		out += ".0"
	}
	return out
}

// shortestDouble mirrors base::NumberToString(double) (double-conversion
// ToShortest, EcmaScript style): plain decimal for 1e-7 <= |f| < 1e21,
// otherwise exponent form like 1e+21 / 1.5e-8.
func shortestDouble(f float64) string {
	if f == 0 {
		return "0"
	}
	a := math.Abs(f)
	if a >= 1e-7 && a < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	s := strconv.FormatFloat(f, 'e', -1, 64) // e.g. 1e+21, 1.5e-08
	mant, exp, _ := strings.Cut(s, "e")
	sign := exp[0]
	digits := strings.TrimLeft(exp[1:], "0")
	if digits == "" {
		digits = "0"
	}
	return mant + "e" + string(sign) + digits
}

// writeString follows base::EscapeJSONString: escape quote, backslash and
// control characters, plus '<' and U+2028/U+2029 (as backslash-u + 4 upper
// hex digits). Escape sequences are built with %c so no raw escape text
// lives in this file.
func writeString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case 0x3C, 0x2028, 0x2029: // '<', LINE SEPARATOR, PARAGRAPH SEPARATOR
			fmt.Fprintf(b, `\%c%04X`, 'u', r)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\%c%04X`, 'u', r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

// Compact serialises v exactly like base::WriteJson, without the empty
// container cleaning. Chromium writes Preferences files this way, so
// Compact(Decode(file)) must equal the file bytes.
func Compact(v any) string {
	var b strings.Builder
	write(&b, v)
	return b.String()
}

// Lookup returns the value at a dotted path inside a decoded JSON document.
func Lookup(doc any, path string) any {
	cur := doc
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur, ok = m[part]
		if !ok {
			return nil
		}
	}
	return cur
}

// Set writes v at a dotted path inside doc, creating dictionaries as needed.
func Set(doc map[string]any, path string, v any) {
	parts := strings.Split(path, ".")
	cur := doc
	for _, part := range parts[:len(parts)-1] {
		next, _ := cur[part].(map[string]any)
		if next == nil {
			next = map[string]any{}
			cur[part] = next
		}
		cur = next
	}
	cur[parts[len(parts)-1]] = v
}

// Pretty serialises v like base::JSONWriter with OPTIONS_PRETTY_PRINT:
// 3-space indents, "key": value, inline lists "[ a, b ]", and eol as the
// line ending ("\r\n" for files written on Windows, "\n" elsewhere),
// including one trailing eol.
func Pretty(v any, eol string) string {
	var b strings.Builder
	pretty(&b, v, 0, eol)
	b.WriteString(eol)
	return b.String()
}

func pretty(b *strings.Builder, v any, depth int, eol string) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("{" + eol)
		for i, k := range keys {
			if i > 0 {
				b.WriteString("," + eol)
			}
			b.WriteString(strings.Repeat(" ", 3*(depth+1)))
			writeString(b, k)
			b.WriteString(": ")
			pretty(b, t[k], depth+1, eol)
		}
		if len(keys) > 0 {
			b.WriteString(eol)
		}
		b.WriteString(strings.Repeat(" ", 3*depth) + "}")
	case []any:
		b.WriteString("[ ")
		for i, x := range t {
			if i > 0 {
				b.WriteString(", ")
			}
			pretty(b, x, depth, eol)
		}
		b.WriteString(" ]")
	default:
		write(b, v)
	}
}

// EOL is the line ending Chromium uses for pretty-printed files on this OS.
func EOL(goos string) string {
	if goos == "windows" {
		return "\r\n"
	}
	return "\n"
}
