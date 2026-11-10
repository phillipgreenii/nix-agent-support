package store

import (
	"errors"
	"testing"
	"time"
)

func shortCloseBounds(t *testing.T) {
	t.Helper()
	l, c, d := closeLockWait, closeCheckpointWait, closeDBsWait
	closeLockWait, closeCheckpointWait, closeDBsWait = 200*time.Millisecond, 300*time.Millisecond, 500*time.Millisecond
	t.Cleanup(func() { closeLockWait, closeCheckpointWait, closeDBsWait = l, c, d })
}

func closeWithin(t *testing.T, s *Store, d time.Duration) (error, time.Duration) {
	t.Helper()
	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	select {
	case err := <-done:
		return err, time.Since(start)
	case <-time.After(d):
		t.Fatalf("Store.Close still running after %v", d)
		return nil, 0
	}
}

// A reader that pins the WAL makes wal_checkpoint(TRUNCATE) wait out the (here
// long) busy_timeout; Close skips the truncate at its own deadline instead.
func TestCloseReturnsWhileAReaderPinsTheWAL(t *testing.T) {
	shortCloseBounds(t)
	old := busyTimeoutMS
	busyTimeoutMS = 30000
	t.Cleanup(func() { busyTimeoutMS = old })
	s, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.Read().Begin()
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM meta`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	// A commit after the reader's snapshot: the checkpoint cannot finish past it.
	if _, err := s.wdb.Exec(`INSERT OR REPLACE INTO meta(key,value) VALUES('close_test','1')`); err != nil {
		t.Fatal(err)
	}
	// Measured: with the reader still open, closing the connections (the
	// implicit last-connection checkpoint) did not wait out busy_timeout either;
	// closeDBs stays bounded regardless.
	_, took := closeWithin(t, s, 10*time.Second)
	if took > 5*time.Second {
		t.Fatalf("Close took %v with a reader pinning the WAL", took)
	}
	_ = tx.Rollback()
}

// A write that holds s.mu (the store's single write lock) cannot hold Close
// past its bound; Close reports it and leaves the state lock to process exit,
// and a later Close, once the writer is done, completes.
func TestCloseReturnsWhileTheWriteLockIsHeld(t *testing.T) {
	shortCloseBounds(t)
	s, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	err, took := closeWithin(t, s, 10*time.Second)
	if !errors.Is(err, errCloseBusy) {
		t.Fatalf("Close = %v, want errCloseBusy", err)
	}
	if took > 3*time.Second {
		t.Fatalf("Close took %v", took)
	}
	if s.dirLock == nil {
		t.Fatal("the state lock was released under a held write lock")
	}
	s.mu.Unlock()
	if err := s.Close(); err != nil {
		t.Fatalf("Close after the writer finished: %v", err)
	}
}
