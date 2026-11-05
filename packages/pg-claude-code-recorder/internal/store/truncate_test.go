package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// growWAL commits n files with the automatic checkpoint off so the WAL file
// grows well past limit, and returns its path.
func growWAL(t *testing.T, s *Store, dir string, n int, limit int64) string {
	t.Helper()
	if _, err := s.wdb.Exec(`PRAGMA wal_autocheckpoint=0`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		commitFile(t, s)
	}
	wal := filepath.Join(dir, DBName+"-wal")
	fi, err := os.Stat(wal)
	if err != nil || fi.Size() <= 4*limit {
		t.Fatalf("setup: WAL %v %v, want well over %d", fi, err, limit)
	}
	return wal
}

func fileCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.wdb.QueryRow(`SELECT count(*) FROM files`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCheckpointTruncateShrinksTheWAL(t *testing.T) {
	const limit = 64 * 1024
	dir := t.TempDir()
	s, err := Open(dir, Options{JournalSizeLimit: limit})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wal := growWAL(t, s, dir, 200, limit)
	log, done, err := s.Checkpoint()
	if err != nil || log != done {
		t.Fatalf("passive checkpoint log=%d done=%d err=%v", log, done, err)
	}
	busy, err := s.CheckpointTruncate(time.Second)
	if err != nil || busy {
		t.Fatalf("truncate busy=%v err=%v", busy, err)
	}
	fi, err := os.Stat(wal)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() > limit {
		t.Fatalf("WAL is %d bytes after TRUNCATE, want <= %d", fi.Size(), limit)
	}
	if got := fileCount(t, s); got != 200 {
		t.Fatalf("files after TRUNCATE = %d, want 200", got)
	}
}

func TestCheckpointTruncateIsBusyWhileAReaderPinsTheWAL(t *testing.T) {
	const limit = 64 * 1024
	dir := t.TempDir()
	s, err := Open(dir, Options{JournalSizeLimit: limit})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	commitFile(t, s)
	tx, err := s.rdb.Begin()
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM files`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	wal := growWAL(t, s, dir, 200, limit)
	before, _ := os.Stat(wal)
	busy, err := s.CheckpointTruncate(100 * time.Millisecond)
	if err != nil || !busy {
		t.Fatalf("truncate with a reader: busy=%v err=%v, want busy", busy, err)
	}
	after, _ := os.Stat(wal)
	if after.Size() != before.Size() {
		t.Fatalf("busy attempt changed the WAL: %d -> %d", before.Size(), after.Size())
	}
	if got := fileCount(t, s); got != 201 {
		t.Fatalf("files after a busy TRUNCATE = %d, want 201 (nothing lost)", got)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	busy, err = s.CheckpointTruncate(time.Second)
	if err != nil || busy {
		t.Fatalf("truncate after the reader closed: busy=%v err=%v", busy, err)
	}
	if fi, _ := os.Stat(wal); fi.Size() > limit {
		t.Fatalf("WAL is %d bytes after the retry, want <= %d", fi.Size(), limit)
	}
	if got := fileCount(t, s); got != 201 {
		t.Fatalf("files after the retry = %d, want 201", got)
	}
	// The checkpoint connection keeps its normal busy_timeout.
	var ms int
	if err := s.cdb.QueryRow(`PRAGMA busy_timeout`).Scan(&ms); err != nil || ms != walBusyTimeoutMS {
		t.Fatalf("busy_timeout after TRUNCATE = %d err=%v", ms, err)
	}
}

// Negative control for the all-frames guard of the periodic TRUNCATE: a TRUNCATE
// attempted while a reader pins frames (a passive checkpoint would report
// log != done) holds the WAL write lock for its whole busy wait, so a writer
// stalls for about that long even though the TRUNCATE cannot finish. The daemon
// therefore attempts it only after a fresh passive checkpoint reached every
// frame, when no reader pins the WAL and the TRUNCATE returns at once.
func TestTruncateWithoutAllFramesStallsWriters(t *testing.T) {
	const wait = 400 * time.Millisecond
	s := open(t, t.TempDir())
	defer s.Close()
	commitFile(t, s)
	tx, err := s.rdb.Begin()
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM files`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	commitFile(t, s)
	commitFile(t, s)
	if log, done, err := s.Checkpoint(); err != nil || done >= log {
		t.Fatalf("setup: passive checkpoint log=%d done=%d err=%v, want partial", log, done, err)
	}
	truncDone := make(chan struct{})
	go func() {
		defer close(truncDone)
		_, _ = s.CheckpointTruncate(wait)
	}()
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	commitFile(t, s)
	stalled := time.Since(start)
	<-truncDone
	if stalled < wait/2 {
		t.Fatalf("writer took %v during an unguarded TRUNCATE, want >= %v (the stall the all-frames guard avoids)", stalled, wait/2)
	}
	_ = tx.Rollback()

	// With every frame checkpointed the same attempt does not delay a writer.
	if log, done, err := s.Checkpoint(); err != nil || done != log {
		t.Fatalf("passive checkpoint log=%d done=%d err=%v", log, done, err)
	}
	go func() { _, _ = s.CheckpointTruncate(wait) }()
	time.Sleep(10 * time.Millisecond)
	start = time.Now()
	commitFile(t, s)
	if d := time.Since(start); d >= wait/2 {
		t.Fatalf("writer took %v beside a guarded TRUNCATE, want < %v", d, wait/2)
	}
}
