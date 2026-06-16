package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/eventlog"
)

// A Transition must be recorded to the wired event log, using the store's
// injected clock so the timestamp is deterministic under the fake clock.
func TestTransition_logsToEventLog(t *testing.T) {
	ctx := context.Background()
	events := filepath.Join(t.TempDir(), "events.jsonl")
	el, err := eventlog.Open(events)
	if err != nil {
		t.Fatalf("eventlog.Open: %v", err)
	}
	// Fixed clock at Unix 1000 (UTC) so the logged ts is predictable.
	st, err := Open(":memory:", &clock.Fake{T: time.Unix(1000, 0).UTC()}, WithEventLog(el))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	mustInsert(t, st, "a", "csid-a") // Starting, generation 1
	if _, err := st.Transition(ctx, "a", Ready, "csid-a", "/p/a.jsonl"); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	evs, err := eventlog.Read(events)
	if err != nil {
		t.Fatalf("eventlog.Read: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("logged %d events, want 1: %+v", len(evs), evs)
	}
	e := evs[0]
	if e.Kind != "transition" || e.Name != "a" ||
		e.From != string(Starting) || e.To != string(Ready) ||
		e.UUID != "csid-a" || e.LineRef != "/p/a.jsonl" {
		t.Errorf("logged event = %+v", e)
	}
	if e.Ts != time.Unix(1000, 0).UTC().Format(time.RFC3339Nano) {
		t.Errorf("logged ts = %q, want store clock's now", e.Ts)
	}
}

// With no event log wired, Transition still succeeds (nil-safe optional).
func TestTransition_noEventLog_isNoOp(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	mustInsert(t, st, "a", "csid-a")
	if _, err := st.Transition(ctx, "a", Ready, "csid-a", ""); err != nil {
		t.Fatalf("Transition without event log: %v", err)
	}
}
