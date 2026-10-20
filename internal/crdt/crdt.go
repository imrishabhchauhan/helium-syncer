// Package crdt is the conflict-free core of Helium Syncer.
//
// Every synced item lives in a last-writer-wins map: key -> (value, stamp,
// deleted). Stamps come from a hybrid logical clock, so they order events
// across devices even when clocks drift, and ties break on the device id.
// Merging is commutative, associative and idempotent: devices that have seen
// the same set of changes hold the same state, in any delivery order, with
// duplicates.
package crdt

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Stamp is a hybrid logical clock reading: wall milliseconds, a counter for
// events in the same millisecond, and the device that made the change.
type Stamp struct {
	Wall    int64  `json:"w"`
	Counter uint32 `json:"c"`
	Device  string `json:"d"`
}

// Less orders stamps totally.
func (a Stamp) Less(b Stamp) bool {
	if a.Wall != b.Wall {
		return a.Wall < b.Wall
	}
	if a.Counter != b.Counter {
		return a.Counter < b.Counter
	}
	return a.Device < b.Device
}

// IsZero reports an unset stamp.
func (a Stamp) IsZero() bool { return a.Wall == 0 && a.Counter == 0 && a.Device == "" }

func (a Stamp) String() string { return fmt.Sprintf("%d.%d@%s", a.Wall, a.Counter, a.Device) }

// Clock issues stamps for one device.
type Clock struct {
	mu     sync.Mutex
	device string
	last   Stamp
	now    func() int64
}

// NewClock creates a clock for device. now may be nil (wall time).
func NewClock(device string, now func() int64) *Clock {
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	return &Clock{device: device, now: now}
}

// Next returns a stamp later than every stamp issued or observed so far.
func (c *Clock) Next() Stamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	w := c.now()
	if w > c.last.Wall {
		c.last = Stamp{Wall: w, Device: c.device}
	} else {
		c.last = Stamp{Wall: c.last.Wall, Counter: c.last.Counter + 1, Device: c.device}
	}
	return c.last
}

// Observe moves the clock past a stamp received from another device.
func (c *Clock) Observe(s Stamp) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last.Less(s) {
		c.last = Stamp{Wall: s.Wall, Counter: s.Counter, Device: c.device}
	}
}

// Op is one change to one item.
type Op struct {
	Type    string          `json:"t"` // data type, e.g. "bookmark"
	Key     string          `json:"k"`
	Value   json.RawMessage `json:"v,omitempty"`
	Deleted bool            `json:"x,omitempty"`
	Stamp   Stamp           `json:"s"`
}

// Entry is the current state of one key.
type Entry struct {
	Value   json.RawMessage `json:"v,omitempty"`
	Deleted bool            `json:"x,omitempty"`
	Stamp   Stamp           `json:"s"`
}

// Map is a last-writer-wins map for one data type.
type Map struct {
	Entries map[string]Entry `json:"e"`
}

// NewMap returns an empty map.
func NewMap() *Map { return &Map{Entries: map[string]Entry{}} }

// Apply merges one op. It returns true if the op changed the map.
func (m *Map) Apply(op Op) bool {
	cur, ok := m.Entries[op.Key]
	if ok && !cur.Stamp.Less(op.Stamp) {
		return false // older or the same op: ignore (idempotent)
	}
	e := Entry{Deleted: op.Deleted, Stamp: op.Stamp}
	if !op.Deleted {
		e.Value = append(json.RawMessage(nil), op.Value...)
	}
	m.Entries[op.Key] = e
	return true
}

// Live returns the keys that are not deleted, sorted.
func (m *Map) Live() []string {
	var out []string
	for k, e := range m.Entries {
		if !e.Deleted {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Get returns the live value for key.
func (m *Map) Get(key string) (json.RawMessage, bool) {
	e, ok := m.Entries[key]
	if !ok || e.Deleted {
		return nil, false
	}
	return e.Value, true
}

// Clone deep-copies the map.
func (m *Map) Clone() *Map {
	out := NewMap()
	for k, e := range m.Entries {
		e.Value = append(json.RawMessage(nil), e.Value...)
		out.Entries[k] = e
	}
	return out
}

// Diff compares a device's last applied snapshot (base) with its current
// snapshot and returns ops for what changed locally: new or edited items and
// removed items. Values are compared as canonical JSON bytes.
func Diff(typ string, base, current map[string]json.RawMessage, clock *Clock) []Op {
	var keys []string
	for k := range current {
		keys = append(keys, k)
	}
	for k := range base {
		if _, ok := current[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var ops []Op
	for _, k := range keys {
		cv, inCur := current[k]
		bv, inBase := base[k]
		switch {
		case inCur && (!inBase || string(cv) != string(bv)):
			ops = append(ops, Op{Type: typ, Key: k, Value: cv, Stamp: clock.Next()})
		case !inCur && inBase:
			ops = append(ops, Op{Type: typ, Key: k, Deleted: true, Stamp: clock.Next()})
		}
	}
	return ops
}
