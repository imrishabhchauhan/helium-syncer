package merge

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
)

func TestBetween(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	keys := []string{Between("", "")}
	for i := 0; i < 5000; i++ {
		j := r.Intn(len(keys) + 1)
		lo, hi := "", ""
		if j > 0 {
			lo = keys[j-1]
		}
		if j < len(keys) {
			hi = keys[j]
		}
		k := Between(lo, hi)
		if (lo != "" && !(lo < k)) || (hi != "" && !(k < hi)) {
			t.Fatalf("Between(%q,%q)=%q not in range", lo, hi, k)
		}
		keys = append(keys[:j], append([]string{k}, keys[j:]...)...)
	}
	if !sort.StringsAreSorted(keys) {
		t.Fatal("keys not sorted")
	}
	if len(keys[len(keys)-1]) > 40 {
		t.Logf("longest key %d chars", len(keys[len(keys)-1]))
	}
}

func TestAssignOrderKeepsUnmovedKeys(t *testing.T) {
	// "d" moved to the front, a new item inserted after "b".
	got := AssignOrder([]Positioned{{"d", "8"}, {"a", "2"}, {"b", "4"}, {"new", ""}, {"c", "6"}})
	if got[1] != "2" || got[2] != "4" || got[4] != "6" {
		t.Errorf("unmoved keys changed: %v", got)
	}
	if !sort.StringsAreSorted(got) {
		t.Errorf("result not sorted: %v", got)
	}
}

// Sorting by (key, id) must always give back the list as the user had it,
// including ties created independently on two devices.
func TestAssignOrderReproducesList(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	for round := 0; round < 2000; round++ {
		n := 1 + r.Intn(8)
		items := make([]Positioned, n)
		for i := range items {
			items[i].ID = fmt.Sprint("id", r.Intn(1000), "-", i)
			if r.Intn(3) > 0 {
				items[i].Known = string(digits[1+r.Intn(4)]) // few values -> many ties
			}
		}
		keys := AssignOrder(items)
		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
		}
		sort.Slice(idx, func(a, b int) bool { return less(keys[idx[a]], items[idx[a]].ID, keys[idx[b]], items[idx[b]].ID) })
		for i := range idx {
			if idx[i] != i {
				t.Fatalf("round %d: order not reproduced\nitems %v\nkeys %v", round, items, keys)
			}
		}
	}
}
