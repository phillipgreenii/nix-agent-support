package store

import (
	"path/filepath"
	"testing"
)

func TestMigrateSetsUserVersionAndIsIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "m.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var v int
	if err := db.sql.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if v != schemaVersion {
		t.Fatalf("user_version = %d, want %d", v, schemaVersion)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	for _, table := range []string{"pull_request", "feedback", "code_comment_message", "outbox"} {
		var name string
		err := db.sql.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name)
		if err != nil {
			t.Fatalf("table %s missing: %v", table, err)
		}
	}
}

func TestMigrateRefusesNewerSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "newer.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := db.sql.Exec("PRAGMA user_version = 9999"); err != nil {
		t.Fatalf("bump version: %v", err)
	}
	if err := migrate(db); err == nil {
		t.Fatal("expected error migrating a newer schema, got nil")
	}
	_ = db.Close()
}
