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
