package store

import (
	"context"
	"fmt"
	"testing"
)

// Schema v16 drops pr_revision.reviewed_by_agent_at (pg2-ynhr.5). It was the
// re-review-on-head-advance cursor for the legacy pg-pr draft-review consumer
// (pg2-4c5i.36), removed wholesale by this bead's strip of pg-pr's review
// workflow — that workflow shipped to pr-pool (ADR 0034), whose review-pr
// bead now carries its own head_sha cursor. Mirrors
// TestMigrate_V12DropsRetiredApprovalColumns's shape (plain native DROP
// COLUMN, not a 12-step rebuild): the column is not a PRIMARY KEY, UNIQUE,
// indexed, or referenced by another column's CHECK/generated expression.
func TestMigrate_V16DropsReviewedByAgentColumn(t *testing.T) {
	db := OpenForTest(t) // fresh DB migrates straight through v16

	var v int
	if err := db.sql.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if v != schemaVersion || schemaVersion < 16 {
		t.Fatalf("user_version=%d schemaVersion=%d; want both >= 16", v, schemaVersion)
	}

	var cnt int
	if err := db.sql.QueryRow(
		"SELECT COUNT(*) FROM pragma_table_info('pr_revision') WHERE name='reviewed_by_agent_at'",
	).Scan(&cnt); err != nil {
		t.Fatalf("pragma_table_info reviewed_by_agent_at: %v", err)
	}
	if cnt != 0 {
		t.Errorf("column %q still present on pr_revision after v16; want dropped", "reviewed_by_agent_at")
	}

	// Idempotent re-migrate.
	if err := migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

// A v15 DB with EXISTING DATA — including a value in the column about to be
// dropped AND in columns that survive (head/base SHA, the CI rollup,
// gate_state) — upgrades cleanly to v16: the drop must not disturb the
// surviving columns' data, and must not fail merely because the retired
// column is non-empty. Mirrors TestMigrate_V12PreservesSurvivingColumnData's
// technique: re-materialize the column with the SAME DDL migrations[4] (v5)
// originally used, seed a pre-v16-shaped row, roll the version counter back
// to 15, and re-apply ONLY the v16 step directly against it.
func TestMigrate_V16PreservesSurvivingColumnData(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t) // full v16 schema present; the column is gone
	prID := seedPR(t, db)

	// Re-materialize the retired column exactly as migrations[4] (v5)
	// originally defined it, so this DB is schema-identical to a production
	// database right before the v16 drop.
	if _, err := db.sql.Exec(`ALTER TABLE pr_revision ADD COLUMN reviewed_by_agent_at TEXT`); err != nil {
		t.Fatalf("re-add reviewed_by_agent_at: %v", err)
	}

	// Seed a revision carrying data in BOTH the retired column and a
	// representative set of surviving columns.
	if _, err := db.sql.Exec(`INSERT INTO pr_revision
		(pr_id,seq,head_sha,base_sha,observed_at,last_seen_at,
		 ci_state,ci_passed,ci_failed,ci_pending,gate_state,
		 reviewed_by_agent_at)
		VALUES (?,1,'h1','b1','t1','t1',
		        'success',3,0,0,'satisfied',
		        't1-agent-reviewed')`, prID); err != nil {
		t.Fatalf("seed pre-v16 revision: %v", err)
	}

	// Force the v16 migration to re-run against the seeded row.
	if _, err := db.sql.Exec("PRAGMA user_version = 15"); err != nil {
		t.Fatalf("roll user_version back to 15: %v", err)
	}
	if err := applyMigration(db, 16, migrations[15]); err != nil {
		t.Fatalf("re-run v16 drop over seeded data: %v", err)
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

	// The retired column is actually gone.
	var cnt int
	if err := db.sql.QueryRow(
		"SELECT COUNT(*) FROM pragma_table_info('pr_revision') WHERE name='reviewed_by_agent_at'",
	).Scan(&cnt); err != nil {
		t.Fatalf("pragma_table_info reviewed_by_agent_at: %v", err)
	}
	if cnt != 0 {
		t.Errorf("column %q still present after re-running the v16 drop", "reviewed_by_agent_at")
	}

	// No dangling FK references after the migration.
	rows, err := db.sql.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Fatal("foreign_key_check reported a violation after the v16 migration")
	}

	// This test's technique re-runs ONLY the v16 step directly
	// (applyMigration(db, 16, ...)) against a user_version it rolled back to
	// 15, to exercise that one migration in isolation against seeded data.
	// OpenForTest(t) already ran the full migrate at the top of this test, so
	// user_version=15 is stale relative to the DB's actual on-disk schema;
	// restore it before the idempotent re-migrate check below.
	if _, err := db.sql.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		t.Fatalf("restore user_version to %d: %v", schemaVersion, err)
	}

	// Idempotent re-migrate.
	if err := migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}
