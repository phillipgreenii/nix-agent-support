package main

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/setup"
)

// TRUE UNIT TESTS — this file is deliberately UNTAGGED and is the only test file
// in package main that a default `go test ./...` sees. Everything here runs
// entirely IN PROCESS: no compiled binary is exec'd and no SQLite store is
// created, so nothing here performs a durable write. All five measured 0.00s on
// the host whose degraded array blew the suite's 10m budget (2026-08-16).
//
// KEEP IT THAT WAY. A test added here that execs the binary or calls
// asklog.NewStore puts fsync latency back on the deploy path — the package build
// runs mkGoApp scoped to THIS package, so this file is what a monorepod
// nixosConfiguration build actually executes. New CLI-level or store-backed
// tests belong in cmd_evaluate_integration_test.go instead.

// --- grading vocabulary: unit tests over categorize/outcomeToExpectedDecision ---

// TestOutcomeToExpectedDecision_ThreeWayDistinction pins the mapping for all
// five outcomes, in both directions: the three that record a decision get an
// expected hook decision, and the two that record none get "" so nothing can be
// graded against them.
func TestOutcomeToExpectedDecision_ThreeWayDistinction(t *testing.T) {
	for _, tc := range []struct {
		outcome string
		want    string
	}{
		{asklog.OutcomeApproved, "allow"},
		{asklog.OutcomeDenied, "deny"},   // somebody declined it
		{asklog.OutcomeRejected, "deny"}, // the hook refused it itself
		{asklog.OutcomePending, ""},      // not resolved yet
		{asklog.OutcomeUnresolved, ""},   // never resolved at all
	} {
		if got := outcomeToExpectedDecision(tc.outcome); got != tc.want {
			t.Errorf("outcomeToExpectedDecision(%q) = %q, want %q", tc.outcome, got, tc.want)
		}
	}
}

// TestCategorize_UnresolvedIsNeverCorrectAndNeverAMiss is the core invariant of
// the split: a row nobody ever resolved carries no ground truth, so WHATEVER the
// engine now replays it to, the row is categorized "unresolved" — never
// "correct" (which would credit the engine for a decision nobody made) and never
// a "miss-*" (which would blame it for one).
func TestCategorize_UnresolvedIsNeverCorrectAndNeverAMiss(t *testing.T) {
	for _, replay := range []string{"allow", "deny", "ask", "abstain", ""} {
		for _, settings := range []string{"", "allow"} {
			row := asklog.DecisionRow{Outcome: asklog.OutcomeUnresolved}
			got := categorize(evalResult{ReplayResult: replay, SettingsResult: settings}, row)
			if got != "unresolved" {
				t.Errorf("replay=%q settings=%q: category = %q, want unresolved", replay, settings, got)
			}
		}
	}
}

// TestCategorize_ThreeWayDistinction pins the grading consequence of each of the
// three refusal-shaped outcomes against the SAME replay results, which is what
// makes them worth distinguishing at all.
func TestCategorize_ThreeWayDistinction(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outcome string
		replay  string
		want    string
	}{
		// A real decline: replaying to deny agrees with the human.
		{"denied + replay deny", asklog.OutcomeDenied, "deny", "correct"},
		// ...but replaying to allow is ambiguous (the user may have redirected
		// rather than truly rejected), so it stays needs-review.
		{"denied + replay allow", asklog.OutcomeDenied, "allow", "needs-review"},
		{"denied + replay abstain", asklog.OutcomeDenied, "abstain", "miss-uncaught"},

		// A hook Reject: no user was involved, so replaying to deny is a
		// self-consistency pass and replaying to allow is a real engine change
		// that MUST stay visible as a miss — the redirection carve-out does not
		// apply here.
		{"rejected + replay deny", asklog.OutcomeRejected, "deny", "correct"},
		{"rejected + replay allow", asklog.OutcomeRejected, "allow", "miss-uncaught"},

		// Never resolved: not gradeable in either direction.
		{"unresolved + replay deny", asklog.OutcomeUnresolved, "deny", "unresolved"},
		{"unresolved + replay allow", asklog.OutcomeUnresolved, "allow", "unresolved"},

		// Not resolved YET keeps its historical needs-review classification.
		{"pending + replay allow", asklog.OutcomePending, "allow", "needs-review"},
	} {
		row := asklog.DecisionRow{Outcome: tc.outcome}
		if got := categorize(evalResult{ReplayResult: tc.replay}, row); got != tc.want {
			t.Errorf("%s: category = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestCategorize_CorrectDecisionOutranksUnresolved: an explicit human annotation
// IS ground truth, so it must still be graded even on a never-resolved row.
func TestCategorize_CorrectDecisionOutranksUnresolved(t *testing.T) {
	deny := "deny"
	row := asklog.DecisionRow{Outcome: asklog.OutcomeUnresolved, CorrectDec: &deny}

	if got := categorize(evalResult{ReplayResult: "deny"}, row); got != "correct" {
		t.Errorf("annotated unresolved row, replay deny: category = %q, want correct", got)
	}
	if got := categorize(evalResult{ReplayResult: "allow"}, row); got != "miss-uncaught" {
		t.Errorf("annotated unresolved row, replay allow: category = %q, want miss-uncaught", got)
	}
}

// TestReplayAttribution_IsNotTheFirstTraceEntry is the same invariant one level
// down, where the alternative implementation is actually visible: with tracing
// on, the deciding RuleResult also carries a chronological Trace whose FIRST
// entry is whichever rule ran first — an abstaining one. Reading attribution off
// trace[0] would therefore name the wrong module, which is why runEvaluate reads
// it off the returned result instead.
func TestReplayAttribution_IsNotTheFirstTraceEntry(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	eng := setup.NewEngineForCWD(cwd)
	eng.SetTrace(true)
	result := eng.EvaluateHook(&hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: json.RawMessage(`{"command":"unknown-cmd-xyz && cat .env"}`),
		CWD:       cwd,
	})

	if decisionToDBString(result.Decision) != "ask" {
		t.Fatalf("decision = %v (%s), want ask", result.Decision, result.Reason)
	}
	if result.Module != "secrets" {
		t.Fatalf("result.Module = %q, want secrets", result.Module)
	}
	if len(result.Trace) == 0 {
		t.Fatal("no trace collected; this test cannot distinguish the two attributions")
	}
	first := result.Trace[0]
	if first.RuleName == result.Module {
		t.Fatalf("trace[0] is already the deciding rule (%q), so this test proves nothing; pick a command whose first-running rule abstains", first.RuleName)
	}
	if first.Decision != hookio.NoOpinion {
		t.Errorf("trace[0] %s decided %v; expected an abstain, i.e. a rule that did NOT decide this input", first.RuleName, first.Decision)
	}
}
