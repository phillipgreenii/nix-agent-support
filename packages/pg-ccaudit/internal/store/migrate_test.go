package store

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestSchemaUpgradeForcesAFullReIngest covers the pg2-oisvb migration path.
//
// The bad outcome this guards against is not a crash — it is a database that
// answers the NEW queries with structurally valid, silently incomplete numbers. The
// data every schema bump so far adds (user prose, in this case) was never written to
// the database, so there is nothing to backfill FROM; it exists only in the
// transcripts. Leaving the old rows in place would therefore produce a
// human-turn count of zero that looks exactly like a corpus with no human turns.
func TestSchemaUpgradeForcesAFullReIngest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcripts.db")

	// Stand up a database that looks like schema 1: the current DDL, one file row and
	// its children, and the OLD user_version stamped last.
	db, err := Open(path, false)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	seed := [][]any{
		{`INSERT INTO files (path, project_dir, size, mtime, resume_offset, complete, lines_ok, lines_bad, ingested_at)
		  VALUES ('/t/a.jsonl', 'p', 100, 1, 100, 1, 2, 0, 1)`},
		{`INSERT INTO events (path, seq, type) VALUES ('/t/a.jsonl', 0, 'assistant')`},
		{`INSERT INTO tool_calls (tool_use_id, path, seq, tool_name, input_json) VALUES ('t1', '/t/a.jsonl', 0, 'Bash', '{}')`},
		{`INSERT INTO tool_results (tool_use_id, path, seq, is_error, content_len) VALUES ('t1', '/t/a.jsonl', 1, 1, 5)`},
		{`INSERT INTO assistant_text (path, seq, text) VALUES ('/t/a.jsonl', 2, 'narration')`},
		{`INSERT INTO user_text (path, seq, text_len, text, interrupted) VALUES ('/t/a.jsonl', 3, 4, 'typed', 0)`},
	}
	for _, s := range seed {
		if _, err := db.Exec(s[0].(string)); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if _, err := db.Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatalf("stamp old version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Re-open at the current version: the migration must clear the database.
	db2, err := Open(path, false)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer func() { _ = db2.Close() }()

	for _, table := range CanonicalTables {
		var n int
		if err := db2.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("table %s still holds %d row(s) after a schema upgrade; the next sweep must re-ingest from zero", table, n)
		}
	}
	var v int
	if err := db2.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if v != SchemaVersion {
		t.Errorf("user_version=%d after upgrade, want %d", v, SchemaVersion)
	}
}

// Clearing `files` is what makes the re-ingest happen. Resetting the resume offsets
// in place would leave (size, mtime) matching, so ingest's `decide` would classify
// every file as `unchanged` and skip it — the migration would appear to succeed and
// index nothing.
func TestMigrationClearsFilesNotJustOffsets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcripts.db")
	db, err := Open(path, false)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO files (path, project_dir, size, mtime, resume_offset, complete, lines_ok, lines_bad, ingested_at)
	                      VALUES ('/t/a.jsonl', 'p', 100, 1, 100, 1, 2, 0, 1)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	db2, err := Open(path, false)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer func() { _ = db2.Close() }()
	var n int
	if err := db2.QueryRow(`SELECT COUNT(*) FROM files WHERE path = '/t/a.jsonl'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Error("the files row survived the migration, so `decide` will report the file unchanged and never re-parse it")
	}
}

// A database already at the current version must be left ALONE. Re-clearing on every
// open would throw away the whole index on each ingest tick.
func TestOpenAtCurrentVersionPreservesData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcripts.db")
	db, err := Open(path, false)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO files (path, project_dir, size, mtime, resume_offset, complete, lines_ok, lines_bad, ingested_at)
	                      VALUES ('/t/a.jsonl', 'p', 100, 1, 100, 1, 2, 0, 1)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	for i := 0; i < 2; i++ {
		again, err := Open(path, false)
		if err != nil {
			t.Fatalf("re-Open %d: %v", i, err)
		}
		var n int
		if err := again.QueryRow(`SELECT COUNT(*) FROM files`).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		if n != 1 {
			t.Fatalf("re-open %d cleared the index (%d rows); Open must be idempotent", i, n)
		}
		if err := again.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	}
}

// A brand-new database reports user_version 0 and has nothing to clear. Treating 0
// as "an old version" would make the migration run on every fresh install.
func TestFreshDatabaseIsNotMigrated(t *testing.T) {
	if err := migrate(nil, 0, false); err != nil {
		t.Errorf("migrate at prior=0 must be a no-op and must not touch the handle: %v", err)
	}
	if err := migrate(nil, SchemaVersion, false); err != nil {
		t.Errorf("migrate at the current version must be a no-op: %v", err)
	}
	if err := migrate(nil, SchemaVersion+1, false); err != nil {
		t.Errorf("migrate on a NEWER database must be a no-op, not a downgrade: %v", err)
	}
}

// user_text is the pg2-oisvb addition and it must be in the canonical table set, so
// that adding or dropping a table stays a deliberate act with a version bump rather
// than a silent schema drift.
func TestUserTextIsCanonical(t *testing.T) {
	_, db := tempDB(t, false)
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='user_text'`,
	).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 1 {
		t.Fatal("user_text is missing from a freshly opened database")
	}
	found := false
	for _, tbl := range CanonicalTables {
		if tbl == "user_text" {
			found = true
		}
	}
	if !found {
		t.Error("user_text exists but is not in CanonicalTables")
	}
}

// The (path, seq) index on tool_calls is not cosmetic. Every Tier 1 query asks "the
// nearest tool call BEFORE line N of this transcript" once per human turn, and
// without the index that is a full scan of an 84k-row table per turn: measured, the
// query did not finish in ten minutes without it and returns in about a second with
// it.
func TestToolCallsHasAPathSeqIndex(t *testing.T) {
	_, db := tempDB(t, false)
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_calls_pathseq'`,
	).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 1 {
		t.Error("idx_calls_pathseq is missing; the Tier 1 candidate queries degrade to full table scans")
	}
	var plan string
	rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT MAX(seq) FROM tool_calls WHERE path = 'x' AND seq < 10`)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var a, b, c int
		var detail string
		if err := rows.Scan(&a, &b, &c, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan += detail + " "
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if !strings.Contains(plan, "idx_calls_pathseq") {
		t.Errorf("the prev-tool-call lookup does not use the index; plan was: %s", plan)
	}
}
