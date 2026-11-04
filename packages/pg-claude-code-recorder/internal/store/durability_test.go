package store

import (
	"errors"
	"testing"
)

// readPragma reads one integer pragma from the write connection.
func readPragma(t *testing.T, s *Store, name string) int {
	t.Helper()
	conn, err := s.wdb.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	var v int
	if err := conn.QueryRowContext(t.Context(), `PRAGMA `+name).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestNormalRunningIsSynchronousNormalAndAFullCommitIsFullOnlyForItself(t *testing.T) {
	s, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if got := readPragma(t, s, "synchronous"); got != 1 {
		t.Fatalf("running synchronous = %d, want 1 (NORMAL)", got)
	}
	if err := s.CommitFull(&Rec{T: TEpoch, At: 1, Epoch: 9}); err != nil {
		t.Fatal(err)
	}
	if got := readPragma(t, s, "synchronous"); got != 1 {
		t.Fatalf("synchronous after a full commit = %d, want 1 (NORMAL)", got)
	}
	if got := readPragma(t, s, "fullfsync"); got != 0 {
		t.Fatalf("fullfsync after a full commit = %d, want 0", got)
	}
}

func TestBarrierCheckpointsTheWholeWALAndRestoresTheSettings(t *testing.T) {
	s, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for i := 0; i < 20; i++ {
		if err := s.Commit(&Rec{T: TEpoch, At: int64(i), Epoch: 2 + i}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Barrier(); err != nil {
		t.Fatal(err)
	}
	var busy, log, ckpt int
	if err := s.wdb.QueryRow(`PRAGMA wal_checkpoint(PASSIVE)`).Scan(&busy, &log, &ckpt); err != nil {
		t.Fatal(err)
	}
	if log != 0 {
		t.Fatalf("WAL holds %d frames after the barrier, want 0 (truncated)", log)
	}
	if got := readPragma(t, s, "synchronous"); got != 1 {
		t.Fatalf("synchronous after the barrier = %d, want 1", got)
	}
	if got := readPragma(t, s, "checkpoint_fullfsync"); got != 0 {
		t.Fatalf("checkpoint_fullfsync after the barrier = %d, want 0", got)
	}
}

func TestBarrierReportsABlockedCheckpoint(t *testing.T) {
	s, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	rd, err := s.Read().Begin()
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := rd.QueryRow(`SELECT COUNT(*) FROM meta`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(&Rec{T: TEpoch, At: 1, Epoch: 5}); err != nil {
		t.Fatal(err)
	}
	if err := s.Barrier(); !errors.Is(err, ErrBarrierBusy) {
		t.Fatalf("Barrier with a pinned reader = %v, want ErrBarrierBusy", err)
	}
	_ = rd.Rollback()
	if err := s.Barrier(); err != nil {
		t.Fatalf("Barrier after the reader left: %v", err)
	}
}
