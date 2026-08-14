package asklog

import (
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// TestMain makes every store this package opens non-durable.
//
// Each test creates a fresh DB under t.TempDir(), and each creation costs 11
// fsyncs (WAL conversion + the schema_version create + the single migration
// commit + the close checkpoint). fsync latency is a host-filesystem property
// that spans orders of magnitude: ~50ms per fsync on this repo's ext4 Linux dev
// host, 1.1-3.6s on the loaded QEMU VM that builds monorepod, versus ~0.8us on
// tmpfs. At the slow end that made this 73-test suite take 2m10s of wall clock
// for 0.9s of CPU — pure I/O wait, which presents as an apparent hang whenever
// the caller sets a -timeout below the 10m Go default. See synchronousPragma in
// store.go for the full write-up.
//
// A test that needs real durability semantics must restore the pragma to "" for
// its own duration, e.g.
//
//	defer asklog.SetSynchronousForTests(asklog.SetSynchronousForTests(""))
func TestMain(m *testing.M) {
	SetSynchronousForTests("OFF")
	os.Exit(m.Run())
}

// TestNoProductionCodeDisablesDurability is the tripwire on SetSynchronousForTests.
//
// The seam is exported so package main's test helpers can reach it (they create
// the throwaway databases that dominate the cmd suite's wall clock), and export
// is the only mechanism Go offers for that — a _test.go file is not importable
// and asklog has no sub-package that could see an unexported var. Export is
// therefore load-bearing, and this test is what keeps it from becoming a way to
// silently ship a non-durable ask log: if a non-test file ever calls it, the
// build turns red naming the file.
//
// Scope is the whole module, walked from the module root, because the risk is
// not local to this package — any production file in any package could call it.
func TestNoProductionCodeDisablesDurability(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	const needle = "SetSynchronousForTests"

	var offenders []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// store.go DEFINES it and its doc comment names it; every other
		// non-test mention is a call site and therefore a defect.
		if path == filepath.Join(root, "internal", "asklog", "store.go") {
			return nil
		}
		if strings.Contains(string(body), needle) {
			rel, _ := filepath.Rel(root, path)
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Errorf("%s is a TEST seam but is referenced from non-test file(s) %v: production must leave synchronousPragma empty so every ask-log commit is durably flushed", needle, offenders)
	}
}

func TestNewStore_CreatesDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("database file not created")
	}
}

func TestNewStore_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "subdir", "nested", "test.db")
	s, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("database file not created in nested dir")
	}
}

func TestNewStore_WALMode(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	var mode string
	err = s.db.QueryRow("PRAGMA journal_mode").Scan(&mode)
	if err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
}

func TestNewStore_TableExists(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	var count int
	err = s.db.QueryRow("SELECT COUNT(*) FROM tool_decisions").Scan(&count)
	if err != nil {
		t.Fatalf("table tool_decisions should exist: %v", err)
	}
}

func TestDefaultDBPath(t *testing.T) {
	orig := os.Getenv("XDG_DATA_HOME")
	defer func() { _ = os.Setenv("XDG_DATA_HOME", orig) }()

	_ = os.Setenv("XDG_DATA_HOME", "/custom/data")
	got := DefaultDBPath()
	want := "/custom/data/claude-extended-tool-approver/asks.db"
	if got != want {
		t.Errorf("DefaultDBPath() = %q, want %q", got, want)
	}
}

func TestDefaultDBPath_NoXDG(t *testing.T) {
	orig := os.Getenv("XDG_DATA_HOME")
	defer func() { _ = os.Setenv("XDG_DATA_HOME", orig) }()

	_ = os.Unsetenv("XDG_DATA_HOME")
	got := DefaultDBPath()
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".local", "share", "claude-extended-tool-approver", "asks.db")
	if got != want {
		t.Errorf("DefaultDBPath() = %q, want %q", got, want)
	}
}

func TestNewStore_SchemaVersion(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	var version int
	err = s.db.QueryRow("SELECT version FROM schema_version ORDER BY version DESC LIMIT 1").Scan(&version)
	if err != nil {
		t.Fatalf("query schema_version: %v", err)
	}
	if version != 8 {
		t.Errorf("schema_version = %d, want 8", version)
	}
}

