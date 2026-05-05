package asklog

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func testInput(sessionID, toolName, toolUseID string, toolInput json.RawMessage) *hookio.HookInput {
	return &hookio.HookInput{
		SessionID: sessionID,
		CWD:       "/test/project",
		ToolName:  toolName,
		ToolUseID: toolUseID,
		ToolInput: toolInput,
	}
}

func countRows(t *testing.T, s *Store, where string) int {
	t.Helper()
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM tool_decisions WHERE " + where).Scan(&n)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func getOutcome(t *testing.T, s *Store, sessionID string) string {
	t.Helper()
	var outcome string
	err := s.db.QueryRow("SELECT outcome FROM tool_decisions WHERE session_id=? ORDER BY id DESC LIMIT 1", sessionID).Scan(&outcome)
	if err != nil {
		t.Fatalf("getOutcome: %v", err)
	}
	return outcome
}

// querySandboxEnabled returns the sandbox_enabled column for the most
// recent row in the given session, as a *int (nil = SQL NULL).
func querySandboxEnabled(t *testing.T, s *Store, sessionID string) *int {
	t.Helper()
	var v *int
	err := s.db.QueryRow(
		"SELECT sandbox_enabled FROM tool_decisions WHERE session_id=? ORDER BY id DESC LIMIT 1",
		sessionID,
	).Scan(&v)
	if err != nil {
		t.Fatalf("query sandbox_enabled: %v", err)
	}
	return v
}

func TestRecordPreToolDecision_SandboxEnabledNullByDefault(t *testing.T) {
	s := testStore(t)
	input := testInput("sb-null", "Bash", "tool-x", json.RawMessage(`{"command":"ls"}`))
	result := hookio.RuleResult{Decision: hookio.Approve, Reason: "safe"}
	if err := RecordPreToolDecision(s, input, result); err != nil {
		t.Fatalf("RecordPreToolDecision: %v", err)
	}
	if v := querySandboxEnabled(t, s, "sb-null"); v != nil {
		t.Errorf("sandbox_enabled = %v, want NULL when SetSandboxEnabled was not called", *v)
	}
}

func TestRecordPreToolDecision_SandboxEnabledTrue(t *testing.T) {
	s := testStore(t)
	s.SetSandboxEnabled(true)
	input := testInput("sb-true", "Bash", "tool-y", json.RawMessage(`{"command":"ls"}`))
	result := hookio.RuleResult{Decision: hookio.Approve, Reason: "safe"}
	if err := RecordPreToolDecision(s, input, result); err != nil {
		t.Fatalf("RecordPreToolDecision: %v", err)
	}
	v := querySandboxEnabled(t, s, "sb-true")
	if v == nil || *v != 1 {
		t.Errorf("sandbox_enabled = %v, want 1", v)
	}
}

func TestRecordPreToolDecision_SandboxEnabledFalse(t *testing.T) {
	s := testStore(t)
	s.SetSandboxEnabled(false)
	input := testInput("sb-false", "Bash", "tool-z", json.RawMessage(`{"command":"ls"}`))
	result := hookio.RuleResult{Decision: hookio.Approve, Reason: "safe"}
	if err := RecordPreToolDecision(s, input, result); err != nil {
		t.Fatalf("RecordPreToolDecision: %v", err)
	}
	v := querySandboxEnabled(t, s, "sb-false")
	if v == nil || *v != 0 {
		t.Errorf("sandbox_enabled = %v, want 0", v)
	}
}

func TestRecordPreToolDecision_Deny(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Bash", "tool-1", json.RawMessage(`{"command":"git reset --hard"}`))
	result := hookio.RuleResult{Decision: hookio.Reject, Reason: "destructive git command"}

	err := RecordPreToolDecision(s, input, result)
	if err != nil {
		t.Fatalf("RecordPreToolDecision: %v", err)
	}

	if n := countRows(t, s, "outcome='denied'"); n != 1 {
		t.Errorf("denied rows = %d, want 1", n)
	}

	var hookDec, reason string
	s.db.QueryRow("SELECT hook_decision, hook_reason FROM tool_decisions WHERE session_id='sess1'").Scan(&hookDec, &reason)
	if hookDec != "deny" {
		t.Errorf("hook_decision = %q, want deny", hookDec)
	}
	if reason != "destructive git command" {
		t.Errorf("hook_reason = %q", reason)
	}
}

