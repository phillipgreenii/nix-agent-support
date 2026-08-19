package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// rawOpenDB opens a bare SQLite DB (no migration, no pragmas) so tests can
// construct arbitrary schemas and call applyMigration directly.
func rawOpenDB(t *testing.T) *DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "raw.db")
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(OFF)", dbPath)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("rawOpenDB sql.Open: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	db := &DB{sql: sqlDB}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestApplyMigration_FKViolationFails proves that applyMigration detects a
// dangling foreign-key reference introduced by the migration DDL and returns a
// descriptive error. Without the PRAGMA foreign_key_check added to
// applyMigration, this test fails (returns nil instead of an error).
func TestApplyMigration_FKViolationFails(t *testing.T) {
	db := rawOpenDB(t)

	// Build a minimal two-table schema: parent + child (FK → parent).
	// foreign_keys must be ON so foreign_key_check actually validates.
	setup := `
PRAGMA foreign_keys = ON;
CREATE TABLE parent (id INTEGER PRIMARY KEY, val TEXT);
CREATE TABLE child  (id INTEGER PRIMARY KEY, parent_id INTEGER NOT NULL REFERENCES parent(id));
INSERT INTO parent VALUES (1, 'keep');
INSERT INTO parent VALUES (2, 'delete-me');
INSERT INTO child  VALUES (10, 1);
INSERT INTO child  VALUES (20, 2);
`
	if _, err := db.sql.Exec(setup); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// This DDL deletes parent row 2 while child row 20 still references it.
	// With foreign_keys=OFF (set by applyMigration) SQLite allows the DELETE,
	// leaving a dangling reference. PRAGMA foreign_key_check should catch it.
	danglingDDL := `DELETE FROM parent WHERE id = 2;`

	err := applyMigration(db, 1, danglingDDL)
	if err == nil {
		t.Fatal("applyMigration should return an error when the migration leaves a dangling FK, but returned nil")
	}
	if !strings.Contains(err.Error(), "foreign key") && !strings.Contains(err.Error(), "foreign_key") {
		t.Fatalf("expected error to mention foreign key violation; got: %v", err)
	}
}

// TestApplyMigration_CleanMigrationPassesFKCheck confirms that a well-formed
// migration (no dangling FKs) still succeeds after the check is added.
func TestApplyMigration_CleanMigrationPassesFKCheck(t *testing.T) {
	db := rawOpenDB(t)

	// Build the same schema with consistent data.
	setup := `
PRAGMA foreign_keys = ON;
CREATE TABLE parent (id INTEGER PRIMARY KEY, val TEXT);
CREATE TABLE child  (id INTEGER PRIMARY KEY, parent_id INTEGER NOT NULL REFERENCES parent(id));
INSERT INTO parent VALUES (1, 'keep');
INSERT INTO child  VALUES (10, 1);
`
	if _, err := db.sql.Exec(setup); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// A migration that only adds a column — no FK violation possible.
	cleanDDL := `ALTER TABLE parent ADD COLUMN extra TEXT;`

	if err := applyMigration(db, 1, cleanDDL); err != nil {
		t.Fatalf("clean migration should succeed, got error: %v", err)
	}

	// Confirm the user_version was bumped.
	var v int
	if err := db.sql.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if v != 1 {
		t.Fatalf("user_version = %d, want 1", v)
	}
}