func TestNewStore_Migration2_NewColumns(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	// Verify new columns exist by inserting a row that uses them
	_, err = s.db.Exec(`INSERT INTO tool_decisions
		(session_id, cwd, tool_name, tool_input_hash, tool_input_json, created_at,
		 excluded, excluded_reason, correct_hook_decision, correct_hook_decision_explanation)
		VALUES ('s1', '/tmp', 'Bash', 'hash1', '{}', '2026-01-01T00:00:00Z',
		        1, 'test reason', 'allow', 'test explanation')`)
	if err != nil {
		t.Fatalf("insert with new columns: %v", err)
	}

	var excluded int
	var excludedReason, correctDecision, correctExplanation string
	err = s.db.QueryRow(`SELECT excluded, excluded_reason, correct_hook_decision, correct_hook_decision_explanation
		FROM tool_decisions WHERE session_id = 's1'`).Scan(&excluded, &excludedReason, &correctDecision, &correctExplanation)
	if err != nil {
		t.Fatalf("query new columns: %v", err)
	}
	if excluded != 1 {
		t.Errorf("excluded = %d, want 1", excluded)
	}
	if excludedReason != "test reason" {
		t.Errorf("excluded_reason = %q, want 'test reason'", excludedReason)
	}
	if correctDecision != "allow" {
		t.Errorf("correct_hook_decision = %q, want 'allow'", correctDecision)
	}
	if correctExplanation != "test explanation" {
		t.Errorf("correct_hook_decision_explanation = %q, want 'test explanation'", correctExplanation)
	}
}

