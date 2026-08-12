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
	t.Cleanup(func() { _ = s.Close() })
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

	// A hook Reject is 'rejected', NOT 'denied': nobody was asked.
	if n := countRows(t, s, "outcome='rejected'"); n != 1 {
		t.Errorf("rejected rows = %d, want 1", n)
	}
	if n := countRows(t, s, "outcome='denied'"); n != 0 {
		t.Errorf("denied rows = %d, want 0 (a hook Reject is not a denial)", n)
	}

	var hookDec, reason string
	_ = s.db.QueryRow("SELECT hook_decision, hook_reason FROM tool_decisions WHERE session_id='sess1'").Scan(&hookDec, &reason)
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
	_ = s.db.QueryRow("SELECT tool_use_id FROM tool_decisions WHERE session_id='sess1'").Scan(&toolUseID)
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
	_ = s.db.QueryRow("SELECT hook_decision FROM tool_decisions WHERE session_id='sess1'").Scan(&hookDec)
	if hookDec != "allow" {
		t.Errorf("hook_decision = %q, want allow", hookDec)
	}
}

func TestRecordPreToolDecision_Abstain(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Bash", "tool-abs", json.RawMessage(`{"command":"some-unknown-cmd"}`))
	result := hookio.RuleResult{Decision: hookio.NoOpinion, Reason: ""}

	err := RecordPreToolDecision(s, input, result)
	if err != nil {
		t.Fatalf("RecordPreToolDecision: %v", err)
	}

	if n := countRows(t, s, "outcome='pending'"); n != 1 {
		t.Errorf("pending rows = %d, want 1", n)
	}

	var hookDec string
	_ = s.db.QueryRow("SELECT hook_decision FROM tool_decisions WHERE session_id='sess1'").Scan(&hookDec)
	if hookDec != "abstain" {
		t.Errorf("hook_decision = %q, want abstain", hookDec)
	}
}

func TestFullLifecycle_Abstain_ThenApproved(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Bash", "tool-abs2", json.RawMessage(`{"command":"unknown-cmd"}`))
	result := hookio.RuleResult{Decision: hookio.NoOpinion}

	_ = RecordPreToolDecision(s, input, result)
	_ = ResolveApproved(s, input, "")

	if o := getOutcome(t, s, "sess1"); o != "approved" {
		t.Errorf("outcome = %q, want approved", o)
	}
}

