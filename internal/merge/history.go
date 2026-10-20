package merge

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/imrishabhchauhan/helium-syncer/internal/crdt"
	"github.com/imrishabhchauhan/helium-syncer/internal/history"
)

// Op types for history.
const (
	TypeVisit = "visit" // key "<visit_time>|<url>"
	TypeTitle = "title" // key "<url>", value the page title
)

// HistoryWindowDays: only visits newer than this are synced. Helium expires
// history after 90 days; staying inside that window means expiry is never
// mistaken for a deletion.
const HistoryWindowDays = 85

// Visit is the synced form of one visit. Titles are synced per URL (TypeTitle)
// so a title change does not touch every old visit.
type Visit struct {
	Transition int64 `json:"tr"`
	Duration   int64 `json:"du,omitempty"`
}

// History is a device's synced history snapshot.
type History struct {
	Visits map[string]json.RawMessage `json:"visits"`
	Titles map[string]json.RawMessage `json:"titles"`
}

func visitKey(url string, at int64) string { return strconv.FormatInt(at, 10) + "|" + url }

func parseVisitKey(k string) (url string, at int64, err error) {
	ts, u, ok := strings.Cut(k, "|")
	if !ok {
		return "", 0, fmt.Errorf("merge: bad visit key %q", k)
	}
	at, err = strconv.ParseInt(ts, 10, 64)
	return u, at, err
}

// HistoryCutoff converts "now" (WebKit µs) into the window start.
func HistoryCutoff(nowWebKit int64) int64 {
	return nowWebKit - int64(HistoryWindowDays)*24*3600*1_000_000
}

// ExtractHistory reads visits and titles inside the window.
func ExtractHistory(db *history.DB, cutoff int64) (History, error) {
	h := History{Visits: map[string]json.RawMessage{}, Titles: map[string]json.RawMessage{}}
	vs, err := db.Visits(cutoff)
	if err != nil {
		return h, err
	}
	for _, v := range vs {
		raw, _ := json.Marshal(Visit{Transition: v.Transition, Duration: v.Duration})
		h.Visits[visitKey(v.URL, v.VisitTime)] = raw
	}
	titles, err := db.Titles(cutoff)
	if err != nil {
		return h, err
	}
	for u, t := range titles {
		if t != "" {
			raw, _ := json.Marshal(t)
			h.Titles[u] = raw
		}
	}
	return h, nil
}

func windowed(m map[string]json.RawMessage, cutoff int64) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		if _, at, err := parseVisitKey(k); err == nil && at >= cutoff {
			out[k] = v
		}
	}
	return out
}

// SyncHistory runs one sync step for one profile's history: local visits,
// deletions and title changes since base become ops; incoming ops are
// merged; then visits the device is missing are added, visits deleted
// elsewhere are removed and titles are updated.
func SyncHistory(db *history.DB, base History, visits, titles *crdt.Map, clock *crdt.Clock,
	incoming []crdt.Op, cutoff int64) (History, []crdt.Op, error) {
	cur, err := ExtractHistory(db, cutoff)
	if err != nil {
		return History{}, nil, err
	}
	out := crdt.Diff(TypeVisit, windowed(base.Visits, cutoff), cur.Visits, clock)
	// A title only disappears locally when its URL left the window or was
	// deleted; that is not a title deletion, so only sets travel.
	for _, op := range crdt.Diff(TypeTitle, base.Titles, cur.Titles, clock) {
		if !op.Deleted {
			out = append(out, op)
		}
	}
	for _, op := range incoming {
		clock.Observe(op.Stamp)
		switch op.Type {
		case TypeVisit:
			visits.Apply(op)
		case TypeTitle:
			titles.Apply(op)
		}
	}
	for _, op := range out {
		if op.Type == TypeVisit {
			visits.Apply(op)
		} else {
			titles.Apply(op)
		}
	}
	titleOf := func(u string) string {
		var t string
		if v, ok := titles.Get(u); ok {
			json.Unmarshal(v, &t)
		}
		return t
	}
	var add []history.Visit
	var del [][2]any
	for k, e := range visits.Entries {
		url, at, err := parseVisitKey(k)
		if err != nil || at < cutoff {
			continue
		}
		_, here := cur.Visits[k]
		switch {
		case e.Deleted && here:
			del = append(del, [2]any{url, at})
		case !e.Deleted && !here:
			var v Visit
			if json.Unmarshal(e.Value, &v) != nil {
				continue
			}
			add = append(add, history.Visit{URL: url, Title: titleOf(url), VisitTime: at, Transition: v.Transition,
				Duration: v.Duration, OriginDevice: e.Stamp.Device})
		}
	}
	if len(del) > 0 {
		if _, err := db.DeleteVisits(del); err != nil {
			return History{}, nil, err
		}
	}
	if len(add) > 0 {
		if _, err := db.AddVisits(add); err != nil {
			return History{}, nil, err
		}
	}
	fix := map[string]string{}
	for _, u := range titles.Live() {
		if t := titleOf(u); t != "" {
			var local string
			if raw, ok := cur.Titles[u]; ok {
				json.Unmarshal(raw, &local)
			}
			if local != t {
				fix[u] = t
			}
		}
	}
	if len(fix) > 0 {
		if err := db.SetTitles(fix); err != nil {
			return History{}, nil, err
		}
	}
	newBase, err := ExtractHistory(db, cutoff)
	if err != nil {
		return History{}, nil, err
	}
	return newBase, out, nil
}
