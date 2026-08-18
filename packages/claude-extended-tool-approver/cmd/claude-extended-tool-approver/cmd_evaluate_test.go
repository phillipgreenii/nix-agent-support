package main

import (
	"encoding/json"
	"os"
	"strings"
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

// --- --baseline: synthetic-corpus unit tests over the join/direction logic ---
//
// These construct baseline and current []evalResult slices directly — no
// asklog store, no compiled binary — which is both the fastest way to pin the
// join semantics and the isolated/synthetic fixture pg2-f1vss requires (never
// the real production asklog).

// TestClassifyDirection pins the two named transitions plus the fallback: only
// allow<->ask/deny gets a directional label, everything else (including any
// move through abstain) is "lateral", and a non-move is "".
func TestClassifyDirection(t *testing.T) {
	for _, tc := range []struct{ from, to, want string }{
		{"allow", "ask", "more-restrictive"},
		{"allow", "deny", "more-restrictive"},
		{"ask", "allow", "less-restrictive"},
		{"deny", "allow", "less-restrictive"},
		{"ask", "deny", "lateral"},
		{"deny", "ask", "lateral"},
		{"allow", "abstain", "lateral"},
		{"abstain", "allow", "lateral"},
		{"allow", "allow", ""},
		{"deny", "deny", ""},
	} {
		if got := classifyDirection(tc.from, tc.to); got != tc.want {
			t.Errorf("classifyDirection(%q, %q) = %q, want %q", tc.from, tc.to, got, tc.want)
		}
	}
}

// TestComputeBaselineDelta_SyntheticCorpus is the AC-pinned synthetic corpus:
// one row moves more-restrictive, one moves less-restrictive, one does not
// move, one is added (present only in current), one is removed (present only
// in baseline). The delta must report exactly the two moved rows with correct
// old/new verdicts and direction, exactly the added/removed rows, and nothing
// else — the unmoved row must not appear in Moved, Added, or Removed.
func TestComputeBaselineDelta_SyntheticCorpus(t *testing.T) {
	baseline := []evalResult{
		{ID: 1, ToolName: "Bash", ReplayResult: "allow"}, // -> more-restrictive
		{ID: 2, ToolName: "Bash", ReplayResult: "ask"},   // -> less-restrictive
		{ID: 3, ToolName: "Bash", ReplayResult: "deny"},  // unmoved
		{ID: 5, ToolName: "Bash", ReplayResult: "ask"},   // removed (absent from current)
	}
	current := []evalResult{
		{ID: 1, ToolName: "Bash", ReplayResult: "deny", ReplayModule: "secrets", ReplayReason: "now matches secrets"},
		{ID: 2, ToolName: "Bash", ReplayResult: "allow", ReplayModule: "config-rules", ReplayReason: "now approved"},
		{ID: 3, ToolName: "Bash", ReplayResult: "deny"},
		{ID: 4, ToolName: "Bash", ReplayResult: "allow"}, // added (absent from baseline)
	}

	delta := computeBaselineDelta(baseline, current)

	if len(delta.Moved) != 2 {
		t.Fatalf("Moved = %+v, want exactly 2 rows (ids 1 and 2)", delta.Moved)
	}
	byID := map[int]movedRow{}
	for _, m := range delta.Moved {
		byID[m.ID] = m
	}
	if m, ok := byID[1]; !ok {
		t.Error("row 1 (allow -> deny) missing from Moved")
	} else {
		if m.FromVerdict != "allow" || m.ToVerdict != "deny" {
			t.Errorf("row 1: from=%q to=%q, want allow -> deny", m.FromVerdict, m.ToVerdict)
		}
		if m.Direction != "more-restrictive" {
			t.Errorf("row 1: direction = %q, want more-restrictive", m.Direction)
		}
		if m.Module != "secrets" || m.Reason != "now matches secrets" {
			t.Errorf("row 1: module/reason = %q/%q, want the CURRENT run's attribution", m.Module, m.Reason)
		}
	}
	if m, ok := byID[2]; !ok {
		t.Error("row 2 (ask -> allow) missing from Moved")
	} else {
		if m.FromVerdict != "ask" || m.ToVerdict != "allow" {
			t.Errorf("row 2: from=%q to=%q, want ask -> allow", m.FromVerdict, m.ToVerdict)
		}
		if m.Direction != "less-restrictive" {
			t.Errorf("row 2: direction = %q, want less-restrictive", m.Direction)
		}
	}
	if _, moved := byID[3]; moved {
		t.Error("row 3 did not move (deny -> deny) but appeared in Moved")
	}

	if len(delta.Added) != 1 || delta.Added[0].ID != 4 {
		t.Errorf("Added = %+v, want exactly row 4", delta.Added)
	}
	if len(delta.Removed) != 1 || delta.Removed[0].ID != 5 {
		t.Errorf("Removed = %+v, want exactly row 5", delta.Removed)
	}
}

// TestMismatchedFilters pins the refuse-rather-than-silently-diff check: an
// identical filter set reports no mismatches, and a difference on any one of
// the five named filters is reported (not silently ignored).
func TestMismatchedFilters(t *testing.T) {
	base := evaluateFilters{Days: 3, Since: "2026-01-01", ApprovalSource: "user", MissesOnly: true, Settings: "/s.json"}

	if got := mismatchedFilters(base, base); len(got) != 0 {
		t.Errorf("identical filters: mismatches = %v, want none", got)
	}

	for _, tc := range []struct {
		name    string
		current evaluateFilters
	}{
		{"days", evaluateFilters{Days: 7, Since: base.Since, ApprovalSource: base.ApprovalSource, MissesOnly: base.MissesOnly, Settings: base.Settings}},
		{"since", evaluateFilters{Days: base.Days, Since: "2026-02-01", ApprovalSource: base.ApprovalSource, MissesOnly: base.MissesOnly, Settings: base.Settings}},
		{"approval-source", evaluateFilters{Days: base.Days, Since: base.Since, ApprovalSource: "auto", MissesOnly: base.MissesOnly, Settings: base.Settings}},
		{"misses-only", evaluateFilters{Days: base.Days, Since: base.Since, ApprovalSource: base.ApprovalSource, MissesOnly: false, Settings: base.Settings}},
		{"settings", evaluateFilters{Days: base.Days, Since: base.Since, ApprovalSource: base.ApprovalSource, MissesOnly: base.MissesOnly, Settings: "/other.json"}},
	} {
		if got := mismatchedFilters(base, tc.current); len(got) != 1 {
			t.Errorf("%s mismatch: got %v, want exactly 1 reported difference", tc.name, got)
		}
	}
}

// TestLoadEvaluateReport_RefusesBareArray: the DEFAULT `--format json` shape
// (no --baseline) is a bare array. Feeding that straight to --baseline (rather
// than a captured envelope) must refuse with a clear, actionable message, not
// silently treat it as an empty/unknown baseline.
func TestLoadEvaluateReport_RefusesBareArray(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/bare.json"
	if err := os.WriteFile(path, []byte(`[{"id":1,"tool_name":"Bash"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadEvaluateReport(path)
	if err == nil {
		t.Fatal("loadEvaluateReport on a bare array: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "bare evaluate array") {
		t.Errorf("error = %q, want it to name the bare-array shape so the operator knows how to fix it", err.Error())
	}
}

// TestEvaluateReport_WriteLoadRoundTrip: a captured envelope must survive a
// write/load round trip with its filters and results intact, since that is
// what the SECOND invocation of --baseline reads back to compute the delta.
func TestEvaluateReport_WriteLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/base.json"
	want := evaluateReport{
		CapturedAt: "2026-08-18T00:00:00Z",
		Filters:    evaluateFilters{Days: 3, ApprovalSource: "user"},
		Totals:     map[string]int{"correct": 2},
		Results:    []evalResult{{ID: 1, ToolName: "Bash", ReplayResult: "allow"}},
	}
	if err := writeEvaluateReport(path, want); err != nil {
		t.Fatalf("writeEvaluateReport: %v", err)
	}
	got, err := loadEvaluateReport(path)
	if err != nil {
		t.Fatalf("loadEvaluateReport: %v", err)
	}
	if got.Filters != want.Filters {
		t.Errorf("Filters = %+v, want %+v", got.Filters, want.Filters)
	}
	if len(got.Results) != 1 || got.Results[0].ID != 1 || got.Results[0].ReplayResult != "allow" {
		t.Errorf("Results = %+v, want the single seeded row back", got.Results)
	}
	if got.Delta != nil {
		t.Errorf("Delta = %+v, want nil on a freshly captured report", got.Delta)
	}
}
