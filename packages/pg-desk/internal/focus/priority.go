package focus

import "strings"

// Priority ranks. A smaller number is a higher priority. P0..P4 are 0..4;
// a tracker value that maps to none of them sorts after P4.
const (
	priorityDefaultPR = 2 // a PR with no priority anywhere in its group is P2
	priorityUnmapped  = 5 // after P4
)

// priorityTable is the tracker-value lookup of the priority key: the
// configured focus.priority_map matched case-insensitively.
type priorityTable map[string]int

// newPriorityTable builds the lookup from a FocusPriorityMap result. An entry
// whose value is not P0..P4 cannot occur for a loaded Config (the load
// rejects it) and is skipped.
func newPriorityTable(m map[string]string) priorityTable {
	t := priorityTable{}
	for k, v := range m {
		if n, ok := directPriority(v); ok {
			t[strings.ToLower(strings.TrimSpace(k))] = n
		}
	}
	return t
}

// directPriority reads "P0".."P4" (any case) as 0..4.
func directPriority(raw string) (int, bool) {
	s := strings.TrimSpace(raw)
	if len(s) != 2 || (s[0] != 'P' && s[0] != 'p') || s[1] < '0' || s[1] > '4' {
		return 0, false
	}
	return int(s[1] - '0'), true
}

// resolve reads one stored priority value. present is false for an empty
// value (the item has no priority of its own); otherwise rank is 0..4, or
// priorityUnmapped with unmapped true when neither the table nor the direct
// P0..P4 form knows the value. The table is read first, so an explicit
// mapping wins.
func (t priorityTable) resolve(raw string) (rank int, present, unmapped bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false, false
	}
	if n, ok := t[strings.ToLower(s)]; ok {
		return n, true, false
	}
	if n, ok := directPriority(s); ok {
		return n, true, false
	}
	return priorityUnmapped, true, true
}

// priorityLabel renders a rank for display: P0..P4, or "" for none.
func priorityLabel(rank int, known bool) string {
	if !known || rank < 0 || rank > 4 {
		return ""
	}
	return "P" + string(rune('0'+rank))
}