func TestRecordPreToolDecision_Ask(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Bash", "tool-2", json.RawMessage(`{"command":"git push --force"}`))
	result := hookio.RuleResult{Decision: hookio.Ask, Reason: "force push detected"}

	err := RecordPreToolDecision(s, input, result)
	if err != nil {
		t.Fatalf("RecordPreToolDecision: %v", err)
	}

	if n := countRows(t, s, "outcome='pending'"); n != 1 {
		t.Errorf("pending rows = %d, want 1", n)
	}

	var toolUseID string
	s.db.QueryRow("SELECT tool_use_id FROM tool_decisions WHERE session_id='sess1'").Scan(&toolUseID)
	if toolUseID != "tool-2" {
		t.Errorf("tool_use_id = %q, want tool-2", toolUseID)
	}
}

func TestRecordPreToolDecision_Approve(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Bash", "tool-a1", json.RawMessage(`{"command":"git log"}`))
	result := hookio.RuleResult{Decision: hookio.Approve, Reason: "read-only git command"}

	err := RecordPreToolDecision(s, input, result)
	if err != nil {
		t.Fatalf("RecordPreToolDecision: %v", err)
	}

	if n := countRows(t, s, "outcome='pending'"); n != 1 {
		t.Errorf("pending rows = %d, want 1", n)
	}

	var hookDec string
	s.db.QueryRow("SELECT hook_decision FROM tool_decisions WHERE session_id='sess1'").Scan(&hookDec)
	if hookDec != "allow" {
		t.Errorf("hook_decision = %q, want allow", hookDec)
	}
}

func TestRecordPreToolDecision_Abstain(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Bash", "tool-abs", json.RawMessage(`{"command":"some-unknown-cmd"}`))
	result := hookio.RuleResult{Decision: hookio.Abstain, Reason: ""}

	err := RecordPreToolDecision(s, input, result)
	if err != nil {
		t.Fatalf("RecordPreToolDecision: %v", err)
	}

	if n := countRows(t, s, "outcome='pending'"); n != 1 {
		t.Errorf("pending rows = %d, want 1", n)
	}

	var hookDec string
	s.db.QueryRow("SELECT hook_decision FROM tool_decisions WHERE session_id='sess1'").Scan(&hookDec)
	if hookDec != "abstain" {
		t.Errorf("hook_decision = %q, want abstain", hookDec)
	}
}

func TestFullLifecycle_Abstain_ThenApproved(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Bash", "tool-abs2", json.RawMessage(`{"command":"unknown-cmd"}`))
	result := hookio.RuleResult{Decision: hookio.Abstain}

	RecordPreToolDecision(s, input, result)
	ResolveApproved(s, input, "")

	if o := getOutcome(t, s, "sess1"); o != "approved" {
		t.Errorf("outcome = %q, want approved", o)
	}
}

func TestFullLifecycle_Abstain_ThenDenied(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Bash", "tool-abs3", json.RawMessage(`{"command":"unknown-cmd"}`))
	result := hookio.RuleResult{Decision: hookio.Abstain}

	RecordPreToolDecision(s, input, result)
	ResolveDeniedAll(s, "sess1")

	if o := getOutcome(t, s, "sess1"); o != "denied" {
		t.Errorf("outcome = %q, want denied", o)
	}
}

func TestRecordPermissionRequest_NewBuiltinASK(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Write", "", json.RawMessage(`{"file_path":"/etc/hosts"}`))

	err := RecordPermissionRequest(s, input, `[{"type":"toolAlwaysAllow"}]`)
	if err != nil {
		t.Fatalf("RecordPermissionRequest: %v", err)
	}

	if n := countRows(t, s, "outcome='pending'"); n != 1 {
		t.Errorf("pending rows = %d, want 1", n)
	}

	var hookDec *string
	s.db.QueryRow("SELECT hook_decision FROM tool_decisions WHERE session_id='sess1'").Scan(&hookDec)
	if hookDec != nil {
		t.Errorf("hook_decision = %v, want NULL for built-in ASK", *hookDec)
	}
}