func TestFullLifecycle_Abstain_ThenNeverResolved(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Bash", "tool-abs3", json.RawMessage(`{"command":"unknown-cmd"}`))
	result := hookio.RuleResult{Decision: hookio.NoOpinion}

	_ = RecordPreToolDecision(s, input, result)
	_ = ResolveUnresolvedAll(s, "sess1")

	if o := getOutcome(t, s, "sess1"); o != "unresolved" {
		t.Errorf("outcome = %q, want unresolved", o)
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
	_ = s.db.QueryRow("SELECT hook_decision FROM tool_decisions WHERE session_id='sess1'").Scan(&hookDec)
	if hookDec != nil {
		t.Errorf("hook_decision = %v, want NULL for built-in ASK", *hookDec)
	}
}

func TestRecordPermissionRequest_ExistingPreToolRow(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Bash", "tool-3", json.RawMessage(`{"command":"git push --force"}`))
	result := hookio.RuleResult{Decision: hookio.Ask, Reason: "force push"}

	_ = RecordPreToolDecision(s, input, result)
	err := RecordPermissionRequest(s, input, `[{"type":"toolAlwaysAllow"}]`)
	if err != nil {
		t.Fatalf("RecordPermissionRequest: %v", err)
	}

	if n := countRows(t, s, "1=1"); n != 1 {
		t.Errorf("total rows = %d, want 1 (no duplicate)", n)
	}

	var suggestions string
	_ = s.db.QueryRow("SELECT permission_suggestions FROM tool_decisions WHERE session_id='sess1'").Scan(&suggestions)
	if suggestions != `[{"type":"toolAlwaysAllow"}]` {
		t.Errorf("permission_suggestions = %q", suggestions)
	}
}

func TestResolveApproved_ByToolUseID(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Bash", "tool-4", json.RawMessage(`{"command":"git push --force"}`))
	result := hookio.RuleResult{Decision: hookio.Ask, Reason: "force push"}
	_ = RecordPreToolDecision(s, input, result)

	err := ResolveApproved(s, input, "")
	if err != nil {
		t.Fatalf("ResolveApproved: %v", err)
	}

	if o := getOutcome(t, s, "sess1"); o != "approved" {
		t.Errorf("outcome = %q, want approved", o)
	}

	var resolvedAt string
	_ = s.db.QueryRow("SELECT resolved_at FROM tool_decisions WHERE session_id='sess1'").Scan(&resolvedAt)
	if resolvedAt == "" {
		t.Error("resolved_at should be set")
	}
}

func TestResolveApproved_ByHash(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Write", "", json.RawMessage(`{"file_path":"/etc/hosts"}`))
	_ = RecordPermissionRequest(s, input, "")

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

func TestResolveUnresolvedAll(t *testing.T) {
	s := testStore(t)
	input1 := testInput("sess1", "Bash", "tool-a", json.RawMessage(`{"command":"cmd1"}`))
	input2 := testInput("sess1", "Bash", "tool-b", json.RawMessage(`{"command":"cmd2"}`))
	input3 := testInput("sess2", "Bash", "tool-c", json.RawMessage(`{"command":"cmd3"}`))
	result := hookio.RuleResult{Decision: hookio.Ask, Reason: "test"}

	_ = RecordPreToolDecision(s, input1, result)
	_ = RecordPreToolDecision(s, input2, result)
	_ = RecordPreToolDecision(s, input3, result)

	err := ResolveUnresolvedAll(s, "sess1")
	if err != nil {
		t.Fatalf("ResolveUnresolvedAll: %v", err)
	}

	if n := countRows(t, s, "session_id='sess1' AND outcome='unresolved'"); n != 2 {
		t.Errorf("sess1 unresolved = %d, want 2", n)
	}
	// The sweep MUST NOT claim a denial that never happened.
	if n := countRows(t, s, "outcome='denied'"); n != 0 {
		t.Errorf("denied rows = %d, want 0 (the sweep is not a denial)", n)
	}
	if n := countRows(t, s, "session_id='sess2' AND outcome='pending'"); n != 1 {
		t.Errorf("sess2 should still be pending, got %d", n)
	}
}

func TestResolveUnresolvedAll_NoPendingRows(t *testing.T) {
	s := testStore(t)
	err := ResolveUnresolvedAll(s, "nonexistent-session")
	if err != nil {
		t.Fatalf("ResolveUnresolvedAll should not error on empty: %v", err)
	}
}

// TestResolveUnresolvedAll_LeavesAlreadyResolvedRowsAlone pins the sweep's other
// direction: it is a bulk statement with no per-row correlation, so it MUST only
// ever touch rows still 'pending'. A real decline, a hook Reject and a completed
// call all keep their own outcome.
func TestResolveUnresolvedAll_LeavesAlreadyResolvedRowsAlone(t *testing.T) {
	s := testStore(t)

	// A hook Reject -> 'rejected' at insert time.
	rejected := testInput("sess1", "Bash", "tool-rej", json.RawMessage(`{"command":"sudo rm -rf /"}`))
	_ = RecordPreToolDecision(s, rejected, hookio.RuleResult{Decision: hookio.Reject, Reason: "no"})

	// An actual decline -> 'denied'.
	declined := testInput("sess1", "Bash", "tool-den", json.RawMessage(`{"command":"declined-cmd"}`))
	_ = RecordPreToolDecision(s, declined, hookio.RuleResult{Decision: hookio.NoOpinion})
	declined.Reason = "user said no"
	_ = RecordPermissionDenied(s, declined)

	// A completed call -> 'approved'.
	approved := testInput("sess1", "Bash", "tool-app", json.RawMessage(`{"command":"ls"}`))
	_ = RecordPreToolDecision(s, approved, hookio.RuleResult{Decision: hookio.Ask})
	_ = ResolveApproved(s, approved, "")

	// One genuinely abandoned call -> 'unresolved'.
	abandoned := testInput("sess1", "Bash", "tool-aba", json.RawMessage(`{"command":"abandoned-cmd"}`))
	_ = RecordPreToolDecision(s, abandoned, hookio.RuleResult{Decision: hookio.Ask})

	if err := ResolveUnresolvedAll(s, "sess1"); err != nil {
		t.Fatalf("ResolveUnresolvedAll: %v", err)
	}

	for _, tc := range []struct{ toolUseID, want string }{
		{"tool-rej", "rejected"},
		{"tool-den", "denied"},
		{"tool-app", "approved"},
		{"tool-aba", "unresolved"},
	} {
		var got string
		if err := s.db.QueryRow(
			"SELECT outcome FROM tool_decisions WHERE tool_use_id=?", tc.toolUseID,
		).Scan(&got); err != nil {
			t.Fatalf("read outcome for %s: %v", tc.toolUseID, err)
		}
		if got != tc.want {
			t.Errorf("%s outcome = %q, want %q", tc.toolUseID, got, tc.want)
		}
	}
}

// TestOutcomeThreeWayDistinction pins the invariant this whole vocabulary
// exists for, in BOTH directions: each of the three refusal-shaped provenances
// gets its OWN outcome value, and none of them is ever written as any of the
// other two.
//
//	a real decline    -> 'denied'
//	a hook Reject     -> 'rejected'
//	never resolved    -> 'unresolved'
func TestOutcomeThreeWayDistinction(t *testing.T) {
	cases := []struct {
		name    string
		session string
		record  func(t *testing.T, s *Store, in *hookio.HookInput)
		want    string
	}{
		{
			name:    "real decline via PermissionDenied",
			session: "decline",
			record: func(t *testing.T, s *Store, in *hookio.HookInput) {
				t.Helper()
				if err := RecordPreToolDecision(s, in, hookio.RuleResult{Decision: hookio.NoOpinion}); err != nil {
					t.Fatalf("RecordPreToolDecision: %v", err)
				}
				in.Reason = "user declined"
				if err := RecordPermissionDenied(s, in); err != nil {
					t.Fatalf("RecordPermissionDenied: %v", err)
				}
			},
			want: OutcomeDenied,
		},
		{
			name:    "hook Reject",
			session: "reject",
			record: func(t *testing.T, s *Store, in *hookio.HookInput) {
				t.Helper()
				if err := RecordPreToolDecision(s, in, hookio.RuleResult{Decision: hookio.Reject, Reason: "blocked"}); err != nil {
					t.Fatalf("RecordPreToolDecision: %v", err)
				}
			},
			want: OutcomeRejected,
		},
		{
			name:    "never resolved, swept at SessionEnd",
			session: "sweep",
			record: func(t *testing.T, s *Store, in *hookio.HookInput) {
				t.Helper()
				if err := RecordPreToolDecision(s, in, hookio.RuleResult{Decision: hookio.Ask}); err != nil {
					t.Fatalf("RecordPreToolDecision: %v", err)
				}
				if err := ResolveUnresolvedAll(s, in.SessionID); err != nil {
					t.Fatalf("ResolveUnresolvedAll: %v", err)
				}
			},
			want: OutcomeUnresolved,
		},
	}

	// Every value the three cases collectively must stay distinct across.
	all := []string{OutcomeDenied, OutcomeRejected, OutcomeUnresolved}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testStore(t)
			in := testInput(tc.session, "Bash", "tool-"+tc.session, json.RawMessage(`{"command":"some-cmd"}`))
			tc.record(t, s, in)

			// Positive direction: it IS the expected value.
			if got := getOutcome(t, s, tc.session); got != tc.want {
				t.Errorf("outcome = %q, want %q", got, tc.want)
			}
			// Negative direction: it is NOT either of the other two.
			for _, other := range all {
				if other == tc.want {
					continue
				}
				if n := countRows(t, s, "outcome='"+other+"'"); n != 0 {
					t.Errorf("%d row(s) written as %q; the three provenances MUST stay distinct", n, other)
				}
			}
			// Only 'denied' means "somebody rendered a judgement", so only it
			// counts as gradeable ground truth.
			wantDecision := tc.want != OutcomeUnresolved
			if got := OutcomeIsDecision(tc.want); got != wantDecision {
				t.Errorf("OutcomeIsDecision(%q) = %v, want %v", tc.want, got, wantDecision)
			}
		})
	}
}

