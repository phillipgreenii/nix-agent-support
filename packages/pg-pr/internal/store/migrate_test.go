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

func TestMigrate_V2EnrichmentColumns(t *testing.T) {
	db := OpenForTest(t)
	var v int
	if err := db.sql.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if v != schemaVersion || schemaVersion != 2 {
		t.Fatalf("user_version=%d schemaVersion=%d; want both 2", v, schemaVersion)
	}
	for _, col := range []string{"kind", "languages", "size", "urgency", "urgency_score", "urgency_reasons"} {
		var cnt int
		if err := db.sql.QueryRow(
			"SELECT COUNT(*) FROM pragma_table_info('pull_request') WHERE name=?", col).Scan(&cnt); err != nil {
			t.Fatalf("pragma_table_info %s: %v", col, err)
		}
		if cnt != 1 {
			t.Errorf("column %q missing from pull_request", col)
		}
	}
}
