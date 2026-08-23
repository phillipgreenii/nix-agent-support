package store

import (
	"context"
	"testing"
)

// Schema v11 adds a per-revision approval-gate state (pg2-4dz88.2.5): the
// gate's overall verdict — satisfied|partially-satisfied|unsatisfied|unknown
// — recorded distinct from ci_state, plus the (n,m) pair that
// partially-satisfied/unsatisfied carry. The columns are additive (ALTER ADD
// COLUMN), so a fresh (from-empty) database — every OpenForTest call in this
// package — migrates straight through this step to schemaVersion.
func TestMigrate_V11GateStateColumns(t *testing.T) {
	db := OpenForTest(t)

	var v int
	if err := db.sql.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if v != schemaVersion || schemaVersion < 11 {
		t.Fatalf("user_version=%d schemaVersion=%d; want both >= 11", v, schemaVersion)
	}

	for _, col := range []string{"gate_state", "gate_state_n", "gate_state_m", "gate_state_captured_at"} {
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

	// Idempotent re-migrate.
	if err := migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

// A revision inserted with no gate_state value (as a pre-migration row would
// read once the ALTER's DEFAULT applies) backfills to 'unknown' with NULL
// n/m/captured_at — never a fabricated 'satisfied', since no gate observation
// exists for that historical row. Also proves PRAGMA foreign_key_check stays
// clean (this is a plain additive ALTER, no table rebuild, but the migration
// framework runs the check unconditionally after every step — see
// applyMigration in migrate.go and its precedent in migrate_fk_check_test.go).
func TestMigrate_V11GateStateDefaults(t *testing.T) {
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
	got := revs[0]
	if got.GateState != "unknown" {
		t.Errorf("GateState default: got %q, want \"unknown\"", got.GateState)
	}
	if got.GateStateN != 0 || got.GateStateM != 0 {
		t.Errorf("GateState n/m default: got n=%d m=%d, want both 0 (NULL)", got.GateStateN, got.GateStateM)
	}
	if got.GateStateCapturedAt != "" {
		t.Errorf("GateStateCapturedAt default: got %q, want \"\" (NULL)", got.GateStateCapturedAt)
	}

	// No dangling FK references after the migration.
	rows, err := db.sql.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Fatal("foreign_key_check reported a violation after the v11 migration")
	}

	// Idempotent re-migrate.
	if err := migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

// The gate_state column carries a CHECK constraint on the four-state
// vocabulary (mirroring pr_revision.ci_state's own CHECK pattern); an
// out-of-vocabulary value must be rejected.
func TestMigrate_V11GateStateCheckRejectsBogus(t *testing.T) {
	db := OpenForTest(t)
	prID := seedPR(t, db)

	if _, err := db.sql.Exec(`INSERT INTO pr_revision
		(pr_id,seq,head_sha,observed_at,last_seen_at,ci_state,gate_state)
		VALUES (?,?,?,?,?,?,?)`, prID, 1, "h1", "t", "t", "none", "bogus"); err == nil {
		t.Fatal("expected gate_state CHECK to reject 'bogus'")
	}

	// A legitimate vocabulary value is unaffected by the CHECK.
	if _, err := db.sql.Exec(`INSERT INTO pr_revision
		(pr_id,seq,head_sha,observed_at,last_seen_at,ci_state,gate_state)
		VALUES (?,?,?,?,?,?,?)`, prID, 2, "h2", "t", "t", "none", "partially-satisfied"); err != nil {
		t.Fatalf("insert with valid gate_state should succeed: %v", err)
	}
}
