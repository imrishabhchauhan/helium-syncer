package history

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func dbWith(t *testing.T, version, lastCompatible string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "History")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(p))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE meta(key LONGVARCHAR NOT NULL UNIQUE PRIMARY KEY, value LONGVARCHAR)`,
		`INSERT INTO meta VALUES('version','` + version + `'),('last_compatible_version','` + lastCompatible + `')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// A newer History format opens when Chromium says code for a known one can
// still use it; otherwise it is refused with ErrUnknownSchema.
func TestOpenNewerSchemas(t *testing.T) {
	for _, tc := range []struct {
		version, last string
		ok            bool
	}{
		{"70", "16", true},  // the tested one
		{"72", "16", true},  // newer, and Chromium says older code still works
		{"72", "70", true},  // newer, compatible down to exactly ours
		{"72", "71", false}, // newer, and needs newer code than ours
		{"69", "16", false}, // older than anything we know
	} {
		db, err := Open(dbWith(t, tc.version, tc.last))
		if tc.ok {
			if err != nil {
				t.Errorf("schema %s (compatible down to %s): %v", tc.version, tc.last, err)
				continue
			}
			db.Close()
		} else if !errors.Is(err, ErrUnknownSchema) {
			t.Errorf("schema %s (compatible down to %s): want ErrUnknownSchema, got %v", tc.version, tc.last, err)
		}
	}
}