func TestOutcomeIsDecision(t *testing.T) {
	for _, tc := range []struct {
		outcome string
		want    bool
	}{
		{OutcomeApproved, true},
		{OutcomeDenied, true},
		{OutcomeRejected, true},
		{OutcomePending, false},
		{OutcomeUnresolved, false},
		{"some-future-value", false},
	} {
		if got := OutcomeIsDecision(tc.outcome); got != tc.want {
			t.Errorf("OutcomeIsDecision(%q) = %v, want %v", tc.outcome, got, tc.want)
		}
	}
}

func TestFullLifecycle_Approved(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Bash", "tool-lc1", json.RawMessage(`{"command":"git push --force"}`))
	result := hookio.RuleResult{Decision: hookio.Ask, Reason: "force push"}

	_ = RecordPreToolDecision(s, input, result)
	_ = RecordPermissionRequest(s, input, `[{"type":"toolAlwaysAllow"}]`)
	_ = ResolveApproved(s, input, "")

	if o := getOutcome(t, s, "sess1"); o != "approved" {
		t.Errorf("outcome = %q, want approved", o)
	}
	if n := countRows(t, s, "1=1"); n != 1 {
		t.Errorf("total rows = %d, want 1", n)
	}
}

// TestFullLifecycle_PromptedThenNeverAnswered covers the ask-dialog-shown-but-
// never-answered path: PreToolUse ASK, the permission dialog is recorded, and
// then the session simply ends. Nobody answered, so the row is 'unresolved'.
func TestFullLifecycle_PromptedThenNeverAnswered(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Bash", "tool-lc2", json.RawMessage(`{"command":"rm -rf /"}`))
	result := hookio.RuleResult{Decision: hookio.Ask, Reason: "dangerous command"}

	_ = RecordPreToolDecision(s, input, result)
	_ = RecordPermissionRequest(s, input, "")
	_ = ResolveUnresolvedAll(s, "sess1")

	if o := getOutcome(t, s, "sess1"); o != "unresolved" {
		t.Errorf("outcome = %q, want unresolved", o)
	}
}

