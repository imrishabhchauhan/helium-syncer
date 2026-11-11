package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeProfileFiles writes one of each file a sync may change.
func fakeProfileFiles(t *testing.T, userData, profile string) {
	t.Helper()
	dir := filepath.Join(userData, profile)
	if err := os.MkdirAll(filepath.Join(dir, "Sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	big := make([]byte, 200_000) // stands in for the history database
	for _, f := range []struct {
		name string
		body []byte
	}{
		{"Bookmarks", []byte(`{"roots":{}}`)},
		{"History", big},
		{"History-journal", big},
		{"Preferences", []byte(`{}`)},
		{"Secure Preferences", []byte(`{}`)},
	} {
		if err := os.WriteFile(filepath.Join(dir, f.name), f.body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(dir, "Sessions", "Session_1"), []byte("tabs"), 0o600)
	os.WriteFile(filepath.Join(userData, "Local State"), []byte(`{}`), 0o600)
}

func sizeOf(t *testing.T, dir string) int64 {
	t.Helper()
	var n int64
	filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			n += info.Size()
		}
		return nil
	})
	return n
}

// A kind the person switched off is never written, so copying it before a
// sync is a large file copied for nothing. On the owner's Mac, where
// history is off, the history database is 44 MB of a 45 MB copy, made on
// every sync.
func TestABackupSkipsWhatThisSyncCannotChange(t *testing.T) {
	userData, state := t.TempDir(), t.TempDir()
	fakeProfileFiles(t, userData, "Default")

	all, err := backup(state, userData, []string{"Default"}, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	withHistoryOff, err := backup(state, userData, []string{"Default"}, map[string]bool{KindHistory: true}, time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}

	whole, small := sizeOf(t, all), sizeOf(t, withHistoryOff)
	if small >= whole/2 {
		t.Errorf("with history off the copy is %d bytes against %d with it on; the big file is still being copied", small, whole)
	}
	for _, f := range []string{"History", "History-journal"} {
		if _, err := os.Stat(filepath.Join(withHistoryOff, "Default", f)); err == nil {
			t.Errorf("%s was copied even though history is off", f)
		}
	}
	// What is still on must still be copied, or a cut-off sync cannot be undone.
	for _, f := range []string{"Bookmarks", "Preferences", "Secure Preferences", filepath.Join("Sessions", "Session_1")} {
		if _, err := os.Stat(filepath.Join(withHistoryOff, "Default", f)); err != nil {
			t.Errorf("%s was not copied: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(withHistoryOff, "Local State")); err != nil {
		t.Errorf("Local State was not copied: %v", err)
	}
}

// Old copies must not pile up. The rule used to keep the newest twenty AND
// everything from the last thirty days, so a computer syncing every few
// minutes kept the lot: 157 folders and 2.1 GB on the owner's Mac.
func TestOldBackupsDoNotPileUp(t *testing.T) {
	state := t.TempDir()
	root := filepath.Join(state, "backups")
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	// Fifty copies, one every five minutes, all from today.
	for i := 0; i < 50; i++ {
		name := now.Add(-time.Duration(i) * 5 * time.Minute).Format("20060102-150405.000")
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// And one from last year, which must go whatever the count.
	old := now.AddDate(-1, 0, 0).Format("20060102-150405.000")
	os.MkdirAll(filepath.Join(root, old), 0o700)

	if err := pruneBackups(state, 20, 30*24*time.Hour, now); err != nil {
		t.Fatal(err)
	}
	left, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 20 {
		t.Errorf("%d copies left, want 20", len(left))
	}
	for _, e := range left {
		if e.Name() == old {
			t.Error("a copy from last year is still there")
		}
	}
}
