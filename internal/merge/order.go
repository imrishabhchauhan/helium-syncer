package merge

// Fractional order keys: strings that sort between any two neighbours, so
// concurrent inserts into the same folder never need renumbering. Digits are
// base-62 in ASCII order; a key never ends in the lowest digit, so there is
// always room before it.

const digits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func digitIndex(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'A' && c <= 'Z':
		return int(c-'A') + 10
	case c >= 'a' && c <= 'z':
		return int(c-'a') + 36
	}
	return 0
}

// Between returns a key k with a < k < b. Empty a means "before everything",
// empty b means "after everything". It requires a < b when both are set.
func Between(a, b string) string {
	if b != "" {
		n := 0
		for n < len(b) {
			ca := byte('0')
			if n < len(a) {
				ca = a[n]
			}
			if ca != b[n] {
				break
			}
			n++
		}
		if n > 0 {
			rest := ""
			if n < len(a) {
				rest = a[n:]
			}
			return b[:n] + Between(rest, b[n:])
		}
	}
	da := 0
	if a != "" {
		da = digitIndex(a[0])
	}
	db := len(digits)
	if b != "" {
		db = digitIndex(b[0])
	}
	if db-da > 1 {
		return string(digits[(da+db)/2])
	}
	if b != "" && len(b) > 1 {
		return b[:1]
	}
	rest := ""
	if len(a) > 1 {
		rest = a[1:]
	}
	return string(digits[da]) + Between(rest, "")
}

// Positioned is one list entry for AssignOrder: its stable id and the order
// key it had before ("" if new or moved here from elsewhere).
type Positioned struct {
	ID    string
	Known string
}

// less is the order every device sorts by: key first, then id. Two devices
// can create the same key independently, so the id breaks ties.
func less(aKey, aID, bKey, bID string) bool {
	if aKey != bKey {
		return aKey < bKey
	}
	return aID < bID
}

// AssignOrder gives each position in a list an order key, so that sorting
// by (key, id) reproduces the list exactly. Entries whose known keys are
// still in order (the longest such run) keep them; the rest get new keys
// between their neighbours, so a reorder changes as few keys as possible.
func AssignOrder(items []Positioned) []string {
	n := len(items)
	out := make([]string, n)
	// Longest increasing subsequence by (key, id) over entries with a key.
	type cand struct{ prev, length int }
	best := make([]cand, n)
	bestEnd, bestLen := -1, 0
	for i := 0; i < n; i++ {
		best[i] = cand{-1, 0}
		if items[i].Known == "" {
			continue
		}
		best[i].length = 1
		for j := 0; j < i; j++ {
			if items[j].Known != "" && less(items[j].Known, items[j].ID, items[i].Known, items[i].ID) && best[j].length+1 > best[i].length {
				best[i] = cand{j, best[j].length + 1}
			}
		}
		if best[i].length > bestLen {
			bestEnd, bestLen = i, best[i].length
		}
	}
	keep := make([]bool, n)
	for i := bestEnd; i >= 0; i = best[i].prev {
		keep[i] = true
	}
	for i := 0; i < n; i++ {
		if keep[i] {
			out[i] = items[i].Known
		}
	}
	// Fill gaps between kept keys. A new key must sort strictly between its
	// neighbours; when the neighbours share a key (a tie broken by id) there
	// is no room, so the right neighbour is re-keyed too.
	for i := 0; i < n; {
		if out[i] != "" {
			i++
			continue
		}
		lo := ""
		if i > 0 {
			lo = out[i-1]
		}
		j := i
		for {
			for j < n && out[j] == "" {
				j++
			}
			if j < n && lo != "" && out[j] <= lo {
				out[j] = "" // no room: re-key this one as well
				continue
			}
			break
		}
		hi := ""
		if j < n {
			hi = out[j]
		}
		for k := i; k < j; k++ {
			out[k] = Between(lo, hi)
			lo = out[k]
		}
		i = j
	}
	return out
}
