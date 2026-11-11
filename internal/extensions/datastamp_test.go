package extensions

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// write puts a file in an extension's data folder.
func write(t *testing.T, profile, id, name, body string) string {
	t.Helper()
	p := filepath.Join(profile, "Local Extension Settings", id, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The mark decides whether a sync reads and compresses the extension's
// data at all. On the owner's Mac that is 165 MB every five minutes, so
// the mark must stay the same when nothing changed.
func TestTheMarkIsTheSameWhenNothingChanged(t *testing.T) {
	profile := t.TempDir()
	id := "abcdefghijklmnopabcdefghijklmnop"
	write(t, profile, id, "000003.log", "hello")
	write(t, profile, id, "CURRENT", "MANIFEST-000002\n")

	first := DataStamp(profile, id)
	if first == "" {
		t.Fatal("no mark at all")
	}
	for i := 0; i < 5; i++ {
		if again := DataStamp(profile, id); again != first {
			t.Fatalf("run %d gave a different mark, so the data would be packed every time", i+2)
		}
	}
}

// And it must change for every kind of change, or a real change would
// never be sent. Each case has to fail without the fix it checks.
func TestTheMarkChangesForEveryKindOfChange(t *testing.T) {
	id := "abcdefghijklmnopabcdefghijklmnop"
	cases := []struct {
		what   string
		change func(t *testing.T, profile string)
	}{
		{"a file grew", func(t *testing.T, p string) { write(t, p, id, "000003.log", "hello there") }},
		{"a file changed but kept its size", func(t *testing.T, p string) {
			f := write(t, p, id, "000003.log", "HELLO")
			// A change a second later, the same length: only the time says so.
			later := time.Now().Add(2 * time.Second)
			if err := os.Chtimes(f, later, later); err != nil {
				t.Fatal(err)
			}
		}},
		{"a file was added", func(t *testing.T, p string) { write(t, p, id, "000004.log", "new") }},
		{"a file was removed", func(t *testing.T, p string) {
			os.Remove(filepath.Join(p, "Local Extension Settings", id, "CURRENT"))
		}},
	}
	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			profile := t.TempDir()
			write(t, profile, id, "000003.log", "hello")
			write(t, profile, id, "CURRENT", "MANIFEST-000002\n")
			before := DataStamp(profile, id)
			c.change(t, profile)
			if after := DataStamp(profile, id); after == before {
				t.Errorf("the mark did not change when %s, so the change would never be sent", c.what)
			}
		})
	}
}

// Two extensions must not share a mark, or one changing would hide the
// other.
func TestEachExtensionHasItsOwnMark(t *testing.T) {
	profile := t.TempDir()
	one := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	two := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	write(t, profile, one, "000003.log", "same body")
	write(t, profile, two, "000003.log", "same body")
	if DataStamp(profile, one) == DataStamp(profile, two) {
		t.Error("two extensions with the same file names share a mark")
	}
}

// An extension with no data at all still gets an answer, and the same one
// every time.
func TestNoDataStillGivesASteadyMark(t *testing.T) {
	profile := t.TempDir()
	id := "cccccccccccccccccccccccccccccccc"
	if a, b := DataStamp(profile, id), DataStamp(profile, id); a != b {
		t.Error("an extension with no data gave two different marks")
	}
}