func TestRecordPermissionRequest_ExistingPreToolRow(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Bash", "tool-3", json.RawMessage(`{"command":"git push --force"}`))
	result := hookio.RuleResult{Decision: hookio.Ask, Reason: "force push"}

	RecordPreToolDecision(s, input, result)
	err := RecordPermissionRequest(s, input, `[{"type":"toolAlwaysAllow"}]`)
	if err != nil {
		t.Fatalf("RecordPermissionRequest: %v", err)
	}

	if n := countRows(t, s, "1=1"); n != 1 {
		t.Errorf("total rows = %d, want 1 (no duplicate)", n)
	}

	var suggestions string
	s.db.QueryRow("SELECT permission_suggestions FROM tool_decisions WHERE session_id='sess1'").Scan(&suggestions)
	if suggestions != `[{"type":"toolAlwaysAllow"}]` {
		t.Errorf("permission_suggestions = %q", suggestions)
	}
}

func TestResolveApproved_ByToolUseID(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Bash", "tool-4", json.RawMessage(`{"command":"git push --force"}`))
	result := hookio.RuleResult{Decision: hookio.Ask, Reason: "force push"}
	RecordPreToolDecision(s, input, result)

	err := ResolveApproved(s, input, "")
	if err != nil {
		t.Fatalf("ResolveApproved: %v", err)
	}

	if o := getOutcome(t, s, "sess1"); o != "approved" {
		t.Errorf("outcome = %q, want approved", o)
	}

	var resolvedAt string
	s.db.QueryRow("SELECT resolved_at FROM tool_decisions WHERE session_id='sess1'").Scan(&resolvedAt)
	if resolvedAt == "" {
		t.Error("resolved_at should be set")
	}
}

func TestResolveApproved_ByHash(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Write", "", json.RawMessage(`{"file_path":"/etc/hosts"}`))
	RecordPermissionRequest(s, input, "")

	err := ResolveApproved(s, input, "")
	if err != nil {
		t.Fatalf("ResolveApproved: %v", err)
	}

	if o := getOutcome(t, s, "sess1"); o != "approved" {
		t.Errorf("outcome = %q, want approved", o)
	}
}

func TestResolveApproved_NoPendingRow(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Bash", "tool-99", json.RawMessage(`{"command":"ls"}`))

	err := ResolveApproved(s, input, "")
	if err != nil {
		t.Fatalf("ResolveApproved should not error on no match: %v", err)
	}
}

func TestResolveDeniedAll(t *testing.T) {
	s := testStore(t)
	input1 := testInput("sess1", "Bash", "tool-a", json.RawMessage(`{"command":"cmd1"}`))
	input2 := testInput("sess1", "Bash", "tool-b", json.RawMessage(`{"command":"cmd2"}`))
	input3 := testInput("sess2", "Bash", "tool-c", json.RawMessage(`{"command":"cmd3"}`))
	result := hookio.RuleResult{Decision: hookio.Ask, Reason: "test"}

	RecordPreToolDecision(s, input1, result)
	RecordPreToolDecision(s, input2, result)
	RecordPreToolDecision(s, input3, result)

	err := ResolveDeniedAll(s, "sess1")
	if err != nil {
		t.Fatalf("ResolveDeniedAll: %v", err)
	}

	if n := countRows(t, s, "session_id='sess1' AND outcome='denied'"); n != 2 {
		t.Errorf("sess1 denied = %d, want 2", n)
	}
	if n := countRows(t, s, "session_id='sess2' AND outcome='pending'"); n != 1 {
		t.Errorf("sess2 should still be pending, got %d", n)
	}
}

func TestResolveDeniedAll_NoPendingRows(t *testing.T) {
	s := testStore(t)
	err := ResolveDeniedAll(s, "nonexistent-session")
	if err != nil {
		t.Fatalf("ResolveDeniedAll should not error on empty: %v", err)
	}
}

