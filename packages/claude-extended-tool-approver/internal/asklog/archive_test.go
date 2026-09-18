package asklog

import (
	"path/filepath"
	"testing"
)

// seedArchiveRow inserts one tool_decisions row with the given id/created_at
// and, optionally, a ground-truth annotation — the minimum shape the
// archiving tests below need. All of these tests build a SYNTHETIC,
// throwaway database under t.TempDir() (never the real corpus — see
// bd pg2-riwdh's safety constraint).
func seedArchiveRow(t *testing.T, s *Store, id int, createdAt string, correctDecision, correctExplanation *string) {
	t.Helper()
	var err error
	if correctDecision == nil {
		_, err = s.db.Exec(`INSERT INTO tool_decisions
			(id, session_id, cwd, tool_name, tool_input_hash, tool_input_json, tool_summary,
			 hook_decision, outcome, created_at)
			VALUES (?, 'sess1', '/tmp', 'Bash', ?, '{"command":"ls"}', 'ls', 'allow', 'approved', ?)`,
			id, "h"+createdAt, createdAt)
	} else {
		_, err = s.db.Exec(`INSERT INTO tool_decisions
			(id, session_id, cwd, tool_name, tool_input_hash, tool_input_json, tool_summary,
			 hook_decision, outcome, created_at, correct_hook_decision, correct_hook_decision_explanation)
			VALUES (?, 'sess1', '/tmp', 'Bash', ?, '{"command":"ls"}', 'ls', 'allow', 'approved', ?, ?, ?)`,
			id, "h"+createdAt, createdAt, *correctDecision, *correctExplanation)
	}
	if err != nil {
		t.Fatalf("seed row id=%d: %v", id, err)
	}
}

// TestQueryRowsBefore_FiltersByThreshold pins the core selection: rows with
// created_at strictly before the threshold come back, rows at or after it
// do not — the archiving ceremony must never sample or delete a row that is
// not actually old enough.
func TestQueryRowsBefore_FiltersByThreshold(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	seedArchiveRow(t, s, 1, "2026-01-01T00:00:00Z", nil, nil)
	seedArchiveRow(t, s, 2, "2026-06-01T00:00:00Z", nil, nil)
	seedArchiveRow(t, s, 3, "2026-09-01T00:00:00Z", nil, nil) // exactly the threshold — must NOT be included

	rows, err := s.QueryRowsBefore("2026-09-01T00:00:00Z")
	if err != nil {
		t.Fatalf("QueryRowsBefore: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (ids 1 and 2)", len(rows))
	}
	if rows[0].ID != 1 || rows[1].ID != 2 {
		t.Errorf("got IDs %d,%d, want 1,2 (ordered by id)", rows[0].ID, rows[1].ID)
	}
}

