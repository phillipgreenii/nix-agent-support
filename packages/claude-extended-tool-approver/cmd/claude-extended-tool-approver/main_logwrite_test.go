package main

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func logTestInput(id string) *hookio.HookInput {
	return &hookio.HookInput{
		SessionID: "s", CWD: "/tmp", ToolName: "Bash", ToolUseID: id,
		ToolInput: json.RawMessage(`{"command":"date"}`),
	}
}

func rowCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tool_decisions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A database whose write lock is held by another connection (the real SQLite
// lock, not a mock) must not delay the hook past its write budget, and the
// decision must be spooled and later replayed rather than lost.
func TestLogPreToolDecision_LockedDatabaseSpoolsThenDrains(t *testing.T) {
	old := askLogWriteBudget
	askLogWriteBudget = 300 * time.Millisecond
	defer func() { askLogWriteBudget = old }()

	dbPath := filepath.Join(t.TempDir(), "asks.db")
	store, err := asklog.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	locker, err := sql.Open("sqlite", dbPath+"?_busy_timeout=100")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locker.Close() }()
	conn, err := locker.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	logPreToolDecision(store, dbPath, "", logTestInput("tu1"), hookio.RuleResult{Decision: hookio.Approve}, false)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("logging took %v with a locked database; budget not enforced", d)
	}
	if got := asklog.SpoolLen(dbPath); got != 1 {
		t.Fatalf("SpoolLen = %d, want 1 (miss must be recorded)", got)
	}

	if _, err := conn.ExecContext(t.Context(), `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	askLogWriteBudget = 5 * time.Second
	logPreToolDecision(store, dbPath, "", logTestInput("tu2"), hookio.RuleResult{Decision: hookio.Approve}, false)
	if got := asklog.SpoolLen(dbPath); got != 0 {
		t.Fatalf("SpoolLen = %d after unlocked run, want 0", got)
	}
	ro, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ro.Close() }()
	if n := rowCount(t, ro); n != 2 {
		t.Fatalf("rows = %d, want 2 (tu1 replayed, tu2 direct)", n)
	}
}

// With no store (open failed or timed out) the decision goes to the spool.
func TestLogPreToolDecision_NoStoreSpools(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "asks.db")
	logPreToolDecision(nil, dbPath, "open: test", logTestInput("tu1"), hookio.RuleResult{Decision: hookio.Ask}, true)
	if got := asklog.SpoolLen(dbPath); got != 1 {
		t.Fatalf("SpoolLen = %d, want 1", got)
	}
}

// Normal path: a row lands directly, nothing is spooled.
func TestLogPreToolDecision_NormalPath(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "asks.db")
	store, err := asklog.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	logPreToolDecision(store, dbPath, "", logTestInput("tu1"), hookio.RuleResult{Decision: hookio.Approve}, false)
	if asklog.SpoolLen(dbPath) != 0 {
		t.Fatal("spooled on the normal path")
	}
	var n int
	if err := sqlQueryRow(t, dbPath, &n); err != nil || n != 1 {
		t.Fatalf("rows=%d err=%v", n, err)
	}
}

func sqlQueryRow(t *testing.T, dbPath string, n *int) error {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	return db.QueryRow(`SELECT COUNT(*) FROM tool_decisions`).Scan(n)
}
