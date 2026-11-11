package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/imrishabhchauhan/helium-syncer/internal/crdt"
	"github.com/imrishabhchauhan/helium-syncer/internal/extensions"
)

// blobBox is a sync server that only holds blobs.
type blobBox struct {
	Transport
	blobs map[string][]byte
	puts  int
}

func (b *blobBox) PutBlob(_ context.Context, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	id := hex.EncodeToString(sum[:])
	if b.blobs == nil {
		b.blobs = map[string][]byte{}
	}
	b.blobs[id] = data
	b.puts++
	return id, nil
}

func (b *blobBox) GetBlob(_ context.Context, id string) ([]byte, error) { return b.blobs[id], nil }

const stampExtID = "eimadpbcbfnmbkopoojfekhnkhdbieeh"

// extProfile is a profile holding one Web Store extension with data.
func extProfile(t *testing.T) string {
	t.Helper()
	p := t.TempDir()
	ver := filepath.Join(p, "Extensions", stampExtID, "4.9.133_0")
	if err := os.MkdirAll(ver, 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(ver, "manifest.json"), []byte(`{"name":"Dark Reader","version":"4.9.133"}`), 0o600)
	data := filepath.Join(p, "Local Extension Settings", stampExtID)
	os.MkdirAll(data, 0o700)
	os.WriteFile(filepath.Join(data, "000003.log"), []byte("some saved settings"), 0o600)
	secure := `{"extensions":{"settings":{"` + stampExtID + `":{"location":1,"from_webstore":true,"path":"` + stampExtID + `/4.9.133_0",` +
		`"manifest":{"name":"Dark Reader","version":"4.9.133"},"disable_reasons":[]}}},"protection":{"macs":{}}}`
	os.WriteFile(filepath.Join(p, "Secure Preferences"), []byte(secure), 0o600)
	return p
}

// A sync that changes nothing must leave a mark saying "this is the data I
// already have", or the next sync reads and compresses all of it again.
// On the owner's Mac that was 165 MB every five minutes, on battery.
func TestASyncThatChangesNothingKeepsTheMark(t *testing.T) {
	dir := extProfile(t)
	box := &blobBox{}
	cfg := Config{Transport: box, MachineID: "TEST-MACHINE", GOOS: "darwin", UserData: filepath.Dir(dir)}
	ps := newProfileState()
	clock := crdt.NewClock("device-1", func() int64 { return 1 })
	j := &journal{}

	if _, err := syncExtensions(context.Background(), cfg, ps, dir, clock, nil, j); err != nil {
		t.Fatal(err)
	}
	if len(ps.DataStamps) == 0 {
		t.Fatal("the first sync left no mark, so every later sync reads and compresses the data again")
	}
	want := extensions.DataStamp(dir, stampExtID)
	if ps.DataStamps[stampExtID] != want {
		t.Errorf("the mark kept is %q, want %q", ps.DataStamps[stampExtID], want)
	}

	// The case that matters on a computer that has been syncing for a
	// while: the data was uploaded long ago by a version that kept no
	// mark. The first sync after the update must make one, or the data is
	// read and compressed on every sync for ever.
	ps.DataStamps = map[string]string{}
	if _, err := syncExtensions(context.Background(), cfg, ps, dir, clock, nil, j); err != nil {
		t.Fatal(err)
	}
	if ps.DataStamps[stampExtID] != want {
		t.Fatalf("after an update with the data already uploaded, the mark is %q, want %q",
			ps.DataStamps[stampExtID], want)
	}

	// A second sync with nothing changed must upload nothing more.
	puts := box.puts
	if _, err := syncExtensions(context.Background(), cfg, ps, dir, clock, nil, j); err != nil {
		t.Fatal(err)
	}
	if box.puts != puts {
		t.Errorf("a sync with nothing changed uploaded %d more time(s)", box.puts-puts)
	}

	// And a real change must still be noticed.
	os.WriteFile(filepath.Join(dir, "Local Extension Settings", stampExtID, "000004.log"), []byte("new"), 0o600)
	if _, err := syncExtensions(context.Background(), cfg, ps, dir, clock, nil, j); err != nil {
		t.Fatal(err)
	}
	if box.puts == puts {
		t.Error("the data changed and nothing was sent")
	}
	if ps.DataStamps[stampExtID] != extensions.DataStamp(dir, stampExtID) {
		t.Error("the mark was not brought up to date after the change")
	}
}
