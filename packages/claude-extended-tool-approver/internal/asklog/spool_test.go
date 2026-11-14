package asklog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func spoolInputFor(id string) *hookio.HookInput {
	return &hookio.HookInput{
		SessionID: "s1", CWD: "/tmp", ToolName: "Bash", ToolUseID: id,
		ToolInput: json.RawMessage(`{"command":"date"}`),
	}
}

func spoolCountRows(t *testing.T, s *Store, where string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM tool_decisions WHERE `+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSpool_DrainReplaysWithOriginalTimeAndTrace(t *testing.T) {
	db := filepath.Join(t.TempDir(), "asks.db")
	sandbox := true
	res := hookio.RuleResult{
		Decision: hookio.Approve, Reason: "r",
		Trace: []hookio.TraceEntry{{RuleName: "rule-a", Decision: hookio.Approve, Reason: "x"}},
	}
	if err := AppendToSpool(db, spoolInputFor("tu1"), res, &sandbox, "test"); err != nil {
		t.Fatal(err)
	}
	if got := SpoolLen(db); got != 1 {
		t.Fatalf("SpoolLen = %d, want 1", got)
	}
	s, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	time.Sleep(1100 * time.Millisecond) // RFC3339 has 1 s resolution
	n, err := s.DrainSpool(db, 10)
	if err != nil || n != 1 {
		t.Fatalf("drain = %d, %v", n, err)
	}
	var created string
	var sb int
	var traces int
	if err := s.db.QueryRowContext(context.Background(), `SELECT created_at, sandbox_enabled, (SELECT COUNT(*) FROM decision_trace_entries) FROM tool_decisions WHERE tool_use_id='tu1'`).Scan(&created, &sb, &traces); err != nil {
		t.Fatal(err)
	}
	if created == nowISO() || sb != 1 || traces != 1 {
		t.Fatalf("created=%s (now %s) sandbox=%d traces=%d", created, nowISO(), sb, traces)
	}
	if SpoolLen(db) != 0 {
		t.Fatal("spool not emptied")
	}
}

func TestSpool_DrainDedupesAndKeepsOverflow(t *testing.T) {
	db := filepath.Join(t.TempDir(), "asks.db")
	s, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	res := hookio.RuleResult{Decision: hookio.Ask}
	// tu1 already committed (an abandoned write that landed anyway)
	if err := RecordPreToolDecision(s, spoolInputFor("tu1"), res); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"tu1", "tu2", "tu3"} {
		if err := AppendToSpool(db, spoolInputFor(id), res, nil, "test"); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.DrainSpool(db, 1) // max 1: tu1 is skipped (dup), tu2 inserted, tu3 kept
	if err != nil || n != 1 {
		t.Fatalf("drain = %d, %v", n, err)
	}
	if c := spoolCountRows(t, s, `tool_use_id='tu1'`); c != 1 {
		t.Fatalf("tu1 rows = %d, want 1", c)
	}
	if SpoolLen(db) != 1 {
		t.Fatalf("SpoolLen = %d, want 1 (overflow kept)", SpoolLen(db))
	}
	if n, err = s.DrainSpool(db, 10); err != nil || n != 1 {
		t.Fatalf("second drain = %d, %v", n, err)
	}
	if c := spoolCountRows(t, s, `1=1`); c != 3 {
		t.Fatalf("rows = %d, want 3", c)
	}
}

func TestRunWithin_TimesOutAndPassesErrors(t *testing.T) {
	start := time.Now()
	err := RunWithin(50*time.Millisecond, func() error { time.Sleep(2 * time.Second); return nil })
	if !errors.Is(err, ErrBudgetExceeded) || time.Since(start) > time.Second {
		t.Fatalf("err=%v elapsed=%v", err, time.Since(start))
	}
	want := errors.New("boom")
	if got := RunWithin(time.Second, func() error { return want }); got != want {
		t.Fatalf("got %v", got)
	}
}

func TestOpenWithin_NormalPath(t *testing.T) {
	s, err := OpenWithin(filepath.Join(t.TempDir(), "asks.db"), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
}

// A write that misses its budget while another connection holds the database
// write lock must not commit once the lock is released: the caller spools that
// record, so a late commit would duplicate the row. Uses the real SQLite lock.
func TestRunWithinCtx_BudgetMissedWriteNeverCommits(t *testing.T) {
	old := ctxGrace
	ctxGrace = 30 * time.Second // wait for the cancelled write to stop; returns as soon as it does
	defer func() { ctxGrace = old }()

	db := filepath.Join(t.TempDir(), "asks.db")
	s, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	locker, err := sql.Open("sqlite", db+"?_busy_timeout=100")
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

	err = RunWithinCtx(100*time.Millisecond, func(ctx context.Context) error {
		return RecordPreToolDecisionCtx(ctx, s, spoolInputFor("tu1"), hookio.RuleResult{Decision: hookio.Approve})
	})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("err = %v, want ErrBudgetExceeded", err)
	}
	if _, err := conn.ExecContext(t.Context(), `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	// The write's own busy timeout (3 s) has not elapsed; were it still
	// waiting it would take the lock now. Give it the chance, then count.
	time.Sleep(500 * time.Millisecond)
	if n := spoolCountRows(t, s, `tool_use_id = ?`, "tu1"); n != 0 {
		t.Fatalf("budget-missed write committed %d row(s) after the lock was released", n)
	}
}
