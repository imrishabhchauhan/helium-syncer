// Package history reads and writes Helium's <profile>/History SQLite
// database (components/history/core/browser, Chromium 155, schema 70).
//
// Only open it while Helium is NOT running on that profile.
package history

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"

	_ "modernc.org/sqlite"
)

// SupportedVersions are the History schema versions this code was verified
// against. Anything else is refused (version gate).
var SupportedVersions = map[int]bool{70: true}

// WebKit epoch: microseconds since 1601-01-01 UTC.
const unixToWebKitMicros = 11644473600 * 1_000_000

// Visit is one page visit.
type Visit struct {
	URL        string
	Title      string
	VisitTime  int64 // microseconds since 1601-01-01 UTC
	Transition int64
	Duration   int64 // microseconds
	// Where the visit originally happened. Empty for visits made on this
	// device; set for visits written by Helium Syncer from another device.
	OriginDevice  string
	OriginVisitID int64
	LocalID       int64 // visits.id on this device (read only)
}

// UnixMillis converts VisitTime to JavaScript time.
func (v Visit) UnixMillis() int64 { return (v.VisitTime - unixToWebKitMicros) / 1000 }

// FromUnixMillis converts JavaScript time to WebKit microseconds.
func FromUnixMillis(ms int64) int64 { return ms*1000 + unixToWebKitMicros }

// DB is an open History database.
type DB struct{ db *sql.DB }

// Open opens path for reading and writing.
func Open(path string) (*DB, error) {
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	h := &DB{db: db}
	v, err := h.Version()
	if err != nil {
		db.Close()
		return nil, err
	}
	if !SupportedVersions[v] {
		db.Close()
		return nil, fmt.Errorf("history: schema version %d is not supported yet", v)
	}
	return h, nil
}

// Close closes the database.
func (h *DB) Close() error { return h.db.Close() }

// Version returns meta.version.
func (h *DB) Version() (int, error) {
	var s string
	if err := h.db.QueryRow(`SELECT value FROM meta WHERE key='version'`).Scan(&s); err != nil {
		return 0, fmt.Errorf("history: read version: %w", err)
	}
	return strconv.Atoi(s)
}

// Visits returns all visits with visit_time >= since, oldest first.
func (h *DB) Visits(since int64) ([]Visit, error) {
	rows, err := h.db.Query(`
		SELECT v.id, u.url, COALESCE(u.title,''), v.visit_time, v.transition, v.visit_duration,
		       COALESCE(v.originator_cache_guid,''), COALESCE(v.originator_visit_id,0)
		FROM visits v JOIN urls u ON u.id = v.url
		WHERE v.visit_time >= ? ORDER BY v.visit_time`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Visit
	for rows.Next() {
		var v Visit
		if err := rows.Scan(&v.LocalID, &v.URL, &v.Title, &v.VisitTime, &v.Transition, &v.Duration, &v.OriginDevice, &v.OriginVisitID); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Transition core types that Chromium does not count in urls.visit_count.
func counted(transition int64) bool {
	switch transition & 0xFF {
	case 3, 4, 10: // AUTO_SUBFRAME, MANUAL_SUBFRAME, KEYWORD_GENERATED
		return false
	}
	return true
}

func typed(transition int64) bool { return transition&0xFF == 1 }

// Chromium only lists visits that end a navigation chain
// (VisitDatabase::GetVisibleVisitsInRange). A visit read from another
// Helium always has these bits; normalise defensively so a synced visit can
// never become invisible.
const chainStartEnd = 0x10000000 | 0x20000000

func normalise(transition int64) int64 {
	if transition&chainStartEnd == 0 {
		return transition | chainStartEnd
	}
	return transition
}

// AddVisits inserts visits from another device in one transaction. A visit
// already present (same URL and visit_time) is skipped, so applying the same
// batch twice is harmless. It returns how many visits were added.
func (h *DB) AddVisits(vs []Visit) (int, error) {
	tx, err := h.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	added := 0
	for _, v := range vs {
		if v.URL == "" || v.VisitTime <= 0 {
			return 0, errors.New("history: visit needs URL and time")
		}
		v.Transition = normalise(v.Transition)
		var urlID, lastVisit int64
		var title string
		err := tx.QueryRow(`SELECT id, last_visit_time, COALESCE(title,'') FROM urls WHERE url = ?`, v.URL).Scan(&urlID, &lastVisit, &title)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			res, err := tx.Exec(`INSERT INTO urls(url, title, visit_count, typed_count, last_visit_time, hidden) VALUES(?,?,0,0,?,0)`,
				v.URL, v.Title, v.VisitTime)
			if err != nil {
				return 0, err
			}
			urlID, _ = res.LastInsertId()
			lastVisit = v.VisitTime
		case err != nil:
			return 0, err
		}
		var exists int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM visits WHERE url = ? AND visit_time = ?`, urlID, v.VisitTime).Scan(&exists); err != nil {
			return 0, err
		}
		if exists > 0 {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO visits(url, visit_time, from_visit, transition, segment_id, visit_duration,
				originator_cache_guid, originator_visit_id) VALUES(?,?,0,?,0,?,?,?)`,
			urlID, v.VisitTime, v.Transition, v.Duration, nullable(v.OriginDevice), v.OriginVisitID); err != nil {
			return 0, err
		}
		inc, typ := 0, 0
		if counted(v.Transition) {
			inc = 1
		}
		if typed(v.Transition) {
			typ = 1
		}
		newTitle := title
		if v.VisitTime >= lastVisit && v.Title != "" {
			newTitle = v.Title
		}
		if _, err := tx.Exec(`UPDATE urls SET visit_count = visit_count + ?, typed_count = typed_count + ?,
				last_visit_time = MAX(last_visit_time, ?), title = ? WHERE id = ?`,
			inc, typ, v.VisitTime, newTitle, urlID); err != nil {
			return 0, err
		}
		added++
	}
	return added, tx.Commit()
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
