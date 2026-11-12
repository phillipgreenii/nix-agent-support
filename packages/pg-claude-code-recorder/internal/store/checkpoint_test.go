package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func commitFile(t *testing.T, s *Store) {
	t.Helper()
	id := s.NewFileID()
	if err := s.Commit(&Rec{T: TFile, At: 1, File: id, Source: "x", Mode: "append"}); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointReaderPin(t *testing.T) {
	s := open(t, t.TempDir())
	defer func() { _ = s.Close() }()
	commitFile(t, s)
	tx, err := s.rdb.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := tx.QueryRowContext(context.Background(), `SELECT count(*) FROM files`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	commitFile(t, s)
	commitFile(t, s)
	log, done, err := s.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	if done <= 0 || done >= log {
		t.Fatalf("pinned: log=%d checkpointed=%d, want 0 < checkpointed < log", log, done)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	log, done, err = s.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	if log == 0 {
		t.Fatal("WAL was reset: a passive checkpoint must not restart or truncate it")
	}
	if done != log {
		t.Fatalf("released: log=%d checkpointed=%d, want equal", log, done)
	}
}

func journalLimit(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	if err := db.QueryRowContext(context.Background(), `PRAGMA journal_size_limit`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestJournalSizeLimitInDSN(t *testing.T) {
	s := open(t, t.TempDir())
	defer func() { _ = s.Close() }()
	want := 1073741824
	if JournalSizeLimit != want {
		t.Fatalf("JournalSizeLimit = %d", JournalSizeLimit)
	}
	for name, db := range map[string]*sql.DB{"write": s.wdb, "checkpoint": s.cdb} {
		if got := journalLimit(t, db); got != want {
			t.Errorf("%s connection: journal_size_limit = %d, want %d", name, got, want)
		}
		// Drop the pooled connection so the next use opens a new one: the
		// pragma must come back from the DSN, not from a one-off Exec.
		db.SetMaxIdleConns(0)
		if got := journalLimit(t, db); got != want {
			t.Errorf("%s connection after reconnect: journal_size_limit = %d, want %d", name, got, want)
		}
	}
}

func TestCheckpointConcurrentWithClose(t *testing.T) {
	s := open(t, t.TempDir())
	commitFile(t, s)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_, _, _ = s.Checkpoint()
			}
		}()
	}
	_ = s.Close()
	wg.Wait()
	if _, _, err := s.Checkpoint(); err == nil {
		t.Fatal("Checkpoint after Close returned nil error")
	}
}

// A WAL that grew past journal_size_limit is truncated to the limit once a
// complete checkpoint lets the next writer restart it (the file does not
// shrink by itself while the WAL merely sits idle).
func TestWALFileShrinksToLimitAfterCompleteCheckpoint(t *testing.T) {
	const limit = 64 * 1024
	dir := t.TempDir()
	s, err := Open(dir, Options{JournalSizeLimit: limit})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.wdb.ExecContext(context.Background(), `PRAGMA wal_autocheckpoint=0`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		commitFile(t, s)
	}
	wal := filepath.Join(dir, DBName+"-wal")
	fi, err := os.Stat(wal)
	if err != nil || fi.Size() <= 4*limit {
		t.Fatalf("setup: WAL %v %v, want well over %d", fi, err, limit)
	}
	log, done, err := s.Checkpoint()
	if err != nil || done != log {
		t.Fatalf("checkpoint log=%d done=%d err=%v", log, done, err)
	}
	commitFile(t, s) // the next writer restarts the WAL and applies the limit
	fi, err = os.Stat(wal)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() > limit {
		t.Fatalf("WAL file is %d bytes after a complete checkpoint and a write, want <= %d", fi.Size(), limit)
	}
}