// TestFullLifecycle_PromptedThenDeclined is the counterpart: the same ASK, but
// the user actually answered "no". That MUST be 'denied', not 'unresolved'.
func TestFullLifecycle_PromptedThenDeclined(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Bash", "tool-lc2b", json.RawMessage(`{"command":"rm -rf /"}`))
	result := hookio.RuleResult{Decision: hookio.Ask, Reason: "dangerous command"}

	_ = RecordPreToolDecision(s, input, result)
	_ = RecordPermissionRequest(s, input, "")
	input.Reason = "user declined"
	_ = RecordPermissionDenied(s, input)
	// The SessionEnd sweep still runs afterwards and must not overwrite it.
	_ = ResolveUnresolvedAll(s, "sess1")

	if o := getOutcome(t, s, "sess1"); o != "denied" {
		t.Errorf("outcome = %q, want denied", o)
	}
}

func TestFullLifecycle_BuiltinASK_Approved(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Write", "", json.RawMessage(`{"file_path":"/etc/hosts"}`))

	_ = RecordPermissionRequest(s, input, "")
	_ = ResolveApproved(s, input, "")

	if o := getOutcome(t, s, "sess1"); o != "approved" {
		t.Errorf("outcome = %q, want approved", o)
	}

	var hookDec *string
	_ = s.db.QueryRow("SELECT hook_decision FROM tool_decisions WHERE session_id='sess1'").Scan(&hookDec)
	if hookDec != nil {
		t.Errorf("hook_decision = %v, want NULL", *hookDec)
	}
}

func TestRecordPermissionDenied_UpdatesExistingPendingRow(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Bash", "tool-pd1", json.RawMessage(`{"command":"rm -rf /tmp/build"}`))
	result := hookio.RuleResult{Decision: hookio.NoOpinion}

	_ = RecordPreToolDecision(s, input, result)
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
	_ = s.db.QueryRow("SELECT outcome, outcome_notes FROM tool_decisions WHERE session_id='sess1'").Scan(&outcome, &outcomeNotes)
	if outcome != "denied" {
		t.Errorf("outcome = %q, want denied", outcome)
	}
	if outcomeNotes != "auto_mode_classifier: Auto mode denied: command targets a path outside the project" {
		t.Errorf("outcome_notes = %q", outcomeNotes)
	}

	var resolvedAt string
	_ = s.db.QueryRow("SELECT resolved_at FROM tool_decisions WHERE session_id='sess1'").Scan(&resolvedAt)
	if resolvedAt == "" {
		t.Error("resolved_at should be set")
	}
}

