package store

import "testing"

// TestMigrate_V14UserStateColumns exercises the v13->v14 additive migration
// (pg2-4dz88.4.2) that adds the USER_HIDDEN column set — user_hidden,
// user_hidden_reason, wip — to pull_request. See migrate.go's v13->v14 entry
// for the full rationale (the fork #5/#6/#7 operator rulings, and why this
// needs no table rebuild). A fresh (from-empty) database — every
// OpenForTest call in this package — migrates straight through this step to
// schemaVersion.
func TestMigrate_V14UserStateColumns(t *testing.T) {
	db := OpenForTest(t)

	var v int
	if err := db.sql.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if v != schemaVersion || schemaVersion < 14 {
		t.Fatalf("user_version=%d schemaVersion=%d; want both >= 14", v, schemaVersion)
	}

	for _, col := range []string{"user_hidden", "user_hidden_reason", "wip"} {
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

	// Idempotent re-migrate.
	if err := migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

// TestMigrate_V14UpgradeFromPriorVersionPreservesRows fabricates a genuinely
// v13-shaped pull_request table — the 3 new columns physically ABSENT, not
// merely a rolled-back version counter — using DROP COLUMN (the same
// capability the v11->v12 migration relies on; see
// drop_approval_columns_migrate_test.go's TestMigrate_V12PreservesSurvivingColumnData
// for the precedent this mirrors almost line-for-line), seeds a row against
// that shape, then re-runs ONLY the v13->v14 step directly via
// applyMigration. Simply rolling PRAGMA user_version back without also
// dropping the columns would make the ADD COLUMN statements fail on
// "duplicate column name" (unlike a table-rebuild migration, a plain ADD
// COLUMN step is not safe to re-run against a table that already has the
// column) — this is why the fabricated shape must also drop `body` (the
// v14->v15 column, pg2-1o1dp): OpenForTest below migrates the fresh DB all
// the way to the CURRENT schemaVersion, so body already exists before this
// test starts undoing columns; leaving it in place would make the v13
// fabrication dishonest and would make the trailing migrate() call below
// fail with exactly that "duplicate column name: body" error (which is
// literally what happened before this test was updated for schema v15).
// Asserts the pre-existing row keeps its id and every prior column value,
// and the three v13->v14 columns backfill to their declared defaults.
func TestMigrate_V14UpgradeFromPriorVersionPreservesRows(t *testing.T) {
	db := OpenForTest(t) // full v14 schema present

	// Seed a row with representative values in every prior column this
	// migration must preserve.
	const prID int64 = 77
	if _, err := db.sql.Exec(`INSERT INTO pull_request
		(id,repo,number,ownership,author,state,branch,base,url,head_sha,last_synced_at,created_at,updated_at)
		VALUES (?,'o/r',9,'mine','me','open','b','base-b','http://example.invalid/9','h1','t0','t','t')`,
		prID); err != nil {
		t.Fatalf("seed pull_request: %v", err)
	}

	// Fabricate the v13 shape: physically drop the 3 columns this migration
	// adds, PLUS `body` (added later still, by v14->v15) — OpenForTest above
	// already brought this DB to the current schemaVersion, so body exists
	// and must come off too for the fabricated shape to be honestly v13's,
	// then roll the version counter back to match.
	for _, col := range []string{"user_hidden", "user_hidden_reason", "wip", "body"} {
		if _, err := db.sql.Exec("ALTER TABLE pull_request DROP COLUMN " + col); err != nil {
			t.Fatalf("drop %s to fabricate v13 shape: %v", col, err)
		}
	}
	// Same reasoning applies one table over: pg2-ynhr.5's v15->v16 step drops
	// pr_revision.reviewed_by_agent_at, and OpenForTest above already dropped
	// it physically. The trailing migrate() call re-runs v15->v16 (among the
	// other remaining steps), so the column must exist again first, or that
	// DROP COLUMN fails with "no such column" against a table that already
	// lacks it — the exact mirror of the body/"duplicate column name" trap
	// this comment already describes.
	if _, err := db.sql.Exec(`ALTER TABLE pr_revision ADD COLUMN reviewed_by_agent_at TEXT`); err != nil {
		t.Fatalf("re-add reviewed_by_agent_at to fabricate pre-v16 shape: %v", err)
	}
	// pg2-ynhr.8's v16->v17 step CREATEs repo_sync_state, and OpenForTest
	// above already created it. The trailing migrate() call re-runs v16->v17
	// too, so the table must be ABSENT first, or that CREATE TABLE fails with
	// "table repo_sync_state already exists" — the table-level mirror of the
	// two column-level traps above.
	if _, err := db.sql.Exec(`DROP TABLE repo_sync_state`); err != nil {
		t.Fatalf("drop repo_sync_state to fabricate pre-v17 shape: %v", err)
	}
	if _, err := db.sql.Exec("PRAGMA user_version = 13"); err != nil {
		t.Fatalf("roll user_version back to 13: %v", err)
	}

	// Re-run ONLY the v13->v14 step against the seeded, prior-shaped row —
	// exactly the data-bearing step of a production v13->v14 upgrade.
	if err := applyMigration(db, 14, migrations[13]); err != nil {
		t.Fatalf("re-run v13->v14 migration over seeded data: %v", err)
	}

	var repo, ownership, author, state, branch, base, url, headSHA, lastSyncedAt string
	var number int
	var userHidden, wip int
	var userHiddenReason string
	if err := db.sql.QueryRow(`SELECT repo, number, ownership, author, state, branch, base, url,
		head_sha, last_synced_at, user_hidden, user_hidden_reason, wip
		FROM pull_request WHERE id=?`, prID).Scan(
		&repo, &number, &ownership, &author, &state, &branch, &base, &url,
		&headSHA, &lastSyncedAt, &userHidden, &userHiddenReason, &wip,
	); err != nil {
		t.Fatalf("pull_request row lost across migration (id=%d): %v", prID, err)
	}
	if repo != "o/r" || number != 9 || ownership != "mine" || author != "me" || state != "open" ||
		branch != "b" || base != "base-b" || url != "http://example.invalid/9" ||
		headSHA != "h1" || lastSyncedAt != "t0" {
		t.Fatalf("prior columns not preserved: repo=%q number=%d ownership=%q author=%q state=%q "+
			"branch=%q base=%q url=%q head_sha=%q last_synced_at=%q",
			repo, number, ownership, author, state, branch, base, url, headSHA, lastSyncedAt)
	}
	if userHidden != 0 || userHiddenReason != "" || wip != 0 {
		t.Fatalf("new columns did not backfill to declared defaults: user_hidden=%d user_hidden_reason=%q wip=%d",
			userHidden, userHiddenReason, wip)
	}

	// No dangling FK references after the migration.
	rows, err := db.sql.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Fatal("foreign_key_check reported a violation after the v13->v14 migration")
	}

	// applyMigration above only ran the v13->v14 step under test; the
	// fabricated DB is genuinely at v14 now (body was dropped above, not
	// re-added), so this migrate() call performs the REMAINING steps up to
	// the current schemaVersion (today: v14->v15 re-adding body, then
	// v15->v16 dropping pr_revision.reviewed_by_agent_at) — proving the
	// v13->v14 step composes cleanly with whatever comes after it, not that
	// this call is a no-op.
	if err := migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var body string
	if err := db.sql.QueryRow("SELECT body FROM pull_request WHERE id=?", prID).Scan(&body); err != nil {
		t.Fatalf("body column missing after migrate() caught up to schemaVersion: %v", err)
	}
	if body != "" {
		t.Fatalf("body did not backfill to its declared default: got %q", body)
	}
}

// TestMigrate_V14BackfillOnBareInsert covers a row inserted through raw SQL
// naming none of the three new columns (as a pre-migration INSERT statement
// executed mid-migration would): it must backfill to the declared defaults
// and scan back correctly through scanPR, never NULL.
func TestMigrate_V14BackfillOnBareInsert(t *testing.T) {
	db := OpenForTest(t)

	if _, err := db.sql.Exec(`INSERT INTO pull_request
		(repo,number,ownership,author,state,branch,base,url,head_sha,last_synced_at,created_at,updated_at)
		VALUES ('o/r',20,'mine','me','open','b','base','http://example.invalid/20','h1','t0','t','t')`); err != nil {
		t.Fatalf("bare insert: %v", err)
	}

	row := db.sql.QueryRow("SELECT " + prColumns + " FROM pull_request WHERE repo='o/r' AND number=20")
	pr, err := scanPR(row)
	if err != nil {
		t.Fatalf("scanPR: %v", err)
	}
	if pr.UserHidden || pr.WIP || pr.UserHiddenReason != "" {
		t.Fatalf("bare insert did not backfill declared defaults: %+v", pr)
	}
}
