package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/setup"
)

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

// --- CLI-level: the same invariant through evaluate and report ---

// setupOutcomeSplitDB seeds one row per refusal provenance plus a pending row.
// cwd is the temp dir so nothing is classified stale-cwd. "unknown-cmd-xyz" is
// a command no rule module recognizes, so it replays to abstain — i.e. it is a
// miss for any outcome that IS gradeable.
func setupOutcomeSplitDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	store, err := asklog.NewStore(filepath.Join(dir, "claude-extended-tool-approver", "asks.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.DB().Exec(`INSERT INTO tool_decisions
		(id, session_id, cwd, tool_name, tool_input_hash, tool_input_json, tool_summary,
		 hook_decision, outcome, outcome_notes, created_at)
		VALUES
		 (1,'s',?,'Bash','h1','{"command":"unknown-cmd-xyz"}','unknown-cmd-xyz',
		  'abstain','unresolved',NULL,'2026-01-01T00:00:00Z'),
		 (2,'s',?,'Bash','h2','{"command":"unknown-cmd-xyz"}','unknown-cmd-xyz',
		  'abstain','pending',NULL,'2026-01-01T00:00:00Z'),
		 (3,'s',?,'Bash','h3','{"command":"unknown-cmd-xyz"}','unknown-cmd-xyz',
		  'abstain','denied','auto_mode_classifier: user declined','2026-01-01T00:00:00Z')`,
		dir, dir, dir)
	if err != nil {
		t.Fatalf("seed rows: %v", err)
	}
	_ = store.Close()
	return dir
}

// TestEvaluate_UnresolvedIsItsOwnCategoryAndNotAMiss: the seeded rows all replay
// to the same thing, so any difference in category comes purely from the outcome
// vocabulary. Only the genuinely-declined row may be a miss.
func TestEvaluate_UnresolvedIsItsOwnCategoryAndNotAMiss(t *testing.T) {
	dataDir := setupOutcomeSplitDB(t)

	out, err := runSubcommand(t, dataDir, "evaluate", "--format=json")
	if err != nil {
		t.Fatalf("evaluate failed: %v\noutput: %s", err, out)
	}
	jsonStart := strings.IndexByte(string(out), '[')
	if jsonStart < 0 {
		t.Fatalf("no JSON array in output: %s", out)
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out)[jsonStart:])), &rows); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
	}
	byID := map[float64]map[string]interface{}{}
	for _, r := range rows {
		byID[r["id"].(float64)] = r
	}
	for id, want := range map[float64]string{1: "unresolved", 2: "needs-review", 3: "miss-uncaught"} {
		got, _ := byID[id]["category"].(string)
		if got != want {
			t.Errorf("row %v category = %q, want %q (replay=%v)", id, got, want, byID[id]["replay_result"])
		}
	}

	// --misses-only must drop the unresolved row entirely, like stale-cwd.
	out, err = runSubcommand(t, dataDir, "evaluate", "--format=json", "--misses-only")
	if err != nil {
		t.Fatalf("evaluate --misses-only failed: %v\noutput: %s", err, out)
	}
	jsonStart = strings.IndexByte(string(out), '[')
	var missRows []map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out)[jsonStart:])), &missRows); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
	}
	for _, r := range missRows {
		if r["id"].(float64) == 1 {
			t.Errorf("--misses-only included the unresolved row: %v", r)
		}
	}
}

// --- per-site attribution: replay_module / replay_reason ---

// staleHookReason is planted in the hook_reason column of every attribution row.
// That column records what an OLDER binary decided, so no assertion below may
// ever see this string — attribution comes from the replay verdict alone.
const staleHookReason = "STALE: what an older binary said"

// setupAttributionDB seeds the three shapes per-site attribution has to answer:
// a plain asking row, a COMPOUND whose FIRST leaf abstains and whose second
// asks, and a row no rule owns at all. cwd is the temp dir so nothing is
// classified stale-cwd. `cat .env` is owned by the secrets rule (band 5, well
// ahead of safe-commands), and "unknown-cmd-xyz" is recognized by no rule.
func setupAttributionDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	store, err := asklog.NewStore(filepath.Join(dir, "claude-extended-tool-approver", "asks.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.DB().Exec(`INSERT INTO tool_decisions
		(id, session_id, cwd, tool_name, tool_input_hash, tool_input_json, tool_summary,
		 hook_decision, hook_reason, outcome, created_at)
		VALUES
		 (1,'s',?,'Bash','h1','{"command":"cat .env"}','cat .env',
		  'ask',?,'approved','2026-01-01T00:00:00Z'),
		 (2,'s',?,'Bash','h2','{"command":"unknown-cmd-xyz && cat .env"}','unknown-cmd-xyz && cat .env',
		  'ask',?,'approved','2026-01-01T00:00:00Z'),
		 (3,'s',?,'Bash','h3','{"command":"unknown-cmd-xyz"}','unknown-cmd-xyz',
		  'abstain',?,'approved','2026-01-01T00:00:00Z')`,
		dir, staleHookReason, dir, staleHookReason, dir, staleHookReason)
	if err != nil {
		t.Fatalf("seed rows: %v", err)
	}
	_ = store.Close()
	return dir
}

// runSubcommandNoConsumerConfig is runSubcommand with the consumer rules.json
// pointed at an empty directory, so every rule sits at its base generic behavior.
// Attribution assertions need that: an `approvedCommands` entry is ABSOLUTE for
// its leaf and skips the whole early security band (ADR 0040), so whatever
// rules.json the developer's machine happens to carry could otherwise decide
// which module is credited.
func runSubcommandNoConsumerConfig(t *testing.T, dataDir string, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command(cliBinary, args...)
	cmd.Env = append(os.Environ(), "XDG_DATA_HOME="+dataDir, "XDG_CONFIG_HOME="+t.TempDir())
	return cmd.CombinedOutput()
}

// evaluateJSONByID runs `evaluate --format=json` and indexes the rows by id.
func evaluateJSONByID(t *testing.T, dataDir string, args ...string) map[float64]map[string]interface{} {
	t.Helper()
	out, err := runSubcommandNoConsumerConfig(t, dataDir, append([]string{"evaluate", "--format=json"}, args...)...)
	if err != nil {
		t.Fatalf("evaluate failed: %v\noutput: %s", err, out)
	}
	jsonStart := strings.IndexByte(string(out), '[')
	if jsonStart < 0 {
		t.Fatalf("no JSON array in output: %s", out)
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out)[jsonStart:])), &rows); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
	}
	byID := map[float64]map[string]interface{}{}
	for _, r := range rows {
		byID[r["id"].(float64)] = r
	}
	return byID
}

// TestEvaluate_JSONCarriesReplayAttribution is the acceptance check for ADR
// 0043's blocking prerequisite: `evaluate --format json` must name the rule its
// replay verdict came from, so a per-site ask count is derivable without a
// second pass through the binary in hook mode.
func TestEvaluate_JSONCarriesReplayAttribution(t *testing.T) {
	byID := evaluateJSONByID(t, setupAttributionDB(t))

	row := byID[1]
	if got := row["replay_result"]; got != "ask" {
		t.Fatalf("row 1 replay_result = %v, want ask (raw: %v)", got, row)
	}
	if got, _ := row["replay_module"].(string); got != "secrets" {
		t.Errorf("row 1 replay_module = %q, want secrets", got)
	}
	reason, _ := row["replay_reason"].(string)
	if !strings.Contains(reason, "credential/secret path") {
		t.Errorf("row 1 replay_reason = %q, want the secrets rule's own reason", reason)
	}

	// A row nobody owns replays to the engine's manufactured terminal abstain,
	// which carries no module — and that empty pair IS the attribution "the chain
	// was exhausted with no rule having an opinion". Asserted so the two `{}`
	// provenances stay distinguishable: an engine FLOOR (heredoc, unparseable
	// text, assignments-only) names module "engine" instead.
	if got := byID[3]["replay_result"]; got != "abstain" {
		t.Fatalf("row 3 replay_result = %v, want abstain (raw: %v)", got, byID[3])
	}
	if got, ok := byID[3]["replay_module"]; !ok || got != "" {
		t.Errorf("row 3 replay_module = %v (present=%v), want an empty string", got, ok)
	}
}

// TestEvaluate_AttributionIsNotTheStoredHookReason guards the whole point of the
// change: hook_reason records what an OLDER binary decided, so attributing
// today's verdict to it would credit yesterday's rule.
func TestEvaluate_AttributionIsNotTheStoredHookReason(t *testing.T) {
	for id, row := range evaluateJSONByID(t, setupAttributionDB(t)) {
		if reason, _ := row["replay_reason"].(string); strings.Contains(reason, staleHookReason) {
			t.Errorf("row %v replay_reason = %q; it must come from the replay verdict, never from the stored hook_reason column", id, reason)
		}
	}
}

// TestEvaluate_AttributionIsTheFinalDecidingRule pins the compound case at the
// CLI boundary: the first leaf of `unknown-cmd-xyz && cat .env` is owned by no
// rule and contributes an abstain with no module, and the SECOND leaf is what
// the most-restrictive fold returns. Crediting the first thing that happened
// would attribute the ask to nothing at all.
func TestEvaluate_AttributionIsTheFinalDecidingRule(t *testing.T) {
	row := evaluateJSONByID(t, setupAttributionDB(t))[2]

	if got := row["replay_result"]; got != "ask" {
		t.Fatalf("compound replay_result = %v, want ask (raw: %v)", got, row)
	}
	if got, _ := row["replay_module"].(string); got != "secrets" {
		t.Errorf("compound replay_module = %q, want secrets (the leaf that WON the fold, not the abstaining first leaf)", got)
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
	if first.Decision != hookio.Abstain {
		t.Errorf("trace[0] %s decided %v; expected an abstain, i.e. a rule that did NOT decide this input", first.RuleName, first.Decision)
	}
}

// TestReport_MissesOnly_SkipsOutcomesThatRecordNoDecision: report grades on the
// stored hook_decision alone, so an unresolved/pending row has nothing to
// disagree with and MUST NOT appear in the miss set.
func TestReport_MissesOnly_SkipsOutcomesThatRecordNoDecision(t *testing.T) {
	dataDir := setupOutcomeSplitDB(t)

	out, err := runSubcommand(t, dataDir, "report", "--misses-only", "--group-by=outcome", "--format=json")
	if err != nil {
		t.Fatalf("report failed: %v\noutput: %s", err, out)
	}
	jsonStart := strings.IndexByte(string(out), '[')
	if jsonStart < 0 {
		t.Fatalf("no JSON array in output: %s", out)
	}
	var entries []map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out)[jsonStart:])), &entries); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
	}

	counts := map[string]float64{}
	for _, e := range entries {
		counts[e["key"].(string)] = e["count"].(float64)
	}
	if counts[asklog.OutcomeDenied] != 1 {
		t.Errorf("denied misses = %v, want 1 (a real decline the hook disagreed with)", counts[asklog.OutcomeDenied])
	}
	for _, notADecision := range []string{asklog.OutcomeUnresolved, asklog.OutcomePending} {
		if n, ok := counts[notADecision]; ok {
			t.Errorf("%q counted as %v misses; an outcome that records no decision cannot be a miss", notADecision, n)
		}
	}
}
