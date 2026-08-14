package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

// The fixture is always built in t.TempDir(); nothing here may touch a real
// database or a real transcript.
func tempDB(t *testing.T, withThinking bool) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcripts.db")
	db, err := Open(path, withThinking)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return path, db
}

func tableNames(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(
		`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`,
	)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// A default database MUST carry exactly the tables the specified DDL declares.
// An accidental extra table is not cosmetic: the schema is the contract other
// audits compare against, and unpicking a stray table later means a migration.
func TestSchemaTableSetIsExactlyCanonical(t *testing.T) {
	_, db := tempDB(t, false)
	got := tableNames(t, db)
	if !reflect.DeepEqual(got, CanonicalTables) {
		t.Fatalf("table set mismatch\n got: %v\nwant: %v", got, CanonicalTables)
	}
}

// T-16 is DEFAULT OFF, and "off" must mean the table is not even created — a
// present-but-empty table would read as a schema deviation to anyone comparing
// against the DDL.
func TestThinkingTableAbsentByDefault(t *testing.T) {
	_, db := tempDB(t, false)
	has, err := HasThinkingTable(db)
	if err != nil {
		t.Fatalf("HasThinkingTable: %v", err)
	}
	if has {
		t.Fatal("thinking table exists without the flag; T-16 is DEFAULT OFF")
	}
}

func TestThinkingTableCreatedOnRequest(t *testing.T) {
	_, db := tempDB(t, true)
	has, err := HasThinkingTable(db)
	if err != nil {
		t.Fatalf("HasThinkingTable: %v", err)
	}
	if !has {
		t.Fatal("thinking table missing with the flag set")
	}
	got := tableNames(t, db)
	want := append(append([]string{}, CanonicalTables...), "thinking")
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("table set mismatch\n got: %v\nwant: %v", got, want)
	}
}

// The DDL's three corrections against the earlier draft are the reason this
// schema exists in its current form, so each is asserted by NAME rather than
// left to a whole-schema string compare that a reformat would break.
func TestMandatoryCorrectedColumnsPresent(t *testing.T) {
	_, db := tempDB(t, false)
	for _, tc := range []struct{ table, column string }{
		{"files", "resume_offset"},      // FIX 1 (T-1a)
		{"files", "complete"},           // FIX 3 (T-15)
		{"tool_results", "content_len"}, // FIX 2 (T-3a)
		{"tool_results", "content"},     // FIX 2 (T-3a): NULLable
	} {
		var n int
		err := db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, tc.table, tc.column,
		).Scan(&n)
		if err != nil {
			t.Fatalf("pragma_table_info(%s): %v", tc.table, err)
		}
		if n != 1 {
			t.Errorf("%s.%s missing", tc.table, tc.column)
		}
	}
	// content MUST be nullable (a successful result stores no body) and
	// content_len MUST NOT be (it is always populated).
	var contentNotNull, lenNotNull int
	if err := db.QueryRow(
		`SELECT "notnull" FROM pragma_table_info('tool_results') WHERE name='content'`,
	).Scan(&contentNotNull); err != nil {
		t.Fatalf("content notnull: %v", err)
	}
	if contentNotNull != 0 {
		t.Error("tool_results.content must be NULLable (T-3a): a successful body is never stored")
	}
	if err := db.QueryRow(
		`SELECT "notnull" FROM pragma_table_info('tool_results') WHERE name='content_len'`,
	).Scan(&lenNotNull); err != nil {
		t.Fatalf("content_len notnull: %v", err)
	}
	if lenNotNull != 1 {
		t.Error("tool_results.content_len must be NOT NULL (T-3a): it is always populated")
	}
}

func TestWALEnabled(t *testing.T) {
	_, db := tempDB(t, false)
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal (T-12: queries must never block ingestion)", mode)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcripts.db")
	for i := range 3 {
		db, err := Open(path, false)
		if err != nil {
			t.Fatalf("Open #%d: %v", i, err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("Close #%d: %v", i, err)
		}
	}
}

// T-13's core guarantee: the query path cannot write. The assertion is on the
// OBSERVABLE outcome — a write is refused and the database file is byte-for-byte
// and timestamp-for-timestamp untouched — not on which pragma achieved it.
func TestOpenReadOnlyRefusesWritesAndLeavesFileUntouched(t *testing.T) {
	path, db := tempDB(t, false)
	if _, err := db.Exec(
		`INSERT INTO files (path, project_dir, size, mtime, lines_ok, lines_bad, ingested_at)
		 VALUES ('/x', 'p', 1, 1, 1, 0, 1)`,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Checkpoint so the row is in the main database file rather than the WAL,
	// making the mtime comparison below meaningful.
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}

	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer func() { _ = ro.Close() }()

	var n int
	if err := ro.QueryRow(`SELECT COUNT(*) FROM files`).Scan(&n); err != nil {
		t.Fatalf("read: %v", err)
	}
	if n != 1 {
		t.Fatalf("read %d rows, want 1", n)
	}
	if _, err := ro.Exec(`DELETE FROM files`); err == nil {
		t.Fatal("a write succeeded on the read-only handle; the query path must not be able to mutate the index")
	}

	// Give the filesystem timestamp room to differ if anything did write.
	time.Sleep(5 * time.Millisecond)
	if err := ro.Close(); err != nil {
		t.Fatalf("close reader: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if after.Size() != before.Size() {
		t.Errorf("database size changed across a query: %d -> %d", before.Size(), after.Size())
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("database mtime changed across a query: %s -> %s", before.ModTime(), after.ModTime())
	}
}

func TestOpenReadOnlyMissingDatabaseIsAnError(t *testing.T) {
	if _, err := OpenReadOnly(filepath.Join(t.TempDir(), "nope.db")); err == nil {
		t.Fatal("expected an error for a missing index")
	}
}

func TestDefaultPathsHonourEnvOverrides(t *testing.T) {
	t.Setenv("PG_CCAUDIT_DB", "/tmp/x/y.db")
	t.Setenv("PG_CCAUDIT_ROOT", "/tmp/x/projects")
	db, err := DefaultDBPath()
	if err != nil {
		t.Fatalf("DefaultDBPath: %v", err)
	}
	if db != "/tmp/x/y.db" {
		t.Errorf("DefaultDBPath = %q", db)
	}
	root, err := DefaultTranscriptRoot()
	if err != nil {
		t.Fatalf("DefaultTranscriptRoot: %v", err)
	}
	if root != "/tmp/x/projects" {
		t.Errorf("DefaultTranscriptRoot = %q", root)
	}
}

func TestDefaultDBPathUsesXDGDataHome(t *testing.T) {
	t.Setenv("PG_CCAUDIT_DB", "")
	t.Setenv("XDG_DATA_HOME", "/tmp/xdg")
	got, err := DefaultDBPath()
	if err != nil {
		t.Fatalf("DefaultDBPath: %v", err)
	}
	want := filepath.Join("/tmp/xdg", "pg-ccaudit", "transcripts.db")
	if got != want {
		t.Errorf("DefaultDBPath = %q, want %q", got, want)
	}
}
