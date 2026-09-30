package asklog

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func TestRecordPostToolUseFailure_UpdatesExistingPendingRow(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Bash", "tool-ptf1", json.RawMessage(`{"command":"flaky-cmd"}`))
	result := hookio.RuleResult{Decision: hookio.NoOpinion}

	if err := RecordPreToolDecision(s, input, result); err != nil {
		t.Fatalf("RecordPreToolDecision: %v", err)
	}
	if n := countRows(t, s, "outcome='pending'"); n != 1 {
		t.Fatalf("setup: pending rows = %d, want 1", n)
	}

	input.Reason = "the tool exited non-zero"
	input.ToolResponse = json.RawMessage(`{"is_error":true,"stdout":"","stderr":"boom"}`)
	if err := RecordPostToolUseFailure(s, input); err != nil {
		t.Fatalf("RecordPostToolUseFailure: %v", err)
	}

	if n := countRows(t, s, "1=1"); n != 1 {
		t.Errorf("total rows = %d, want 1", n)
	}

	var outcome, outcomeNotes string
	var exitCode *int
	var excerpt *string
	if err := s.db.QueryRow(
		"SELECT outcome, outcome_notes, tool_response_exit_code, tool_response_excerpt FROM tool_decisions WHERE session_id='sess1'",
	).Scan(&outcome, &outcomeNotes, &exitCode, &excerpt); err != nil {
		t.Fatalf("query: %v", err)
	}
	if outcome != OutcomeFailed {
		t.Errorf("outcome = %q, want %q", outcome, OutcomeFailed)
	}
	if outcomeNotes != "tool_failure: the tool exited non-zero" {
		t.Errorf("outcome_notes = %q", outcomeNotes)
	}
	if exitCode == nil || *exitCode != 1 {
		t.Errorf("tool_response_exit_code = %v, want 1", exitCode)
	}
	if excerpt == nil || *excerpt != "boom" {
		t.Errorf("tool_response_excerpt = %v, want %q", excerpt, "boom")
	}

	var resolvedAt string
	_ = s.db.QueryRow("SELECT resolved_at FROM tool_decisions WHERE session_id='sess1'").Scan(&resolvedAt)
	if resolvedAt == "" {
		t.Error("resolved_at should be set")
	}
}

func TestRecordPostToolUseFailure_UpdatesByHashWhenNoToolUseID(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Write", "", json.RawMessage(`{"file_path":"/tmp/x"}`))

	if err := RecordPreToolDecision(s, input, hookio.RuleResult{Decision: hookio.Ask}); err != nil {
		t.Fatalf("RecordPreToolDecision: %v", err)
	}
	if n := countRows(t, s, "outcome='pending'"); n != 1 {
		t.Fatalf("setup: pending rows = %d, want 1", n)
	}

	input.Reason = "write failed"
	if err := RecordPostToolUseFailure(s, input); err != nil {
		t.Fatalf("RecordPostToolUseFailure: %v", err)
	}

	if n := countRows(t, s, "1=1"); n != 1 {
		t.Errorf("total rows = %d, want 1", n)
	}

	if o := getOutcome(t, s, "sess1"); o != OutcomeFailed {
		t.Errorf("outcome = %q, want %q", o, OutcomeFailed)
	}
}

func TestRecordPostToolUseFailure_InsertsWhenNoPendingRow(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Bash", "tool-ptf3", json.RawMessage(`{"command":"some-cmd"}`))
	input.Reason = "process killed"

	if err := RecordPostToolUseFailure(s, input); err != nil {
		t.Fatalf("RecordPostToolUseFailure: %v", err)
	}

	if n := countRows(t, s, "1=1"); n != 1 {
		t.Errorf("total rows = %d, want 1", n)
	}

	var hookDec *string
	var outcome, outcomeNotes string
	if err := s.db.QueryRow(
		"SELECT hook_decision, outcome, outcome_notes FROM tool_decisions WHERE session_id='sess1'",
	).Scan(&hookDec, &outcome, &outcomeNotes); err != nil {
		t.Fatalf("query: %v", err)
	}
	if hookDec != nil {
		t.Errorf("hook_decision = %v, want NULL", *hookDec)
	}
	if outcome != OutcomeFailed {
		t.Errorf("outcome = %q, want %q", outcome, OutcomeFailed)
	}
	if outcomeNotes != "tool_failure: process killed" {
		t.Errorf("outcome_notes = %q", outcomeNotes)
	}
}

// TestRecordPostToolUseFailure_DistinctFromApproved pins the reason this
// outcome value exists at all: a failed run must never collapse into
// 'approved', and must be classified as a gradeable decision (the hook DID
// allow the call — see OutcomeIsDecision's doc comment).
func TestRecordPostToolUseFailure_DistinctFromApproved(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Bash", "tool-ptf4", json.RawMessage(`{"command":"some-cmd"}`))

	if err := RecordPreToolDecision(s, input, hookio.RuleResult{Decision: hookio.Ask}); err != nil {
		t.Fatalf("RecordPreToolDecision: %v", err)
	}
	input.Reason = "nonzero exit"
	if err := RecordPostToolUseFailure(s, input); err != nil {
		t.Fatalf("RecordPostToolUseFailure: %v", err)
	}

	if n := countRows(t, s, "outcome='"+OutcomeApproved+"'"); n != 0 {
		t.Errorf("%d row(s) written as approved; a failure MUST NOT collapse into approved", n)
	}
	if !OutcomeIsDecision(OutcomeFailed) {
		t.Error("OutcomeIsDecision(OutcomeFailed) = false, want true")
	}
}
