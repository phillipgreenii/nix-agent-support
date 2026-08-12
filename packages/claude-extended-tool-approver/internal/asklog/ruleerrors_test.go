package asklog

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/metrics"
)

func ruleErrorStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRecordRuleErrors_PersistsOneRowPerRule(t *testing.T) {
	s := ruleErrorStore(t)
	input := &hookio.HookInput{SessionID: "sess-1", CWD: "/repo", ToolName: "Bash"}

	c := metrics.NewRuleErrors()
	c.Record("gh", errors.New("resolve current branch: timeout"))
	c.Record("gh", errors.New("resolve current branch: timeout again"))
	c.Record("primary-commit", errors.New("resolver failed"))

	if err := RecordRuleErrors(s, input, c.Snapshot()); err != nil {
		t.Fatalf("RecordRuleErrors: %v", err)
	}

	rows, err := s.db.Query(`SELECT rule_name, error_count, error_sample, session_id, cwd, tool_name
		FROM rule_errors ORDER BY rule_name`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer func() { _ = rows.Close() }()

	type row struct {
		rule, sample, session, cwd, tool string
		count                            int
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.rule, &r.count, &r.sample, &r.session, &r.cwd, &r.tool); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("rule_errors has %d rows, want 2 (one per failing rule): %+v", len(got), got)
	}
	if got[0].rule != "gh" || got[0].count != 2 {
		t.Errorf("row 0 = %+v, want rule gh with count 2", got[0])
	}
	if got[0].sample == "" {
		t.Error("row 0 has no error_sample — the sample is what makes a systematic failure diagnosable, " +
			"not merely countable")
	}
	if got[0].session != "sess-1" || got[0].cwd != "/repo" || got[0].tool != "Bash" {
		t.Errorf("row 0 call context = %+v, want the call's session/cwd/tool", got[0])
	}
	if got[1].rule != "primary-commit" || got[1].count != 1 {
		t.Errorf("row 1 = %+v, want rule primary-commit with count 1", got[1])
	}
}

// TestRecordRuleErrors_EmptySnapshotWritesNothing pins the reading convention: a row
// MEANS "this rule failed", so the absence of rows is the evidence a rule is healthy.
// Zero-count rows would bury that signal under one row per rule per tool call.
func TestRecordRuleErrors_EmptySnapshotWritesNothing(t *testing.T) {
	s := ruleErrorStore(t)
	input := &hookio.HookInput{SessionID: "sess-1", CWD: "/repo", ToolName: "Bash"}

	if err := RecordRuleErrors(s, input, metrics.NewRuleErrors().Snapshot()); err != nil {
		t.Fatalf("RecordRuleErrors: %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM rule_errors`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("rule_errors has %d rows for an empty window, want 0", n)
	}
}

// TestRecordRuleErrors_NilStoreIsANoOp: the hook runs storeless when the ask-log
// cannot be opened, and losing an observability row must never be able to panic the
// decision path.
func TestRecordRuleErrors_NilStoreIsANoOp(t *testing.T) {
	c := metrics.NewRuleErrors()
	c.Record("gh", errors.New("x"))
	if err := RecordRuleErrors(nil, &hookio.HookInput{}, c.Snapshot()); err != nil {
		t.Errorf("RecordRuleErrors(nil store) = %v, want nil", err)
	}
}

// TestRuleErrorsSurvivesReopen is the point of the table: the hook is one process per
// tool call, so "systematically failing" is only observable if the counts OUTLIVE the
// process. Reopening the same file and finding both windows is that property.
func TestRuleErrorsSurvivesReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	input := &hookio.HookInput{SessionID: "sess-1", CWD: "/repo", ToolName: "Bash"}

	for i := 0; i < 2; i++ {
		s, err := NewStore(dbPath)
		if err != nil {
			t.Fatalf("NewStore (pass %d): %v", i, err)
		}
		c := metrics.NewRuleErrors()
		c.Record("gh", errors.New("timeout"))
		if err := RecordRuleErrors(s, input, c.Snapshot()); err != nil {
			t.Fatalf("RecordRuleErrors (pass %d): %v", i, err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close (pass %d): %v", i, err)
		}
	}

	s, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore (verify): %v", err)
	}
	defer func() { _ = s.Close() }()
	var total int
	if err := s.db.QueryRow(`SELECT SUM(error_count) FROM rule_errors WHERE rule_name = 'gh'`).Scan(&total); err != nil {
		t.Fatalf("sum: %v", err)
	}
	if total != 2 {
		t.Errorf("gh failures across two processes = %d, want 2 — an in-process counter alone cannot "+
			"satisfy ADR 0043's 'detectable' requirement", total)
	}
}
