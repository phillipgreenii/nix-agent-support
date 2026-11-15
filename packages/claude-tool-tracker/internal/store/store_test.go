package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-sqlite/sqliteopen"
)

// The writer carries the whole standard pragma set of the shared sqliteopen
// helper, on a real SQLite file.
func TestOpen_EffectiveStandardPragmas(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "nested", "tracker.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	got, err := sqliteopen.Effective(context.Background(), db)
	if err != nil {
		t.Fatalf("Effective: %v", err)
	}
	want := sqliteopen.Pragmas{
		JournalMode:      "wal",
		Synchronous:      1, // NORMAL
		BusyTimeout:      5000,
		CacheSize:        -65536,
		MmapSize:         256 << 20,
		JournalSizeLimit: 64 << 20,
		TempStore:        2, // MEMORY
		ForeignKeys:      true,
	}
	if got != want {
		t.Errorf("effective pragmas = %+v, want %+v", got, want)
	}
}

// The read-only handle is query_only, carries the standard read pragmas and
// refuses writes.
func TestOpenReadOnly_EffectivePragmasAndRefusesWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tracker.db")
	w, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = w.Close()

	db, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer func() { _ = db.Close() }()

	got, err := sqliteopen.Effective(context.Background(), db)
	if err != nil {
		t.Fatalf("Effective: %v", err)
	}
	if !got.QueryOnly || got.BusyTimeout != 5000 || got.CacheSize != -65536 || got.TempStore != 2 {
		t.Errorf("read-only pragmas = %+v", got)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO cwd_repo(cwd, repo) VALUES ('/a','b')`); err == nil {
		t.Error("write through the read-only handle succeeded")
	}
}
