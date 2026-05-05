package asklog

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestNewStore_CreatesDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()

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
	defer s.Close()

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
	defer s.Close()

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
	defer s.Close()

	var count int
	err = s.db.QueryRow("SELECT COUNT(*) FROM tool_decisions").Scan(&count)
	if err != nil {
		t.Fatalf("table tool_decisions should exist: %v", err)
	}
}

func TestDefaultDBPath(t *testing.T) {
	orig := os.Getenv("XDG_DATA_HOME")
	defer os.Setenv("XDG_DATA_HOME", orig)

	os.Setenv("XDG_DATA_HOME", "/custom/data")
	got := DefaultDBPath()
	want := "/custom/data/claude-extended-tool-approver/asks.db"
	if got != want {
		t.Errorf("DefaultDBPath() = %q, want %q", got, want)
	}
}

func TestDefaultDBPath_NoXDG(t *testing.T) {
	orig := os.Getenv("XDG_DATA_HOME")
	defer os.Setenv("XDG_DATA_HOME", orig)

	os.Unsetenv("XDG_DATA_HOME")
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
	defer s.Close()

	var version int
	err = s.db.QueryRow("SELECT version FROM schema_version ORDER BY version DESC LIMIT 1").Scan(&version)
	if err != nil {
		t.Fatalf("query schema_version: %v", err)
	}
	if version != 4 {
		t.Errorf("schema_version = %d, want 4", version)
	}
}

func TestNewStore_Migration2_NewColumns(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()

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

func TestNewStore_Migration2_ExcludedDefaultsToZero(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()

	_, err = s.db.Exec(`INSERT INTO tool_decisions
		(session_id, cwd, tool_name, tool_input_hash, tool_input_json, created_at)
		VALUES ('s1', '/tmp', 'Bash', 'hash1', '{}', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	var excluded int
	s.db.QueryRow("SELECT excluded FROM tool_decisions WHERE session_id = 's1'").Scan(&excluded)
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
	db.Close()

	// Open with NewStore to trigger migration 2
	s, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore upgrade: %v", err)
	}
	defer s.Close()

	// Verify schema version is now 3
	var version int
	s.db.QueryRow("SELECT MAX(version) FROM schema_version").Scan(&version)
	if version != 4 {
		t.Errorf("schema_version = %d, want 4", version)
	}

	// Verify old row has excluded = 0 (default)
	var excluded int
	s.db.QueryRow("SELECT excluded FROM tool_decisions WHERE session_id = 'old-sess'").Scan(&excluded)
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
	db.Close()

	s, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore on existing DB: %v", err)
	}
	defer s.Close()

	var version int
	err = s.db.QueryRow("SELECT version FROM schema_version ORDER BY version DESC LIMIT 1").Scan(&version)
	if err != nil {
		t.Fatalf("query schema_version: %v", err)
	}
	if version != 4 {
		t.Errorf("schema_version = %d, want 4", version)
	}
}

func TestNewStore_IdempotentMigration(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	s1, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("first NewStore: %v", err)
	}
	s1.Close()

	s2, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("second NewStore: %v", err)
	}
	defer s2.Close()

	var count int
	s2.db.QueryRow("SELECT COUNT(*) FROM schema_version").Scan(&count)
	if count != 4 {
		t.Errorf("schema_version rows = %d, want 4", count)
	}
}

func setupTestDB(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

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
	defer s.Close()

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
	defer s.Close()

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
	defer s.Close()

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
	s.db.QueryRow("SELECT COUNT(*) FROM decision_trace_entries WHERE tool_decision_id = ?", decID).Scan(&count)
	if count != 3 {
		t.Fatalf("trace entries = %d, want 3", count)
	}

	_, err = s.db.Exec("DELETE FROM tool_decisions WHERE id = ?", decID)
	if err != nil {
		t.Fatalf("delete tool_decisions: %v", err)
	}

	s.db.QueryRow("SELECT COUNT(*) FROM decision_trace_entries WHERE tool_decision_id = ?", decID).Scan(&count)
	if count != 0 {
		t.Errorf("trace entries after cascade = %d, want 0", count)
	}
}

func TestStore_QueryTraceByDecisionID(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()

	res, err := s.db.Exec(`INSERT INTO tool_decisions
		(session_id, cwd, tool_name, tool_input_hash, tool_input_json, outcome, created_at)
		VALUES ('sess1', '/tmp', 'Bash', 'h1', '{}', 'pending', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	decID, _ := res.LastInsertId()

	s.db.Exec(`INSERT INTO decision_trace_entries (tool_decision_id, rule_order, rule_name, decision, reason) VALUES (?, 1, 'envvars', 'abstain', 'not relevant')`, decID)
	s.db.Exec(`INSERT INTO decision_trace_entries (tool_decision_id, rule_order, rule_name, decision, reason) VALUES (?, 2, 'git', 'allow', 'safe command')`, decID)

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
