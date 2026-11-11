package settle

import (
	"maps"
	"testing"
)

// Sessions "a", "b", "c" are the smallest set that lets one test hold a
// session in each of the cases (unchanged, entered, left) at once; fewer would
// hide a case that two branches handle differently.
func set(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

func TestLiveDiffSessions(t *testing.T) {
	tests := []struct {
		name       string
		live, seen map[string]bool
		want       map[string]bool
	}{
		{"both empty", set(), set(), set()},
		{"unchanged", set("a"), set("a"), set()},
		{"entered", set("a", "b"), set("a"), set("b")},
		{"left", set("a"), set("a", "b"), set("b")},
		{"entered and left", set("a", "c"), set("a", "b"), set("b", "c")},
		{"a false entry in live counts as not live", map[string]bool{"a": false}, set(), set()},
		{"a false entry in live does not hide a left session", map[string]bool{"a": false}, set("a"), set("a")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := liveDiffSessions(tc.live, tc.seen); !maps.Equal(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNotLiveSessions(t *testing.T) {
	if got := notLiveSessions(set(), set("a")); len(got) != 0 {
		t.Fatalf("empty set: got %v", got)
	}
	if got, want := notLiveSessions(set("a", "b"), set("a")), set("b"); !maps.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got, want := notLiveSessions(set("a"), set()), set("a"); !maps.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestUnsettledSessions(t *testing.T) {
	cache := map[string]int{"a": 1, "b": 2, "c": 3}
	if got, want := unsettledSessions(cache, set("b")), set("a", "c"); !maps.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := unsettledSessions(cache, set("a", "b", "c", "z")); len(got) != 0 {
		t.Fatalf("all settled: got %v", got)
	}
	if got := unsettledSessions(map[string]int{}, set()); len(got) != 0 {
		t.Fatalf("empty cache: got %v", got)
	}
}

// Counts 0 and 1 are the boundary of "any"; a count of 2 would test nothing
// more because the code only compares with zero.
func TestWakeReasons(t *testing.T) {
	tests := []struct {
		name                       string
		dirty, due                 int
		sigLive, liveDiff, flip    bool
		wantDirty, wantDue, wantPl bool
	}{
		{"nothing at all is a poll", 0, 0, false, false, false, false, false, true},
		{"one dirty session", 1, 0, false, false, false, true, false, false},
		{"live signal alone is dirty", 0, 0, true, false, false, true, false, false},
		{"one due item", 0, 1, false, false, false, false, true, false},
		{"live diff without a signal polls", 0, 0, false, true, false, false, false, true},
		{"live diff with a signal does not poll", 0, 0, true, true, false, true, false, false},
		{"flip polls even with other reasons", 1, 1, false, false, true, true, true, true},
		{"dirty and due without a diff do not poll", 1, 1, false, false, false, true, true, false},
		{"live diff with dirty but no signal polls", 1, 0, false, true, false, true, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := wakeReasons(tc.dirty, tc.due, tc.sigLive, tc.liveDiff, tc.flip)
			want := map[string]bool{"dirty": tc.wantDirty, "due": tc.wantDue, "poll": tc.wantPl}
			if !maps.Equal(got, want) {
				t.Fatalf("got %v, want %v", got, want)
			}
		})
	}
}
