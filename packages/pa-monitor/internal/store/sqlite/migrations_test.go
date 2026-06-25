package sqlite

import (
	"context"
	"testing"
)

func TestMigrate_FreshDB_CreatesAllTables(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	want := []string{
		"sessions", "blocks", "weeks",
		"session_block_contributions", "session_week_contributions",
		"system_toggles", "nudge_history", "nudge_history_sources",
		"schema_migrations",
	}
	for _, table := range want {
		var n int
		err := db.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&n)
		if err != nil {
			t.Errorf("query for %s: %v", table, err)
			continue
		}
		if n != 1 {
			t.Errorf("table %s: count=%d, want 1", table, n)
		}
	}

	var version int
	if err := db.QueryRowContext(context.Background(),
		"SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatalf("max version: %v", err)
	}
	if version != 2 {
		t.Errorf("schema_migrations max version = %d, want 2", version)
	}

	// Migration 002 adds last_error_from_subagent to sessions.
	var hasCol int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name='last_error_from_subagent'").Scan(&hasCol); err != nil {
		t.Fatalf("pragma_table_info: %v", err)
	}
	if hasCol != 1 {
		t.Errorf("sessions.last_error_from_subagent present = %d, want 1", hasCol)
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate first: %v", err)
	}
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate second: %v", err)
	}
	var count int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("schema_migrations rows = %d, want 2 (idempotent)", count)
	}
}
