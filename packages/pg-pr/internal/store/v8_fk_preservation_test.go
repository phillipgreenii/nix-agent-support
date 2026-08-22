package store

import "testing"

// TestMigrate_V8PreservesFKChildren actually exercises the v8 pull_request
// table-rebuild (migrate.go: widen the ownership CHECK to add 'co-owned' via
// the SQLite 12-step ALTER) with FK-bearing children PRESENT. The existing
// TestMigrate_V8CoOwnedOwnership runs against a fresh empty DB, so the rebuild
// drops/recreates with zero feedback/pr_revision children and foreign_key_check
// passes trivially — the "preserves ids + child links" behavior is never
// tested.
//
// A literal mirror of TestMigrate_V6PreservesCodeCommentMessages would NOT help:
// after OpenForTest the DB is already at the terminal schema version, so a
// second migrate() is a no-op (the `for current < schemaVersion` loop doesn't
// run) and the rebuild never re-runs against the seeded rows. So this test
// rolls the version COUNTER back to 7 (not the schema — the table keeps its
// v8 CHECK) and applies ONLY the v8 rebuild DDL directly via
// applyMigration(db, 8, migrations[7]) against a populated table, which is
// exactly the data-bearing step of a production v7->v8 upgrade. Setting
// user_version in a store test has precedent (migrate_test.go sets it to
// 9999). v8 is NOT the terminal migration once schema v9 exists
// (pg2-4dz88.1.5 added a purely-additive pr_approval table after it), so a
// full migrate() call here would ALSO re-run the v9 step and fail with
// "table pr_approval already exists" (v9's CREATE TABLE is not idempotent
// against a DB already at v9) — applyMigration(db, 8, migrations[7]) runs
// ONLY the v8 step, mirroring TestMigrate_V6PreservesCodeCommentMessages's
// technique for the same reason (v6 was not terminal either). The rollback is
// lossless: the only columns ever added to pull_request are the 6 v2
// enrichment columns and the v8 INSERT...SELECT copies all of them.
// (pg2-2ozt3)
func TestMigrate_V8PreservesFKChildren(t *testing.T) {
	db := OpenForTest(t) // full v8 schema present

	// Seed a co-owned parent (the widened value must round-trip the rebuild)
	// plus one child in each ON DELETE CASCADE table (feedback, pr_revision).
	//
	// Use an EXPLICIT non-1 id (42): if a regression dropped `id` from the v8
	// INSERT...SELECT, the single copied row would be re-assigned rowid 1, so
	// checking `WHERE id=1` (the value a fresh assignment happens to produce)
	// would still find it and hide the bug. Seeding id=42 makes both the
	// id-preservation lookup and the child FK links load-bearing: an
	// id-renumbering rebuild leaves the children's pr_id=42 dangling.
	const prID int64 = 42
	if _, err := db.sql.Exec(`INSERT INTO pull_request
		(id,repo,number,ownership,state,head_sha,created_at,updated_at)
		VALUES (?,'o/r',1,'co-owned','open','h1','t','t')`, prID); err != nil {
		t.Fatalf("seed pull_request: %v", err)
	}

	// feedback child: 'pr-comments' needs no file (the code-comment-thread =>
	// file-NOT-NULL CHECK does not apply).
	if _, err := db.sql.Exec(`INSERT INTO feedback
		(pr_id,kind,fingerprint,status,body,created_at,updated_at)
		VALUES (?,'pr-comments','fp1','new','a finding','t','t')`, prID); err != nil {
		t.Fatalf("seed feedback child: %v", err)
	}
	// pr_revision child.
	if _, err := db.sql.Exec(`INSERT INTO pr_revision
		(pr_id,seq,head_sha,observed_at,last_seen_at)
		VALUES (?,1,'h1','t','t')`, prID); err != nil {
		t.Fatalf("seed pr_revision child: %v", err)
	}

	// Force ONLY the v8 rebuild to re-run against the seeded rows (v8 is no
	// longer the terminal migration, so a full migrate() would also re-run
	// the v9 step — see the doc comment above for why that fails).
	if _, err := db.sql.Exec("PRAGMA user_version = 7"); err != nil {
		t.Fatalf("roll user_version back to 7: %v", err)
	}
	if err := applyMigration(db, 8, migrations[7]); err != nil {
		t.Fatalf("re-run v8 rebuild over seeded data: %v", err)
	}

	// Parent survived with the SAME id and the co-owned value intact.
	var gotOwnership string
	if err := db.sql.QueryRow(
		"SELECT ownership FROM pull_request WHERE id=?", prID,
	).Scan(&gotOwnership); err != nil {
		t.Fatalf("pull_request row lost across rebuild (id=%d): %v", prID, err)
	}
	if gotOwnership != "co-owned" {
		t.Fatalf("ownership not preserved across rebuild: got %q, want co-owned", gotOwnership)
	}

	// feedback child survived with its link AND full row data intact.
	var fbPRID int64
	var fbFingerprint string
	if err := db.sql.QueryRow(
		"SELECT pr_id, fingerprint FROM feedback WHERE fingerprint='fp1'",
	).Scan(&fbPRID, &fbFingerprint); err != nil {
		t.Fatalf("feedback child lost across rebuild: %v", err)
	}
	if fbPRID != prID {
		t.Fatalf("feedback FK link broken: pr_id=%d, want %d", fbPRID, prID)
	}

	// pr_revision child survived with its link AND full row data intact.
	var revPRID int64
	var revHeadSHA string
	if err := db.sql.QueryRow(
		"SELECT pr_id, head_sha FROM pr_revision WHERE pr_id=? AND seq=1", prID,
	).Scan(&revPRID, &revHeadSHA); err != nil {
		t.Fatalf("pr_revision child lost across rebuild: %v", err)
	}
	if revPRID != prID {
		t.Fatalf("pr_revision FK link broken: pr_id=%d, want %d", revPRID, prID)
	}
	if revHeadSHA != "h1" {
		t.Fatalf("pr_revision row data not preserved: head_sha=%q, want h1", revHeadSHA)
	}

	// No dangling FK references anywhere after the rebuild.
	rows, err := db.sql.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Fatal("foreign_key_check reported a violation after the pull_request rebuild")
	}
}