func TestFullLifecycle_Approved(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Bash", "tool-lc1", json.RawMessage(`{"command":"git push --force"}`))
	result := hookio.RuleResult{Decision: hookio.Ask, Reason: "force push"}

	RecordPreToolDecision(s, input, result)
	RecordPermissionRequest(s, input, `[{"type":"toolAlwaysAllow"}]`)
	ResolveApproved(s, input, "")

	if o := getOutcome(t, s, "sess1"); o != "approved" {
		t.Errorf("outcome = %q, want approved", o)
	}
	if n := countRows(t, s, "1=1"); n != 1 {
		t.Errorf("total rows = %d, want 1", n)
	}
}

func TestFullLifecycle_Denied(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Bash", "tool-lc2", json.RawMessage(`{"command":"rm -rf /"}`))
	result := hookio.RuleResult{Decision: hookio.Ask, Reason: "dangerous command"}

	RecordPreToolDecision(s, input, result)
	RecordPermissionRequest(s, input, "")
	ResolveDeniedAll(s, "sess1")

	if o := getOutcome(t, s, "sess1"); o != "denied" {
		t.Errorf("outcome = %q, want denied", o)
	}
}

func TestFullLifecycle_BuiltinASK_Approved(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Write", "", json.RawMessage(`{"file_path":"/etc/hosts"}`))

	RecordPermissionRequest(s, input, "")
	ResolveApproved(s, input, "")

	if o := getOutcome(t, s, "sess1"); o != "approved" {
		t.Errorf("outcome = %q, want approved", o)
	}

	var hookDec *string
	s.db.QueryRow("SELECT hook_decision FROM tool_decisions WHERE session_id='sess1'").Scan(&hookDec)
	if hookDec != nil {
		t.Errorf("hook_decision = %v, want NULL", *hookDec)
	}
}

func TestRecordPermissionDenied_UpdatesExistingPendingRow(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Bash", "tool-pd1", json.RawMessage(`{"command":"rm -rf /tmp/build"}`))
	result := hookio.RuleResult{Decision: hookio.Abstain}

	RecordPreToolDecision(s, input, result)
	if n := countRows(t, s, "outcome='pending'"); n != 1 {
		t.Fatalf("setup: pending rows = %d, want 1", n)
	}

	input.Reason = "Auto mode denied: command targets a path outside the project"
	err := RecordPermissionDenied(s, input)
	if err != nil {
		t.Fatalf("RecordPermissionDenied: %v", err)
	}

	if n := countRows(t, s, "1=1"); n != 1 {
		t.Errorf("total rows = %d, want 1", n)
	}

	var outcome, outcomeNotes string
	s.db.QueryRow("SELECT outcome, outcome_notes FROM tool_decisions WHERE session_id='sess1'").Scan(&outcome, &outcomeNotes)
	if outcome != "denied" {
		t.Errorf("outcome = %q, want denied", outcome)
	}
	if outcomeNotes != "auto_mode_classifier: Auto mode denied: command targets a path outside the project" {
		t.Errorf("outcome_notes = %q", outcomeNotes)
	}

	var resolvedAt string
	s.db.QueryRow("SELECT resolved_at FROM tool_decisions WHERE session_id='sess1'").Scan(&resolvedAt)
	if resolvedAt == "" {
		t.Error("resolved_at should be set")
	}
}

func TestRecordPermissionDenied_UpdatesByHashWhenNoToolUseID(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Write", "", json.RawMessage(`{"file_path":"/etc/hosts"}`))

	RecordPermissionRequest(s, input, "")
	if n := countRows(t, s, "outcome='pending'"); n != 1 {
		t.Fatalf("setup: pending rows = %d, want 1", n)
	}

	input.Reason = "Auto mode denied: writing to system directory"
	err := RecordPermissionDenied(s, input)
	if err != nil {
		t.Fatalf("RecordPermissionDenied: %v", err)
	}

	if n := countRows(t, s, "1=1"); n != 1 {
		t.Errorf("total rows = %d, want 1", n)
	}

	var outcome string
	s.db.QueryRow("SELECT outcome FROM tool_decisions WHERE session_id='sess1'").Scan(&outcome)
	if outcome != "denied" {
		t.Errorf("outcome = %q, want denied", outcome)
	}
}