func TestRecordPermissionDenied_UpdatesByHashWhenNoToolUseID(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Write", "", json.RawMessage(`{"file_path":"/etc/hosts"}`))

	_ = RecordPermissionRequest(s, input, "")
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
	_ = s.db.QueryRow("SELECT outcome FROM tool_decisions WHERE session_id='sess1'").Scan(&outcome)
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
	_ = s.db.QueryRow("SELECT hook_decision, outcome, outcome_notes FROM tool_decisions WHERE session_id='sess1'").Scan(&hookDec, &outcome, &outcomeNotes)
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
			{RuleName: "envvars", Decision: hookio.NoOpinion, Reason: "not relevant"},
			{RuleName: "assume", Decision: hookio.NoOpinion, Reason: "not an assume command"},
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
	_ = s.db.QueryRow("SELECT id FROM tool_decisions WHERE session_id='sess1'").Scan(&decID)

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
	_ = s.db.QueryRow("SELECT COUNT(*) FROM decision_trace_entries").Scan(&count)
	if count != 0 {
		t.Errorf("trace entries = %d, want 0 (tracing disabled)", count)
	}
}

// queryHookContext returns the v5 context columns for the most recent row in
// the given session as *string (nil = SQL NULL).
func queryHookContext(t *testing.T, s *Store, sessionID string) (permMode, promptID, transcript, toolResp *string) {
	t.Helper()
	err := s.db.QueryRow(
		`SELECT permission_mode, prompt_id, transcript_path, tool_response
		 FROM tool_decisions WHERE session_id=? ORDER BY id DESC LIMIT 1`, sessionID,
	).Scan(&permMode, &promptID, &transcript, &toolResp)
	if err != nil {
		t.Fatalf("query hook context: %v", err)
	}
	return
}

func TestRecordPreToolDecision_PersistsHookContext(t *testing.T) {
	for _, trace := range []bool{false, true} {
		name := "non-trace"
		if trace {
			name = "trace"
		}
		t.Run(name, func(t *testing.T) {
			s := testStore(t)
			input := testInput("hc", "Bash", "tool-hc", json.RawMessage(`{"command":"ls"}`))
			input.PermissionMode = "acceptEdits"
			input.PromptID = "prompt-9"
			input.TranscriptPath = "/x/t.jsonl"
			input.AgentType = "Explore"
			result := hookio.RuleResult{Decision: hookio.Approve, Reason: "safe"}
			if trace {
				result.Trace = []hookio.TraceEntry{{RuleName: "safecmds", Decision: hookio.Approve, Reason: "safe"}}
			}
			if err := RecordPreToolDecision(s, input, result); err != nil {
				t.Fatalf("RecordPreToolDecision: %v", err)
			}

			pm, pid, tp, _ := queryHookContext(t, s, "hc")
			if pm == nil || *pm != "acceptEdits" {
				t.Errorf("permission_mode = %v, want acceptEdits", pm)
			}
			if pid == nil || *pid != "prompt-9" {
				t.Errorf("prompt_id = %v, want prompt-9", pid)
			}
			if tp == nil || *tp != "/x/t.jsonl" {
				t.Errorf("transcript_path = %v, want /x/t.jsonl", tp)
			}
			var agentType *string
			_ = s.db.QueryRow("SELECT agent_type FROM tool_decisions WHERE session_id='hc'").Scan(&agentType)
			if agentType == nil || *agentType != "Explore" {
				t.Errorf("agent_type = %v, want Explore", agentType)
			}
		})
	}
}

func TestResolveApproved_SetsToolResponse_NoClobber(t *testing.T) {
	s := testStore(t)
	input := testInput("tr", "Bash", "tool-tr", json.RawMessage(`{"command":"ls"}`))
	input.PermissionMode = "default"
	input.PromptID = "prompt-1"
	result := hookio.RuleResult{Decision: hookio.Ask, Reason: "ask"}
	if err := RecordPreToolDecision(s, input, result); err != nil {
		t.Fatalf("RecordPreToolDecision: %v", err)
	}

	// PostToolUse carries the tool_response payload.
	input.ToolResponse = json.RawMessage(`{"stdout":"hi","is_error":false}`)
	if err := ResolveApproved(s, input, "done"); err != nil {
		t.Fatalf("ResolveApproved: %v", err)
	}

	pm, pid, _, tr := queryHookContext(t, s, "tr")
	if pm == nil || *pm != "default" {
		t.Errorf("permission_mode clobbered by PostToolUse = %v, want default", pm)
	}
	if pid == nil || *pid != "prompt-1" {
		t.Errorf("prompt_id clobbered by PostToolUse = %v, want prompt-1", pid)
	}
	if tr == nil || *tr != `{"stdout":"hi","is_error":false}` {
		t.Errorf("tool_response = %v, want the raw payload", tr)
	}
	if o := getOutcome(t, s, "tr"); o != "approved" {
		t.Errorf("outcome = %q, want approved", o)
	}
}

