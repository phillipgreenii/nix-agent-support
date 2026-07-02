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
	if v != schemaVersion || schemaVersion < 2 {
		t.Fatalf("user_version=%d schemaVersion=%d; want both >= 2", v, schemaVersion)
	}
	for _, col := range []string{"kind", "languages", "size", "urgency", "urgency_score", "urgency_reasons"} {
		var cnt int
		if err := db.sql.QueryRow(
			"SELECT COUNT(*) FROM pragma_table_info('pull_request') WHERE name=?", col,
		).Scan(&cnt); err != nil {
			t.Fatalf("pragma_table_info %s: %v", col, err)
		}
		if cnt != 1 {
			t.Errorf("column %q missing from pull_request", col)
		}
	}
}

func TestMigrate_V3RevisionTable(t *testing.T) {
	db := OpenForTest(t)

	// Table exists.
	var name string
	if err := db.sql.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name='pr_revision'",
	).Scan(&name); err != nil {
		t.Fatalf("pr_revision table missing: %v", err)
	}

	// CHECK on ci_state is enforced.
	_, err := db.sql.Exec(`INSERT INTO pull_request
		(repo,number,ownership,state,head_sha,created_at,updated_at)
		VALUES ('o/r',1,'mine','open','sha1','t','t')`)
	if err != nil {
		t.Fatalf("seed pr: %v", err)
	}
	var prID int64
	_ = db.sql.QueryRow("SELECT id FROM pull_request WHERE repo='o/r' AND number=1").Scan(&prID)
	if _, err := db.sql.Exec(`INSERT INTO pr_revision
		(pr_id,seq,head_sha,observed_at,last_seen_at,ci_state)
		VALUES (?,?,?,?,?,?)`, prID, 1, "sha1", "t", "t", "bogus"); err == nil {
		t.Fatal("expected ci_state CHECK to reject 'bogus'")
	}

	// Idempotent re-migrate.
	if err := migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestMigrate_V3MyReviewStateCHECK(t *testing.T) {
	db := OpenForTest(t)

	// Seed a pull_request row to satisfy the FK.
	if _, err := db.sql.Exec(`INSERT INTO pull_request
		(repo,number,ownership,state,head_sha,created_at,updated_at)
		VALUES ('o/r',99,'mine','open','sha99','t','t')`); err != nil {
		t.Fatalf("seed pr: %v", err)
	}
	var prID int64
	_ = db.sql.QueryRow("SELECT id FROM pull_request WHERE repo='o/r' AND number=99").Scan(&prID)

	// Valid ci_state + bogus my_review_state: the CHECK on my_review_state must reject it.
	_, err := db.sql.Exec(`INSERT INTO pr_revision
		(pr_id,seq,head_sha,observed_at,last_seen_at,ci_state,my_review_state)
		VALUES (?,?,?,?,?,?,?)`, prID, 1, "sha99", "t", "t", "none", "bogus")
	if err == nil {
		t.Fatal("expected my_review_state CHECK to reject 'bogus'")
	}
}