func TestNewStore_Migration5_NewColumns(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	// The v5 columns are writable and round-trip verbatim.
	_, err = s.db.Exec(`INSERT INTO tool_decisions
		(session_id, cwd, tool_name, tool_input_hash, tool_input_json, created_at,
		 permission_mode, prompt_id, tool_response, transcript_path)
		VALUES ('v5', '/tmp', 'Bash', 'h1', '{}', '2026-01-01T00:00:00Z',
		        'bypassPermissions', 'p-1', '{"is_error":true}', '/x/t.jsonl')`)
	if err != nil {
		t.Fatalf("insert with v5 columns: %v", err)
	}

	var permMode, promptID, toolResp, transcript string
	err = s.db.QueryRow(`SELECT permission_mode, prompt_id, tool_response, transcript_path
		FROM tool_decisions WHERE session_id = 'v5'`).Scan(&permMode, &promptID, &toolResp, &transcript)
	if err != nil {
		t.Fatalf("query v5 columns: %v", err)
	}
	if permMode != "bypassPermissions" {
		t.Errorf("permission_mode = %q, want bypassPermissions", permMode)
	}
	if promptID != "p-1" {
		t.Errorf("prompt_id = %q, want p-1", promptID)
	}
	if toolResp != `{"is_error":true}` {
		t.Errorf("tool_response = %q, want the raw JSON payload", toolResp)
	}
	if transcript != "/x/t.jsonl" {
		t.Errorf("transcript_path = %q, want /x/t.jsonl", transcript)
	}

	// A row inserted WITHOUT the new columns (an old row) leaves them NULL and readable.
	_, err = s.db.Exec(`INSERT INTO tool_decisions
		(session_id, cwd, tool_name, tool_input_hash, tool_input_json, created_at)
		VALUES ('v5-null', '/tmp', 'Bash', 'h2', '{}', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert without v5 columns: %v", err)
	}
	var pm, pid, tr, tp *string
	err = s.db.QueryRow(`SELECT permission_mode, prompt_id, tool_response, transcript_path
		FROM tool_decisions WHERE session_id = 'v5-null'`).Scan(&pm, &pid, &tr, &tp)
	if err != nil {
		t.Fatalf("query null v5 columns: %v", err)
	}
	if pm != nil || pid != nil || tr != nil || tp != nil {
		t.Errorf("new columns should be NULL for a row that omits them, got %v/%v/%v/%v", pm, pid, tr, tp)
	}
}

// TestNewStore_Migration7_SplitsDeniedByProvenance pins the backfill in BOTH
// directions: the two provenances that were never a decline judgement are
// rewritten (hook Reject -> 'rejected', SessionEnd sweep -> 'unresolved'), and
// a real decline plus every non-'denied' outcome are left completely alone.
func TestNewStore_Migration7_SplitsDeniedByProvenance(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Build a v6 database and seed it with the pre-split shape: every refusal
	// written as 'denied', discriminated only by hook_decision / outcome_notes.
	seed := func() {
		s, err := NewStore(dbPath)
		if err != nil {
			t.Fatalf("NewStore v6: %v", err)
		}
		defer func() { _ = s.Close() }()
		// Force the store back to v6 so migration 7 re-runs over the seed rows.
		if _, err := s.db.Exec("DELETE FROM schema_version WHERE version >= 7"); err != nil {
			t.Fatalf("reset schema_version: %v", err)
		}
		_, err = s.db.Exec(`INSERT INTO tool_decisions
			(session_id, cwd, tool_name, tool_use_id, tool_input_hash, tool_input_json,
			 hook_decision, outcome, outcome_notes, created_at, resolved_at)
			VALUES
			 -- hook Reject: resolved at insert time, hook_decision='deny', no notes.
			 ('s','/tmp','Bash','reject','h1','{}','deny','denied',NULL,
			  '2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
			 -- real decline: the only writer that sets outcome_notes.
			 ('s','/tmp','Bash','decline','h2','{}','abstain','denied',
			  'auto_mode_classifier: blocked','2026-01-01T00:00:00Z','2026-01-01T00:00:30Z'),
			 -- SessionEnd sweep: no notes, hook_decision anything but 'deny'.
			 ('s','/tmp','Bash','sweep-abstain','h3','{}','abstain','denied',NULL,
			  '2026-01-01T00:00:00Z','2026-01-10T00:00:00Z'),
			 ('s','/tmp','Bash','sweep-allow','h4','{}','allow','denied',NULL,
			  '2026-01-01T00:00:00Z','2026-01-10T00:00:00Z'),
			 -- sweep of a built-in ASK row: hook_decision IS NULL.
			 ('s','/tmp','Bash','sweep-null','h5','{}',NULL,'denied',NULL,
			  '2026-01-01T00:00:00Z','2026-01-10T00:00:00Z'),
			 -- untouchable: not 'denied' at all.
			 ('s','/tmp','Bash','approved','h6','{}','ask','approved',NULL,
			  '2026-01-01T00:00:00Z','2026-01-01T00:00:05Z'),
			 ('s','/tmp','Bash','pending','h7','{}','ask','pending',NULL,
			  '2026-01-01T00:00:00Z',NULL)`)
		if err != nil {
			t.Fatalf("seed pre-split rows: %v", err)
		}
	}
	seed()

	// Re-open: migration 7 runs.
	s, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore upgrade to v7: %v", err)
	}
	defer func() { _ = s.Close() }()

	want := map[string]string{
		"reject":        OutcomeRejected,
		"decline":       OutcomeDenied,
		"sweep-abstain": OutcomeUnresolved,
		"sweep-allow":   OutcomeUnresolved,
		"sweep-null":    OutcomeUnresolved,
		"approved":      OutcomeApproved,
		"pending":       OutcomePending,
	}
	for id, wantOutcome := range want {
		var got string
		if err := s.db.QueryRow(
			"SELECT outcome FROM tool_decisions WHERE tool_use_id = ?", id,
		).Scan(&got); err != nil {
			t.Fatalf("read outcome for %s: %v", id, err)
		}
		if got != wantOutcome {
			t.Errorf("%s outcome = %q, want %q", id, got, wantOutcome)
		}
	}

	// Exactly one row may still say 'denied' — the one somebody actually declined.
	var denied int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM tool_decisions WHERE outcome = 'denied'").Scan(&denied)
	if denied != 1 {
		t.Errorf("remaining 'denied' rows = %d, want 1 (only the real decline)", denied)
	}

	// Idempotent: re-running the migration must be a no-op.
	if _, err := s.db.Exec("DELETE FROM schema_version WHERE version >= 7"); err != nil {
		t.Fatalf("reset schema_version: %v", err)
	}
	_ = s.Close()
	s2, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore re-run: %v", err)
	}
	defer func() { _ = s2.Close() }()
	for id, wantOutcome := range want {
		var got string
		if err := s2.db.QueryRow(
			"SELECT outcome FROM tool_decisions WHERE tool_use_id = ?", id,
		).Scan(&got); err != nil {
			t.Fatalf("re-read outcome for %s: %v", id, err)
		}
		if got != wantOutcome {
			t.Errorf("after re-running migration 7, %s outcome = %q, want %q", id, got, wantOutcome)
		}
	}
}

func TestNewStore_Migration2_ExcludedDefaultsToZero(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	_, err = s.db.Exec(`INSERT INTO tool_decisions
		(session_id, cwd, tool_name, tool_input_hash, tool_input_json, created_at)
		VALUES ('s1', '/tmp', 'Bash', 'hash1', '{}', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	var excluded int
	_ = s.db.QueryRow("SELECT excluded FROM tool_decisions WHERE session_id = 's1'").Scan(&excluded)
	if excluded != 0 {
		t.Errorf("excluded default = %d, want 0", excluded)
	}
}

func TestNewStore_Migration2_UpgradeFromV1(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Create a v1 database
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, _ = db.Exec(`CREATE TABLE schema_version (version INTEGER NOT NULL)`)
	_, _ = db.Exec(`INSERT INTO schema_version (version) VALUES (1)`)
	_, _ = db.Exec(`CREATE TABLE tool_decisions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT NOT NULL, cwd TEXT NOT NULL,
		agent_id TEXT, agent_type TEXT,
		tool_name TEXT NOT NULL, tool_use_id TEXT,
		tool_input_hash TEXT NOT NULL, tool_input_json TEXT NOT NULL,
		tool_summary TEXT, hook_decision TEXT, hook_reason TEXT,
		permission_suggestions TEXT,
		outcome TEXT NOT NULL DEFAULT 'pending', outcome_notes TEXT,
		created_at TEXT NOT NULL, resolved_at TEXT
	)`)
	// Insert a pre-existing row
	_, _ = db.Exec(`INSERT INTO tool_decisions
		(session_id, cwd, tool_name, tool_input_hash, tool_input_json, created_at)
		VALUES ('old-sess', '/tmp', 'Bash', 'h1', '{}', '2026-01-01T00:00:00Z')`)
	_ = db.Close()

	// Open with NewStore to trigger migration 2
	s, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore upgrade: %v", err)
	}
	defer func() { _ = s.Close() }()

	// Verify schema version is now 3
	var version int
	_ = s.db.QueryRow("SELECT MAX(version) FROM schema_version").Scan(&version)
	if version != 8 {
		t.Errorf("schema_version = %d, want 8", version)
	}

	// Verify old row has excluded = 0 (default)
	var excluded int
	_ = s.db.QueryRow("SELECT excluded FROM tool_decisions WHERE session_id = 'old-sess'").Scan(&excluded)
	if excluded != 0 {
		t.Errorf("existing row excluded = %d, want 0", excluded)
	}
}

func TestNewStore_UpgradeFromUnversioned(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE tool_decisions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT NOT NULL,
		cwd TEXT NOT NULL,
		agent_id TEXT,
		agent_type TEXT,
		tool_name TEXT NOT NULL,
		tool_use_id TEXT,
		tool_input_hash TEXT NOT NULL,
		tool_input_json TEXT NOT NULL,
		tool_summary TEXT,
		hook_decision TEXT,
		hook_reason TEXT,
		permission_suggestions TEXT,
		outcome TEXT NOT NULL DEFAULT 'pending',
		outcome_notes TEXT,
		created_at TEXT NOT NULL,
		resolved_at TEXT
	)`)
	if err != nil {
		t.Fatalf("create old table: %v", err)
	}
	_ = db.Close()

	s, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore on existing DB: %v", err)
	}
	defer func() { _ = s.Close() }()

	var version int
	err = s.db.QueryRow("SELECT version FROM schema_version ORDER BY version DESC LIMIT 1").Scan(&version)
	if err != nil {
		t.Fatalf("query schema_version: %v", err)
	}
	if version != 8 {
		t.Errorf("schema_version = %d, want 8", version)
	}
}

func TestNewStore_IdempotentMigration(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	s1, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("first NewStore: %v", err)
	}
	_ = s1.Close()

	s2, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("second NewStore: %v", err)
	}
	defer func() { _ = s2.Close() }()

	var count int
	_ = s2.db.QueryRow("SELECT COUNT(*) FROM schema_version").Scan(&count)
	if count != 8 {
		t.Errorf("schema_version rows = %d, want 8", count)
	}
}

func setupTestDB(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Insert test rows directly
	for _, q := range []string{
		`INSERT INTO tool_decisions (id, session_id, cwd, tool_name, tool_input_hash, tool_input_json, tool_summary, hook_decision, hook_reason, outcome, created_at)
		 VALUES (1, 'sess1', '/tmp', 'Bash', 'h1', '{"command":"git log"}', 'git log', 'allow', 'git: read-only', 'approved', '2026-03-01T00:00:00Z')`,
		`INSERT INTO tool_decisions (id, session_id, cwd, tool_name, tool_input_hash, tool_input_json, tool_summary, hook_decision, hook_reason, outcome, created_at)
		 VALUES (2, 'sess1', '/tmp', 'Bash', 'h2', '{"command":"rm -rf /"}', 'rm -rf /', 'deny', 'dangerous', 'denied', '2026-03-01T00:00:00Z')`,
		`INSERT INTO tool_decisions (id, session_id, cwd, tool_name, tool_input_hash, tool_input_json, tool_summary, hook_decision, hook_reason, outcome, created_at, excluded)
		 VALUES (3, 'sess1', '/tmp', 'Bash', 'h3', '{"command":"ls"}', 'ls', 'allow', 'safecmd', 'approved', '2026-03-01T00:00:00Z', 1)`,
	} {
		if _, err := store.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func TestQueryRowsByIDs(t *testing.T) {
	store := setupTestDB(t)

	rows, err := store.QueryRowsByIDs([]int{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0].ID != 1 || rows[1].ID != 2 {
		t.Errorf("got IDs %d,%d, want 1,2", rows[0].ID, rows[1].ID)
	}
	if rows[0].ToolSummary != "git log" {
		t.Errorf("row 1 tool_summary = %q, want 'git log'", rows[0].ToolSummary)
	}
	if rows[0].HookReason != "git: read-only" {
		t.Errorf("row 1 hook_reason = %q, want 'git: read-only'", rows[0].HookReason)
	}
}

func TestQueryRowsByIDs_IncludesExcluded(t *testing.T) {
	store := setupTestDB(t)

	rows, err := store.QueryRowsByIDs([]int{3})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 (excluded rows should be returned by show)", len(rows))
	}
}

func TestQueryRowsByIDs_Empty(t *testing.T) {
	store := setupTestDB(t)

	rows, err := store.QueryRowsByIDs([]int{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("got %d rows, want 0 for empty input", len(rows))
	}
}

func TestQueryRowsByIDs_MissingIDs(t *testing.T) {
	store := setupTestDB(t)

	rows, err := store.QueryRowsByIDs([]int{1, 999})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 (missing ID 999 skipped)", len(rows))
	}
}

func TestStore_ForeignKeysEnabled(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	var fkEnabled int
	err = s.db.QueryRow("PRAGMA foreign_keys").Scan(&fkEnabled)
	if err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if fkEnabled != 1 {
		t.Errorf("foreign_keys = %d, want 1 (enabled)", fkEnabled)
	}
}

func TestStore_TraceTableExists(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	var name string
	err = s.db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='decision_trace_entries'").Scan(&name)
	if err != nil {
		t.Fatalf("decision_trace_entries table should exist: %v", err)
	}
}

func TestStore_CascadeDelete(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	res, err := s.db.Exec(`INSERT INTO tool_decisions
		(session_id, cwd, tool_name, tool_input_hash, tool_input_json, outcome, created_at)
		VALUES ('sess1', '/tmp', 'Bash', 'h1', '{}', 'pending', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert tool_decisions: %v", err)
	}
	decID, _ := res.LastInsertId()

	for i := 1; i <= 3; i++ {
		_, err := s.db.Exec(`INSERT INTO decision_trace_entries
			(tool_decision_id, rule_order, rule_name, decision, reason)
			VALUES (?, ?, ?, 'abstain', 'test')`, decID, i, fmt.Sprintf("rule-%d", i))
		if err != nil {
			t.Fatalf("insert trace entry %d: %v", i, err)
		}
	}

	var count int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM decision_trace_entries WHERE tool_decision_id = ?", decID).Scan(&count)
	if count != 3 {
		t.Fatalf("trace entries = %d, want 3", count)
	}

	_, err = s.db.Exec("DELETE FROM tool_decisions WHERE id = ?", decID)
	if err != nil {
		t.Fatalf("delete tool_decisions: %v", err)
	}

	_ = s.db.QueryRow("SELECT COUNT(*) FROM decision_trace_entries WHERE tool_decision_id = ?", decID).Scan(&count)
	if count != 0 {
		t.Errorf("trace entries after cascade = %d, want 0", count)
	}
}

func TestStore_QueryTraceByDecisionID(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	res, err := s.db.Exec(`INSERT INTO tool_decisions
		(session_id, cwd, tool_name, tool_input_hash, tool_input_json, outcome, created_at)
		VALUES ('sess1', '/tmp', 'Bash', 'h1', '{}', 'pending', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	decID, _ := res.LastInsertId()

	_, _ = s.db.Exec(`INSERT INTO decision_trace_entries (tool_decision_id, rule_order, rule_name, decision, reason) VALUES (?, 1, 'envvars', 'abstain', 'not relevant')`, decID)
	_, _ = s.db.Exec(`INSERT INTO decision_trace_entries (tool_decision_id, rule_order, rule_name, decision, reason) VALUES (?, 2, 'git', 'allow', 'safe command')`, decID)

	entries, err := s.QueryTraceByDecisionID(int(decID))
	if err != nil {
		t.Fatalf("QueryTraceByDecisionID: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].RuleName != "envvars" || entries[0].Decision != "abstain" {
		t.Errorf("entry[0] = %+v, want envvars/abstain", entries[0])
	}
	if entries[1].RuleName != "git" || entries[1].Decision != "allow" {
		t.Errorf("entry[1] = %+v, want git/allow", entries[1])
	}
}

func sp(s string) *string { return &s }

// TestApprovalSource_Classification exercises the ordered approval-source
// derivation. approval_source is the approval-MECHANISM axis only
// {unknown,bypass,auto,settings,hook,user}; subagent is NOT in this axis
// (agent_type stays a separate column, see the segmentation test below).
func TestApprovalSource_Classification(t *testing.T) {
	allow := sp("allow")
	tests := []struct {
		name         string
		permission   *string
		promptID     *string
		hookDecision *string
		want         string
	}{
		{"NULL permission_mode -> unknown (all pre-migration rows)", nil, nil, allow, "unknown"},
		{"bypassPermissions -> bypass", sp("bypassPermissions"), nil, nil, "bypass"},
		{"auto -> auto", sp("auto"), nil, nil, "auto"},
		{"dontAsk -> auto", sp("dontAsk"), nil, nil, "auto"},
		{"acceptEdits falls through: prompt present -> user", sp("acceptEdits"), sp("p1"), nil, "user"},
		{"acceptEdits falls through: no prompt + not approved -> settings", sp("acceptEdits"), nil, sp("deny"), "settings"},
		{"acceptEdits falls through: no prompt + CETA approved -> hook", sp("acceptEdits"), nil, allow, "hook"},
		{"default: prompt present -> user", sp("default"), sp("p2"), nil, "user"},
		{"default: no prompt + CETA approved -> hook", sp("default"), nil, allow, "hook"},
		{"default: no prompt + CETA abstained -> settings", sp("default"), nil, sp("abstain"), "settings"},
		{"empty prompt_id string counts as no-prompt", sp("default"), sp(""), allow, "hook"},
		// approval_source classifies CONTEXT, not outcome: a denied/pending row
		// still gets its mechanism bucket. An auto-mode DENIAL is the primary
		// false-denial calibration signal for pg2-okd13.2, so it MUST bucket as auto.
		{"auto-mode denial still buckets as auto", sp("auto"), nil, sp("deny"), "auto"},
		{"mode wins over prompt: bypass with a prompt_id still bypass", sp("bypassPermissions"), sp("p3"), allow, "bypass"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ApprovalSource(tt.permission, tt.promptID, tt.hookDecision); got != tt.want {
				t.Errorf("ApprovalSource() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestQueryRows_ExposesRawFieldsAndAgentTypeSegmentation verifies QueryRows
// exposes all four raw fields (permission_mode, agent_type, outcome_notes,
// tool_response) plus prompt_id, and that agent_type is a SEPARATE filterable
// axis from approval_source — subagent segmentation is agent_type IS NOT NULL,
// crossed with (not merged into) the approval_source mechanism enum.
func TestQueryRows_ExposesRawFieldsAndAgentTypeSegmentation(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	// A subagent row (agent_type set) auto-approved under bypassPermissions.
	_, err = s.db.Exec(`INSERT INTO tool_decisions
		(id, session_id, cwd, tool_name, tool_input_hash, tool_input_json, outcome, created_at,
		 permission_mode, agent_type, outcome_notes, tool_response, prompt_id)
		VALUES (1,'s','/tmp','Bash','h1','{}','approved','2026-01-01T00:00:00Z',
		        'bypassPermissions','Explore','note-1','{"is_error":false}',NULL)`)
	if err != nil {
		t.Fatalf("insert subagent row: %v", err)
	}
	// A main-agent row (agent_type NULL) DENIED under auto mode.
	_, err = s.db.Exec(`INSERT INTO tool_decisions
		(id, session_id, cwd, tool_name, tool_input_hash, tool_input_json, outcome, created_at,
		 permission_mode, agent_type, outcome_notes, tool_response, prompt_id)
		VALUES (2,'s','/tmp','Bash','h2','{}','denied','2026-01-01T00:00:00Z',
		        'auto',NULL,'auto_mode_classifier: x',NULL,NULL)`)
	if err != nil {
		t.Fatalf("insert main-agent row: %v", err)
	}

	rows, err := s.QueryRows("")
	if err != nil {
		t.Fatalf("QueryRows: %v", err)
	}
	byID := map[int]DecisionRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}

	sub := byID[1]
	if sub.AgentType == nil || *sub.AgentType != "Explore" {
		t.Errorf("row1 AgentType = %v, want Explore", sub.AgentType)
	}
	if sub.PermissionMode == nil || *sub.PermissionMode != "bypassPermissions" {
		t.Errorf("row1 permission_mode = %v, want bypassPermissions", sub.PermissionMode)
	}
	if sub.OutcomeNotes == nil || *sub.OutcomeNotes != "note-1" {
		t.Errorf("row1 outcome_notes = %v, want note-1", sub.OutcomeNotes)
	}
	if sub.ToolResponse == nil || *sub.ToolResponse != `{"is_error":false}` {
		t.Errorf("row1 tool_response = %v, want the raw payload", sub.ToolResponse)
	}
	if got := ApprovalSource(sub.PermissionMode, sub.PromptID, sub.HookDecision); got != "bypass" {
		t.Errorf("row1 approval_source = %q, want bypass", got)
	}

	main := byID[2]
	if main.AgentType != nil {
		t.Errorf("row2 AgentType = %v, want nil (main agent)", *main.AgentType)
	}
	if got := ApprovalSource(main.PermissionMode, main.PromptID, main.HookDecision); got != "auto" {
		t.Errorf("row2 approval_source = %q, want auto (denied row still bucketed)", got)
	}

	// Cross the two axes: subagent segmentation is agent_type IS NOT NULL,
	// independent of the approval_source of each row.
	subagentCount := 0
	for _, r := range rows {
		if r.AgentType != nil {
			subagentCount++
		}
	}
	if subagentCount != 1 {
		t.Errorf("agent_type IS NOT NULL rows = %d, want 1", subagentCount)
	}
}
