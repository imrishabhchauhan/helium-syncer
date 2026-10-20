package crdt

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
)

func TestClockMonotonicEvenWhenWallGoesBack(t *testing.T) {
	wall := int64(1000)
	c := NewClock("a", func() int64 { return wall })
	s1 := c.Next()
	wall = 500 // clock jumped backwards
	s2 := c.Next()
	if !s1.Less(s2) {
		t.Fatalf("stamps must keep increasing: %v then %v", s1, s2)
	}
	c.Observe(Stamp{Wall: 9000, Counter: 3, Device: "b"})
	if s3 := c.Next(); !(Stamp{Wall: 9000, Counter: 3, Device: "b"}).Less(s3) {
		t.Fatalf("after observing a remote stamp, next must be later: %v", s3)
	}
}

// Random ops delivered in any order, with duplicates, give the same map.
func TestMapConvergesInAnyOrder(t *testing.T) {
	r := rand.New(rand.NewSource(42))
	for round := 0; round < 300; round++ {
		clocks := []*Clock{NewClock("mac", nil), NewClock("win", nil), NewClock("pc2", nil)}
		var ops []Op
		for i := 0; i < 40; i++ {
			c := clocks[r.Intn(len(clocks))]
			op := Op{Type: "x", Key: fmt.Sprint("k", r.Intn(6)), Stamp: c.Next()}
			if r.Intn(4) == 0 {
				op.Deleted = true
			} else {
				op.Value = json.RawMessage(fmt.Sprintf(`{"n":%d}`, r.Intn(100)))
			}
			ops = append(ops, op)
		}
		want := NewMap()
		for _, op := range ops {
			want.Apply(op)
		}
		for trial := 0; trial < 5; trial++ {
			got := NewMap()
			shuffled := append([]Op(nil), ops...)
			shuffled = append(shuffled, ops[:r.Intn(len(ops))]...) // duplicates
			r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
			for _, op := range shuffled {
				got.Apply(op)
			}
			if !reflect.DeepEqual(got.Entries, want.Entries) {
				t.Fatalf("round %d: maps diverged", round)
			}
		}
	}
}

func TestDiff(t *testing.T) {
	c := NewClock("a", nil)
	base := map[string]json.RawMessage{"keep": []byte(`1`), "edit": []byte(`1`), "gone": []byte(`1`)}
	cur := map[string]json.RawMessage{"keep": []byte(`1`), "edit": []byte(`2`), "new": []byte(`1`)}
	ops := Diff("x", base, cur, c)
	got := map[string]string{}
	for _, op := range ops {
		if op.Deleted {
			got[op.Key] = "delete"
		} else {
			got[op.Key] = "set " + string(op.Value)
		}
	}
	want := map[string]string{"edit": "set 2", "gone": "delete", "new": "set 1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
