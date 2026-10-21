package merge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/imrishabhchauhan/helium-syncer/internal/crdt"
	"github.com/imrishabhchauhan/helium-syncer/internal/history"
)

func emptyHistory() History {
	return History{Visits: map[string]json.RawMessage{}, Titles: map[string]json.RawMessage{}}
}

// How long a computer takes to write a big history it gets for the first
// time (about the size of a real first sync), and the next sync after it.
// Run: HELIUM_SYNCER_BENCH=1 go test -run TestBigHistoryTime -v ./internal/merge/
func TestBigHistoryTime(t *testing.T) {
	if os.Getenv("HELIUM_SYNCER_BENCH") == "" {
		t.Skip("set HELIUM_SYNCER_BENCH=1")
	}
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "dst")
	for _, p := range []string{src, dst} {
		if err := history.CreateEmptyForTests(p); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UnixMicro() + 11644473600*1_000_000
	var vs []history.Visit
	for i := 0; i < 25000; i++ {
		vs = append(vs, history.Visit{URL: fmt.Sprintf("https://site%d.example/page/%d", i%3000, i), Title: fmt.Sprintf("Page %d", i), VisitTime: now - int64(i)*60_000_000, Transition: 0x30000000 | 1})
	}
	sdb, _ := history.Open(src)
	if _, err := sdb.AddVisits(vs); err != nil {
		t.Fatal(err)
	}
	clock := crdt.NewClock("mac", nil)
	sv, st := crdt.NewMap(), crdt.NewMap()
	_, ops, err := SyncHistory(sdb, emptyHistory(), sv, st, clock, nil, HistoryCutoff(now))
	sdb.Close()
	if err != nil {
		t.Fatal(err)
	}
	ddb, _ := history.Open(dst)
	defer ddb.Close()
	dv, dt := crdt.NewMap(), crdt.NewMap()
	start := time.Now()
	base, _, err := SyncHistory(ddb, emptyHistory(), dv, dt, crdt.NewClock("win", nil), ops, HistoryCutoff(now))
	if err != nil {
		t.Fatal(err)
	}
	first := time.Since(start)
	start = time.Now()
	if _, _, err := SyncHistory(ddb, base, dv, dt, crdt.NewClock("win", nil), nil, HistoryCutoff(now)); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d ops: first write %v, next sync %v", len(ops), first, time.Since(start))
}
