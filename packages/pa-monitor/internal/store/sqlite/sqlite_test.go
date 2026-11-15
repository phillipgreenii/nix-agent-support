package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-sqlite/sqliteopen"
)

func TestOpen_WALModeAndForeignKeys(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	var journal string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if journal != "wal" {
		t.Errorf("journal_mode = %q, want %q", journal, "wal")
	}

	var fk int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}
}

func TestOpen_BusyTimeout(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	var ms int
	if err := db.QueryRowContext(context.Background(), "PRAGMA busy_timeout").Scan(&ms); err != nil {
		t.Fatalf("busy_timeout: %v", err)
	}
	if ms < 1000 {
		t.Errorf("busy_timeout = %d ms, want >= 1000", ms)
	}
}

// A pa-monitor database opened the way the daemon opens it carries the whole
// standard pragma set of the shared sqliteopen helper, on a real SQLite file.
func TestOpen_EffectiveStandardPragmas(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "nested", "pragmas.db"))
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
		QueryOnly:        false,
	}
	if got != want {
		t.Errorf("effective pragmas = %+v, want %+v", got, want)
	}
}
