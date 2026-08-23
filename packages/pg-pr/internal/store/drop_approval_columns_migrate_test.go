package store

import (
	"context"
	"testing"
)

// Schema v12 drops the four pr_revision columns pg2-4dz88.1.9 left write-only
// once its per-approver pr_approval cutover moved every reader off them:
// others_approved, others_approved_at, my_review_state, reviewed_at
// (pg2-tgrip). The drop is a plain native ALTER TABLE ... DROP COLUMN per
// column (not a 12-step table rebuild, unlike v6/v8): none of the four is a
// PRIMARY KEY, UNIQUE, indexed, or referenced by another column's generated
// expression or a foreign key — the cases SQLite's DROP COLUMN refuses — and
// my_review_state's own self-referencing CHECK is dropped along with it.
func TestMigrate_V12DropsRetiredApprovalColumns(t *testing.T) {
	db := OpenForTest(t) // fresh DB migrates straight through v12

	var v int
	if err := db.sql.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if v != schemaVersion || schemaVersion < 12 {
		t.Fatalf("user_version=%d schemaVersion=%d; want both >= 12", v, schemaVersion)
	}

	for _, col := range []string{"others_approved", "others_approved_at", "my_review_state", "reviewed_at"} {
		var cnt int
		if err := db.sql.QueryRow(
			"SELECT COUNT(*) FROM pragma_table_info('pr_revision') WHERE name=?", col,
		).Scan(&cnt); err != nil {
			t.Fatalf("pragma_table_info %s: %v", col, err)
		}
		if cnt != 0 {
			t.Errorf("column %q still present on pr_revision after v12; want dropped", col)
		}
	}

	// Idempotent re-migrate.
	if err := migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

// A v11 DB with EXISTING DATA — including values in the four columns about to
// be dropped AND in columns that survive (head/base SHA, the CI rollup,
// gate_state) — upgrades cleanly to v12: the drop must not disturb the
// surviving columns' data, and must not fail merely because the retired
// columns are non-empty.
//
// OpenForTest already migrates a fresh DB straight through v12, physically
// removing the four columns, so — mirroring
// TestMigrate_V9BackfillsExistingData's technique (pg2-2ozt3) of forcing one
// step to re-run against seeded data — this test re-materializes the four
// columns with the SAME DDL migrations[2] (v3) and migrations[6] (v7)
// originally used, seeds a pre-v12-shaped row, rolls the version COUNTER back
// to 11, and re-applies ONLY the v12 step (applyMigration(db, 12,
// migrations[11])) directly against it — exactly the data-bearing step of a
// production v11->v12 upgrade.
func TestMigrate_V12PreservesSurvivingColumnData(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t) // full v12 schema present; the four columns are gone
	prID := seedPR(t, db)

	// Re-materialize the four retired columns exactly as migrations[2] (v3)
	// and migrations[6] (v7) originally defined them, so this DB is
	// schema-identical to a production database right before the v12 drop.
	if _, err := db.sql.Exec(`ALTER TABLE pr_revision ADD COLUMN reviewed_at TEXT`); err != nil {
		t.Fatalf("re-add reviewed_at: %v", err)
	}
	if _, err := db.sql.Exec(`ALTER TABLE pr_revision ADD COLUMN my_review_state TEXT CHECK (my_review_state IS NULL OR
                      my_review_state IN ('approved','changes-requested','commented'))`); err != nil {
		t.Fatalf("re-add my_review_state: %v", err)
	}
	if _, err := db.sql.Exec(`ALTER TABLE pr_revision ADD COLUMN others_approved    INTEGER NOT NULL DEFAULT 0`); err != nil {
		t.Fatalf("re-add others_approved: %v", err)
	}
	if _, err := db.sql.Exec(`ALTER TABLE pr_revision ADD COLUMN others_approved_at TEXT`); err != nil {
		t.Fatalf("re-add others_approved_at: %v", err)
	}

	// Seed a revision carrying data in BOTH the four retired columns and a
	// representative set of surviving columns.
	if _, err := db.sql.Exec(`INSERT INTO pr_revision
		(pr_id,seq,head_sha,base_sha,observed_at,last_seen_at,
		 ci_state,ci_passed,ci_failed,ci_pending,gate_state,
		 reviewed_at,my_review_state,others_approved,others_approved_at)
		VALUES (?,1,'h1','b1','t1','t1',
		        'success',3,0,0,'satisfied',
		        't1-reviewed','approved',1,'t1-approved')`, prID); err != nil {
		t.Fatalf("seed pre-v12 revision: %v", err)
	}

	// Force the v12 migration to re-run against the seeded row.
	if _, err := db.sql.Exec("PRAGMA user_version = 11"); err != nil {
		t.Fatalf("roll user_version back to 11: %v", err)
	}
	if err := applyMigration(db, 12, migrations[11]); err != nil {
		t.Fatalf("re-run v12 drop over seeded data: %v", err)
	}

	// Surviving columns' data is untouched.
	revs, err := db.ListRevisions(ctx, prID)
	if err != nil {
		t.Fatalf("ListRevisions: %v", err)
	}
	if len(revs) != 1 {
		t.Fatalf("want 1 revision, got %d: %+v", len(revs), revs)
	}
	got := revs[0]
	if got.HeadSHA != "h1" || got.BaseSHA != "b1" {
		t.Errorf("head/base sha not preserved: %+v", got)
	}
	if got.CIState != "success" || got.CIPassed != 3 {
		t.Errorf("CI rollup not preserved: %+v", got)
	}
	if got.GateState != "satisfied" {
		t.Errorf("gate_state not preserved: %+v", got)
	}

	// The four retired columns are actually gone.
	for _, col := range []string{"others_approved", "others_approved_at", "my_review_state", "reviewed_at"} {
		var cnt int
		if err := db.sql.QueryRow(
			"SELECT COUNT(*) FROM pragma_table_info('pr_revision') WHERE name=?", col,
		).Scan(&cnt); err != nil {
			t.Fatalf("pragma_table_info %s: %v", col, err)
		}
		if cnt != 0 {
			t.Errorf("column %q still present after re-running the v12 drop", col)
		}
	}

	// No dangling FK references after the migration.
	rows, err := db.sql.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Fatal("foreign_key_check reported a violation after the v12 migration")
	}

	// Idempotent re-migrate.
	if err := migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}
