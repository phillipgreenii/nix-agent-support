package store

import (
	"context"
	"fmt"
	"testing"
)

// Schema v9 replaces the single self-only my_review_state slot and the single
// teammate-only others_approved boolean with one row PER (pr_id, approver) in
// a new pr_approval table (pg2-4dz88.1.5), so two teammates approving are two
// distinguishable rows and per-approver staleness is representable. This is
// additive: a new table, no ALTER on pr_revision — the old columns are
// untouched and still populated by the sync write path.
func TestMigrate_V9ApprovalTableExists(t *testing.T) {
	db := OpenForTest(t)

	var v int
	if err := db.sql.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if v != schemaVersion || schemaVersion < 9 {
		t.Fatalf("user_version=%d schemaVersion=%d; want both >= 9", v, schemaVersion)
	}

	var name string
	if err := db.sql.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name='pr_approval'",
	).Scan(&name); err != nil {
		t.Fatalf("pr_approval table missing: %v", err)
	}

	for _, col := range []string{"id", "pr_id", "approver", "state", "head_sha", "observed_at"} {
		var cnt int
		if err := db.sql.QueryRow(
			"SELECT COUNT(*) FROM pragma_table_info('pr_approval') WHERE name=?", col,
		).Scan(&cnt); err != nil {
			t.Fatalf("pragma_table_info %s: %v", col, err)
		}
		if cnt != 1 {
			t.Errorf("column %q missing from pr_approval", col)
		}
	}

	// Idempotent re-migrate.
	if err := migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

// A v9 DB with NO recorded self review and NO teammate approval backfills to
// zero pr_approval rows — absence stays absence; the migration must not
// fabricate a row for a marker that was never observed. This is the
// "documented default": an unreviewed revision leaves pr_approval empty.
func TestMigrate_V9BackfillDefaultIsNoRows(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)
	if _, _, err := db.RecordRevision(ctx, prID, "h1", "b1"); err != nil {
		t.Fatalf("RecordRevision: %v", err)
	}

	approvals, err := db.ListApprovals(ctx, prID)
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(approvals) != 0 {
		t.Errorf("no self/teammate state was ever recorded; want 0 backfilled rows, got %d: %+v", len(approvals), approvals)
	}
}

// A v8 DB SEEDED with existing my_review_state (self) and
// others_approved/others_approved_at (teammate) values migrates those into
// SPECIFIC rows of pr_approval on upgrade, picking the LATEST matching
// revision per marker.
//
// After OpenForTest the DB is already at v9 with pr_approval created (empty).
// Mirroring TestMigrate_V8PreservesFKChildren's technique (pg2-2ozt3): v9 is
// the terminal migration, so dropping the (empty) pr_approval table and
// rolling the version COUNTER back to 8 forces migrate() to re-run ONLY the
// v9 step against the already-seeded pr_revision rows — exactly the
// data-bearing step of a production v8->v9 upgrade.
func TestMigrate_V9BackfillsExistingData(t *testing.T) {
	db := OpenForTest(t) // full v9 schema present
	prID := seedPR(t, db)

	// Seed TWO revisions: h1 (self reviewed only) then h2 (self reviewed AGAIN
	// + a teammate approval) — so the backfill must pick the LATEST matching
	// revision per marker, not just any matching row.
	if _, err := db.sql.Exec(`INSERT INTO pr_revision
		(pr_id,seq,head_sha,observed_at,last_seen_at,reviewed_at,my_review_state)
		VALUES (?,1,'h1','t1','t1','t1-reviewed','changes-requested')`, prID); err != nil {
		t.Fatalf("seed revision h1: %v", err)
	}
	if _, err := db.sql.Exec(`INSERT INTO pr_revision
		(pr_id,seq,head_sha,observed_at,last_seen_at,reviewed_at,my_review_state,others_approved,others_approved_at)
		VALUES (?,2,'h2','t2','t2','t2-reviewed','approved',1,'t2-approved')`, prID); err != nil {
		t.Fatalf("seed revision h2: %v", err)
	}

	// Force the v9 migration to re-run against the seeded pr_revision rows.
	if _, err := db.sql.Exec("DROP TABLE pr_approval"); err != nil {
		t.Fatalf("drop pr_approval to re-force migration: %v", err)
	}
	if _, err := db.sql.Exec("PRAGMA user_version = 8"); err != nil {
		t.Fatalf("roll user_version back to 8: %v", err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("re-run v9 migration over seeded data: %v", err)
	}

	ctx := context.Background()
	approvals, err := db.ListApprovals(ctx, prID)
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(approvals) != 2 {
		t.Fatalf("want 2 backfilled rows (self + teammate), got %d: %+v", len(approvals), approvals)
	}
	byApprover := map[string]Approval{}
	for _, a := range approvals {
		byApprover[a.Approver] = a
	}

	self, ok := byApprover["self"]
	if !ok {
		t.Fatalf("no backfilled 'self' row: %+v", approvals)
	}
	if self.State != "approved" || self.HeadSHA != "h2" || self.ObservedAt != "t2-reviewed" {
		t.Errorf("self backfill = %+v, want state=approved head_sha=h2 observed_at=t2-reviewed (the LATEST self-reviewed revision)", self)
	}

	teammate, ok := byApprover["teammate"]
	if !ok {
		t.Fatalf("no backfilled 'teammate' row: %+v", approvals)
	}
	if teammate.State != "approved" || teammate.HeadSHA != "h2" || teammate.ObservedAt != "t2-approved" {
		t.Errorf("teammate backfill = %+v, want state=approved head_sha=h2 observed_at=t2-approved", teammate)
	}

	// Old columns are still present and correct — no read-seam consumer has
	// cut over yet in this leaf.
	var myReviewState string
	var othersApproved int
	var othersApprovedAt string
	if err := db.sql.QueryRow(
		"SELECT my_review_state, others_approved, others_approved_at FROM pr_revision WHERE pr_id=? AND seq=2",
		prID,
	).Scan(&myReviewState, &othersApproved, &othersApprovedAt); err != nil {
		t.Fatalf("read back old columns: %v", err)
	}
	if myReviewState != "approved" || othersApproved != 1 || othersApprovedAt != "t2-approved" {
		t.Errorf("old columns changed by migration: my_review_state=%q others_approved=%d others_approved_at=%q",
			myReviewState, othersApproved, othersApprovedAt)
	}

	// No dangling FK references after the migration.
	rows, err := db.sql.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Fatal("foreign_key_check reported a violation after the v9 migration")
	}
}

// A DB whose user_version already exceeds this binary's schemaVersion refuses
// to open — the existing "schema newer than this binary" guard
// (TestMigrateRefusesNewerSchema) re-asserted at the v9 boundary this leaf
// introduces, matching the acceptance criteria's "a v9-stamped DB still
// refuses to open on the v8 binary's version check": the same comparison
// (current > schemaVersion) that would make an old v8 binary refuse a
// v9-stamped DB is what this test exercises against the live guard.
func TestMigrate_V9RefusesNewerSchema(t *testing.T) {
	db := OpenForTest(t)
	if _, err := db.sql.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion+1)); err != nil {
		t.Fatalf("bump version: %v", err)
	}
	if err := migrate(db); err == nil {
		t.Fatal("expected error migrating a schema newer than this binary")
	}
}