func TestResolveApproved_NoPendingRow_DropsToolResponse(t *testing.T) {
	s := testStore(t)
	input := testInput("drop", "Bash", "tool-drop", json.RawMessage(`{"command":"ls"}`))
	input.ToolResponse = json.RawMessage(`{"is_error":true}`)

	if err := ResolveApproved(s, input, ""); err != nil {
		t.Fatalf("ResolveApproved: %v", err)
	}
	// No pending PreToolUse row existed → ResolveApproved does NOT INSERT a
	// fallback row, so the tool_response is dropped on the floor.
	if n := countRows(t, s, "1=1"); n != 0 {
		t.Errorf("rows = %d, want 0 (ResolveApproved has no INSERT fallback)", n)
	}
}

func TestRecordPermissionDenied_FallbackSetsPermissionModeAndAgentType(t *testing.T) {
	s := testStore(t)
	input := testInput("den", "Bash", "tool-den", json.RawMessage(`{"command":"curl http://evil"}`))
	input.PermissionMode = "auto"
	input.AgentType = "general-purpose"
	input.Reason = "Auto mode denied: network access not allowed"

	if err := RecordPermissionDenied(s, input); err != nil {
		t.Fatalf("RecordPermissionDenied: %v", err)
	}
	if n := countRows(t, s, "1=1"); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}

	pm, _, _, _ := queryHookContext(t, s, "den")
	if pm == nil || *pm != "auto" {
		t.Errorf("permission_mode = %v, want auto (else auto-denials derive to unknown)", pm)
	}
	var at *string
	_ = s.db.QueryRow("SELECT agent_type FROM tool_decisions WHERE session_id='den'").Scan(&at)
	if at == nil || *at != "general-purpose" {
		t.Errorf("agent_type = %v, want general-purpose", at)
	}
	// This auto-mode denial (the primary calibration signal) now buckets as auto.
	if got := ApprovalSource(pm, nil, nil); got != "auto" {
		t.Errorf("ApprovalSource = %q, want auto", got)
	}
}

func TestRecordPermissionRequest_FallbackSetsPermissionMode(t *testing.T) {
	s := testStore(t)
	input := testInput("req", "Write", "", json.RawMessage(`{"file_path":"/etc/hosts"}`))
	input.PermissionMode = "default"
	input.PromptID = "prompt-2"
	input.AgentType = "Explore"

	if err := RecordPermissionRequest(s, input, `[{"type":"toolAlwaysAllow"}]`); err != nil {
		t.Fatalf("RecordPermissionRequest: %v", err)
	}

	pm, pid, _, _ := queryHookContext(t, s, "req")
	if pm == nil || *pm != "default" {
		t.Errorf("permission_mode = %v, want default", pm)
	}
	if pid == nil || *pid != "prompt-2" {
		t.Errorf("prompt_id = %v, want prompt-2", pid)
	}
	var at *string
	_ = s.db.QueryRow("SELECT agent_type FROM tool_decisions WHERE session_id='req'").Scan(&at)
	if at == nil || *at != "Explore" {
		t.Errorf("agent_type = %v, want Explore", at)
	}
}

func TestFullLifecycle_Abstain_ThenPermissionDenied(t *testing.T) {
	s := testStore(t)
	input := testInput("sess1", "Bash", "tool-pd4", json.RawMessage(`{"command":"dangerous-cmd"}`))
	result := hookio.RuleResult{Decision: hookio.NoOpinion}

	_ = RecordPreToolDecision(s, input, result)

	input.Reason = "Auto mode denied: unrecognized command"
	_ = RecordPermissionDenied(s, input)

	if o := getOutcome(t, s, "sess1"); o != "denied" {
		t.Errorf("outcome = %q, want denied", o)
	}

	var hookDec string
	_ = s.db.QueryRow("SELECT hook_decision FROM tool_decisions WHERE session_id='sess1'").Scan(&hookDec)
	if hookDec != "abstain" {
		t.Errorf("hook_decision = %q, want abstain (original hook decision preserved)", hookDec)
	}
}
