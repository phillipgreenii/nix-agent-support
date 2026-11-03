package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// slowQuery runs for far longer than any test waits: a recursive count that also reads a table, so it holds a read snapshot.
const slowQuery = `WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < 4000000000) SELECT count(*) + (SELECT count(*) FROM meta) FROM c`

func checkpoint(t *testing.T, s *Store, mode string) (busy, log, done int) {
	t.Helper()
	if err := s.wdb.QueryRow(`PRAGMA wal_checkpoint(`+mode+`)`).Scan(&busy, &log, &done); err != nil {
		t.Fatal(err)
	}
	return busy, log, done
}

// A held-open slow read is cut at the deadline, and the WAL then resets.
func TestSlowReadIsCutAtTheDeadlineAndTheWALResets(t *testing.T) {
	s, err := Open(t.TempDir(), Options{ReadTimeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	// The test's own context only guards against a missing deadline, so a
	// failing run ends instead of hanging.
	guard, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	res := make(chan error, 1)
	go func() {
		var n int64
		res <- s.Read().QueryRowContext(guard, slowQuery).Scan(&n)
	}()

	// Write after the reader began until a checkpoint shows the reader pins
	// the log: frames written after its snapshot cannot be backfilled.
	pinned := false
	for i := 0; i < 200 && !pinned; i++ {
		if _, err := s.wdb.Exec(`INSERT OR REPLACE INTO meta(key,value) VALUES('probe', ?)`, i); err != nil {
			t.Fatal(err)
		}
		_, log, done := checkpoint(t, s, "PASSIVE")
		pinned = done < log
		if !pinned {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if !pinned {
		t.Fatal("the slow reader never pinned the WAL; the fixture proves nothing")
	}

	select {
	case err := <-res:
		if !errors.Is(err, context.DeadlineExceeded) || guard.Err() != nil {
			t.Fatalf("the read ended with %v (guard %v), want the store deadline", err, guard.Err())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the slow read was not cut by the store deadline")
	}

	busy, log, done := checkpoint(t, s, "TRUNCATE")
	if busy != 0 || log != done {
		t.Fatalf("checkpoint after the deadline: busy=%d log=%d done=%d, want busy 0 and done == log", busy, log, done)
	}
}

// A read transaction as a whole ends at the deadline, not only each statement.
func TestReadTransactionEndsAtTheDeadline(t *testing.T) {
	s, err := Open(t.TempDir(), Options{ReadTimeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	tx, err := s.Read().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if err := tx.QueryRow(`SELECT 1`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := tx.QueryRow(`SELECT 1`).Scan(&n); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a statement after the transaction deadline returned %v, want the deadline error", err)
	}
}

// Every connection the read pool opens carries query_only and busy_timeout.
func TestReopenedReadConnectionsKeepTheirPragmas(t *testing.T) {
	s, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	s.Read().SetMaxIdleConns(0) // every use opens a fresh connection
	for i := 0; i < 3; i++ {
		var qo, bt int
		if err := s.Read().QueryRow(`PRAGMA query_only`).Scan(&qo); err != nil {
			t.Fatal(err)
		}
		if err := s.Read().QueryRow(`PRAGMA busy_timeout`).Scan(&bt); err != nil {
			t.Fatal(err)
		}
		if qo != 1 || bt != 5000 {
			t.Fatalf("connection %d: query_only=%d busy_timeout=%d, want 1 and 5000", i, qo, bt)
		}
	}
	if _, err := s.Read().Exec(`INSERT INTO meta(key,value) VALUES('x','y')`); err == nil {
		t.Fatal("the read pool accepted a write")
	}
}
