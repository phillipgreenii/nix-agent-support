package session

import (
	"testing"

	"github.com/phillipgreenii/ccpool/internal/store"
)

func stateCount(t *testing.T, state store.State, live bool, rows []liveCount) int64 {
	t.Helper()
	for _, c := range rows {
		if c.state == string(state) && c.live == live {
			return c.count
		}
	}
	t.Fatalf("bucket %s live=%v not emitted", state, live)
	return -1
}

type liveCount struct {
	state string
	live  bool
	count int64
}

// TestSessionStateCounts_excludesEveryClosedDeadRow: a dead row that was closed
// since its last activity leaves its state bucket whatever the state, while a
// dead row nobody closed (or one resumed after the close) stays counted.
func TestSessionStateCounts_excludesEveryClosedDeadRow(t *testing.T) {
	dead := []store.Session{
		{ExternalID: "closed-working", State: store.Working, LastActivityAt: 100, ClosedAt: 100},
		{ExternalID: "unclosed-working", State: store.Working, LastActivityAt: 100},
		{ExternalID: "resumed-working", State: store.Working, LastActivityAt: 200, ClosedAt: 100},
		{ExternalID: "closed-needs", State: store.NeedsInput, LastActivityAt: 100, ClosedAt: 150},
		{ExternalID: "unclosed-needs", State: store.NeedsInput, LastActivityAt: 100},
	}
	live := []store.Session{{ExternalID: "l", State: store.Working, LastActivityAt: 100, ClosedAt: 50}}
	var rows []liveCount
	for _, c := range sessionStateCounts(live, dead) {
		rows = append(rows, liveCount{c.State, c.Live, c.Count})
	}
	if got := stateCount(t, store.Working, false, rows); got != 2 {
		t.Errorf("working/dead = %d, want 2 (unclosed + resumed)", got)
	}
	if got := stateCount(t, store.NeedsInput, false, rows); got != 1 {
		t.Errorf("needs_input/dead = %d, want 1", got)
	}
	if got := stateCount(t, store.Working, true, rows); got != 1 {
		t.Errorf("working/live = %d, want 1", got)
	}
}

// TestSessionRecordCounts_kindsPartitionRows: live, dead_closed and
// dead_unclosed sum to the row count, and empty kinds are emitted as zeros.
func TestSessionRecordCounts_kindsPartitionRows(t *testing.T) {
	live := []store.Session{{ExternalID: "a"}, {ExternalID: "b"}}
	dead := []store.Session{
		{ExternalID: "c", State: store.Working, LastActivityAt: 100, ClosedAt: 100},
		{ExternalID: "d", State: store.Ready, LastActivityAt: 100, ClosedAt: 300},
		{ExternalID: "e", State: store.Working, LastActivityAt: 100},
	}
	got := map[string]int64{}
	var sum int64
	for _, c := range sessionRecordCounts(live, dead) {
		got[c.Kind] = c.Count
		sum += c.Count
	}
	want := map[string]int64{"live": 2, "dead_closed": 2, "dead_unclosed": 1}
	if len(got) != 3 {
		t.Fatalf("kinds = %v, want exactly live, dead_closed, dead_unclosed", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("kind %s = %d, want %d", k, got[k], v)
		}
	}
	if sum != int64(len(live)+len(dead)) {
		t.Errorf("kinds sum to %d, want %d rows", sum, len(live)+len(dead))
	}
	for _, c := range sessionRecordCounts(nil, nil) {
		if c.Count != 0 {
			t.Errorf("empty registry: kind %s = %d, want 0", c.Kind, c.Count)
		}
	}
}