// TestQueryRowsBefore_PreservesCorrectHookDecision is the AC-pinned
// ground-truth-survival check: a row a human annotated via
// set-correct-decision must come back with BOTH the decision and its
// explanation intact, not summarized away or dropped — this is the exact
// column QueryRowsByIDs/QueryRows exist alongside but each expose
// differently, and ArchiveRow's whole reason to exist is not losing it.
func TestQueryRowsBefore_PreservesCorrectHookDecision(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	decision := "deny"
	explanation := "this should have been refused"
	seedArchiveRow(t, s, 1, "2026-01-01T00:00:00Z", &decision, &explanation)
	seedArchiveRow(t, s, 2, "2026-01-01T00:00:00Z", nil, nil)

	rows, err := s.QueryRowsBefore("2026-12-01T00:00:00Z")
	if err != nil {
		t.Fatalf("QueryRowsBefore: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}

	annotated := rows[0]
	if annotated.CorrectHookDecision == nil || *annotated.CorrectHookDecision != decision {
		t.Errorf("row 1 CorrectHookDecision = %v, want %q", annotated.CorrectHookDecision, decision)
	}
	if annotated.CorrectHookDecisionExplanation == nil || *annotated.CorrectHookDecisionExplanation != explanation {
		t.Errorf("row 1 CorrectHookDecisionExplanation = %v, want %q", annotated.CorrectHookDecisionExplanation, explanation)
	}

	unannotated := rows[1]
	if unannotated.CorrectHookDecision != nil {
		t.Errorf("row 2 CorrectHookDecision = %v, want nil (never annotated)", unannotated.CorrectHookDecision)
	}
}

// TestDeleteRowsBefore_OnlyDeletesOldRows is the delete-side counterpart of
// TestQueryRowsBefore_FiltersByThreshold: the same threshold semantics must
// hold for the destructive step, and DeleteRowsBefore's returned count must
// match exactly what was removed.
func TestDeleteRowsBefore_OnlyDeletesOldRows(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	seedArchiveRow(t, s, 1, "2026-01-01T00:00:00Z", nil, nil)
	seedArchiveRow(t, s, 2, "2026-06-01T00:00:00Z", nil, nil)
	seedArchiveRow(t, s, 3, "2026-09-01T00:00:00Z", nil, nil)

	deleted, err := s.DeleteRowsBefore("2026-09-01T00:00:00Z")
	if err != nil {
		t.Fatalf("DeleteRowsBefore: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted = %d, want 2", deleted)
	}

	var remaining int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM tool_decisions").Scan(&remaining); err != nil {
		t.Fatalf("count remaining: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("remaining rows = %d, want 1 (only id 3, the one at/after the threshold)", remaining)
	}
	var remainingID int
	if err := s.db.QueryRow("SELECT id FROM tool_decisions").Scan(&remainingID); err != nil {
		t.Fatalf("select remaining id: %v", err)
	}
	if remainingID != 3 {
		t.Errorf("remaining id = %d, want 3", remainingID)
	}
}

// TestDeleteRowsBefore_CascadesTraceEntries proves the single DELETE is
// genuinely sufficient: a decision_trace_entries row anchored to a deleted
// tool_decisions row must disappear too (via the ON DELETE CASCADE foreign
// key — see DeleteRowsBefore's doc comment), and a trace row anchored to a
// SURVIVING decision must not be touched.
func TestDeleteRowsBefore_CascadesTraceEntries(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	seedArchiveRow(t, s, 1, "2026-01-01T00:00:00Z", nil, nil) // will be deleted
	seedArchiveRow(t, s, 2, "2026-09-01T00:00:00Z", nil, nil) // will survive

	for _, q := range []string{
		`INSERT INTO decision_trace_entries (tool_decision_id, rule_order, rule_name, decision, reason)
		 VALUES (1, 1, 'safecmds', 'allow', 'r1')`,
		`INSERT INTO decision_trace_entries (tool_decision_id, rule_order, rule_name, decision, reason)
		 VALUES (2, 1, 'safecmds', 'allow', 'r2')`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatalf("seed trace: %v", err)
		}
	}

	deleted, err := s.DeleteRowsBefore("2026-09-01T00:00:00Z")
	if err != nil {
		t.Fatalf("DeleteRowsBefore: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}

	var traceCount int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM decision_trace_entries WHERE tool_decision_id = 1").Scan(&traceCount); err != nil {
		t.Fatalf("count trace for deleted decision: %v", err)
	}
	if traceCount != 0 {
		t.Errorf("decision_trace_entries for deleted id=1 = %d, want 0 (cascade)", traceCount)
	}

	var survivingTraceCount int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM decision_trace_entries WHERE tool_decision_id = 2").Scan(&survivingTraceCount); err != nil {
		t.Fatalf("count trace for surviving decision: %v", err)
	}
	if survivingTraceCount != 1 {
		t.Errorf("decision_trace_entries for surviving id=2 = %d, want 1 (untouched)", survivingTraceCount)
	}
}

// TestVacuum_SucceedsAfterDelete is the ceremony's last-step smoke test: on
// a database that just had rows removed, VACUUM must succeed and the
// database must remain fully readable and internally consistent afterward.
func TestVacuum_SucceedsAfterDelete(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	for i := 1; i <= 5; i++ {
		seedArchiveRow(t, s, i, "2026-01-01T00:00:00Z", nil, nil)
	}
	if _, err := s.DeleteRowsBefore("2026-06-01T00:00:00Z"); err != nil {
		t.Fatalf("DeleteRowsBefore: %v", err)
	}

	if err := s.Vacuum(); err != nil {
		t.Fatalf("Vacuum: %v", err)
	}

	var integrityResult string
	if err := s.db.QueryRow("PRAGMA integrity_check").Scan(&integrityResult); err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	if integrityResult != "ok" {
		t.Errorf("integrity_check = %q, want ok", integrityResult)
	}

	var remaining int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM tool_decisions").Scan(&remaining); err != nil {
		t.Fatalf("count after vacuum: %v", err)
	}
	if remaining != 0 {
		t.Errorf("remaining rows after delete+vacuum = %d, want 0", remaining)
	}
}

// TestVacuum_NoopOnEmptyDatabase pins that VACUUM is safe to call even when
// there is nothing to reclaim (the archive CLI subcommand's own contract is
// to SKIP calling it in that case, but Vacuum itself must not error either
// way — the skip is an efficiency/lock-avoidance choice at the call site,
// not a correctness requirement on this method).
func TestVacuum_NoopOnEmptyDatabase(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.Vacuum(); err != nil {
		t.Fatalf("Vacuum on empty database: %v", err)
	}
}
