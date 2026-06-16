package store

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/clock"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(":memory:", &clock.Fake{T: time.Unix(1000, 0).UTC()})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestOpen_migratesSessionsTable(t *testing.T) {
	st := newTestStore(t)
	var n int
	err := st.db.QueryRowContext(context.Background(),
		"SELECT count(*) FROM sqlite_master WHERE type='table' AND name='sessions'").Scan(&n)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 1 {
		t.Fatalf("sessions table count = %d, want 1", n)
	}
}

// TestState_idleErroredReplaceDoneFailed pins the ADR 0015 state vocabulary: the
// settled turn-end states are `idle` (Claude Stop) and `errored` (Claude
// StopFailure) — there is no `done`/`failed` and no Terminal() concept.
func TestState_idleErroredReplaceDoneFailed(t *testing.T) {
	if Idle != "idle" {
		t.Errorf("Idle = %q, want \"idle\"", Idle)
	}
	if Errored != "errored" {
		t.Errorf("Errored = %q, want \"errored\"", Errored)
	}
}
