package store

import (
	"context"
	"testing"
)

// Schema v7 adds a per-revision others-approved marker (pg2-4c5i.13): a
// teammate (non-self) APPROVED review is recorded per revision so the attention
// predicate stays store-derived. The columns are additive (ALTER ADD COLUMN with
// defaults), so existing rows backfill to 0 / NULL.
func TestMigrate_V7OthersApprovedColumns(t *testing.T) {
	db := OpenForTest(t)

	var v int
	if err := db.sql.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if v != schemaVersion || schemaVersion < 7 {
		t.Fatalf("user_version=%d schemaVersion=%d; want both >= 7", v, schemaVersion)
	}

	for _, col := range []string{"others_approved", "others_approved_at"} {
		var cnt int
		if err := db.sql.QueryRow(
			"SELECT COUNT(*) FROM pragma_table_info('pr_revision') WHERE name=?", col,
		).Scan(&cnt); err != nil {
			t.Fatalf("pragma_table_info %s: %v", col, err)
		}
		if cnt != 1 {
			t.Errorf("column %q missing from pr_revision", col)
		}
	}
}

// A revision inserted with no others_approved value backfills to 0 (NOT NULL
// DEFAULT 0), and Scan (via ListRevisions) reads it as false / "".
func TestMigrate_V7OthersApprovedDefaults(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)

	if _, err := db.sql.Exec(`INSERT INTO pr_revision
		(pr_id,seq,head_sha,observed_at,last_seen_at,ci_state)
		VALUES (?,?,?,?,?,?)`, prID, 1, "h1", "t", "t", "none"); err != nil {
		t.Fatalf("seed revision: %v", err)
	}

	revs, err := db.ListRevisions(ctx, prID)
	if err != nil {
		t.Fatalf("ListRevisions: %v", err)
	}
	if len(revs) != 1 {
		t.Fatalf("want 1 revision, got %d", len(revs))
	}
	if revs[0].OthersApproved {
		t.Errorf("OthersApproved should default false, got true")
	}
	if revs[0].OthersApprovedAt != "" {
		t.Errorf("OthersApprovedAt should default empty, got %q", revs[0].OthersApprovedAt)
	}

	// Idempotent re-migrate.
	if err := migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}