func TestRecordPermissionDenied_InsertsWhenNoPendingRow(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Bash", "tool-pd3", json.RawMessage(`{"command":"curl http://evil.com"}`))
	input.Reason = "Auto mode denied: network access not allowed"

	err := RecordPermissionDenied(s, input)
	if err != nil {
		t.Fatalf("RecordPermissionDenied: %v", err)
	}

	if n := countRows(t, s, "1=1"); n != 1 {
		t.Errorf("total rows = %d, want 1", n)
	}

	var outcome, outcomeNotes string
	var hookDec *string
	s.db.QueryRow("SELECT hook_decision, outcome, outcome_notes FROM tool_decisions WHERE session_id='sess1'").Scan(&hookDec, &outcome, &outcomeNotes)
	if hookDec != nil {
		t.Errorf("hook_decision = %v, want NULL", *hookDec)
	}
	if outcome != "denied" {
		t.Errorf("outcome = %q, want denied", outcome)
	}
	if outcomeNotes != "auto_mode_classifier: Auto mode denied: network access not allowed" {
		t.Errorf("outcome_notes = %q", outcomeNotes)
	}
}

func TestRecordPreToolDecision_WithTrace(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Bash", "tool-t1", json.RawMessage(`{"command":"git push --force"}`))
	result := hookio.RuleResult{
		Decision: hookio.Ask,
		Reason:   "force push detected",
		Module:   "git",
		Trace: []hookio.TraceEntry{
			{RuleName: "envvars", Decision: hookio.Abstain, Reason: "not relevant"},
			{RuleName: "assume", Decision: hookio.Abstain, Reason: "not an assume command"},
			{RuleName: "git", Decision: hookio.Ask, Reason: "force push detected"},
		},
	}

	err := RecordPreToolDecision(s, input, result)
	if err != nil {
		t.Fatalf("RecordPreToolDecision: %v", err)
	}

	if n := countRows(t, s, "1=1"); n != 1 {
		t.Fatalf("tool_decisions rows = %d, want 1", n)
	}

	var decID int
	s.db.QueryRow("SELECT id FROM tool_decisions WHERE session_id='sess1'").Scan(&decID)

	entries, err := s.QueryTraceByDecisionID(decID)
	if err != nil {
		t.Fatalf("QueryTraceByDecisionID: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("trace entries = %d, want 3", len(entries))
	}
	if entries[0].RuleName != "envvars" || entries[0].Decision != "abstain" {
		t.Errorf("entry[0] = %+v", entries[0])
	}
	if entries[2].RuleName != "git" || entries[2].Decision != "ask" {
		t.Errorf("entry[2] = %+v", entries[2])
	}
}

func TestRecordPreToolDecision_NilTrace_NoTraceEntries(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Bash", "tool-t2", json.RawMessage(`{"command":"git log"}`))
	result := hookio.RuleResult{
		Decision: hookio.Approve,
		Reason:   "read-only git",
		Module:   "git",
		Trace:    nil,
	}

	err := RecordPreToolDecision(s, input, result)
	if err != nil {
		t.Fatalf("RecordPreToolDecision: %v", err)
	}

	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM decision_trace_entries").Scan(&count)
	if count != 0 {
		t.Errorf("trace entries = %d, want 0 (tracing disabled)", count)
	}
}

func TestFullLifecycle_Abstain_ThenPermissionDenied(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Bash", "tool-pd4", json.RawMessage(`{"command":"dangerous-cmd"}`))
	result := hookio.RuleResult{Decision: hookio.Abstain}

	RecordPreToolDecision(s, input, result)

	input.Reason = "Auto mode denied: unrecognized command"
	RecordPermissionDenied(s, input)

	if o := getOutcome(t, s, "sess1"); o != "denied" {
		t.Errorf("outcome = %q, want denied", o)
	}

	var hookDec string
	s.db.QueryRow("SELECT hook_decision FROM tool_decisions WHERE session_id='sess1'").Scan(&hookDec)
	if hookDec != "abstain" {
		t.Errorf("hook_decision = %q, want abstain (original hook decision preserved)", hookDec)
	}
}
