package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/trackeradapter"
)

// The grading fixture (internal/trackeradapter/testdata/tracker_grading.sql) is
// built by the real claude-tool-tracker ingest over grading-events/ (see
// gen_grading.sh). It holds one call for each row of the settlement grading
// table, a converted ask, a call that ran and errored, a refused call, two calls
// CETA settled itself and an unresolved call. call_id -> tool_use_id: 1 g1, 3 g2,
// 5 g3, 7 g4, 9 g5 (converted ask), 11 g6, 13 g7, 15 g8, 17 g9, 19 g10, 21 g11,
// 23 g12 (errored), 25 g13, 27 g14 (unresolved), 29 g15 and 31 g16 (CETA settled).

func gradingRows(t *testing.T) map[int]trackeradapter.Row {
	t.Helper()
	a, err := trackeradapter.Open(trackerFixture(t, "tracker_grading.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	rows, err := a.Rows("", nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int]trackeradapter.Row{}
	for _, r := range rows {
		out[r.CallID] = r
	}
	return out
}

func TestSettleGrade_Golden(t *testing.T) {
	seedCetaHistory(t)
	db := trackerFixture(t, "tracker_grading.db")
	cases := []struct {
		name string
		args []string
	}{
		{"grading.evaluate.summary", []string{"evaluate"}},
		{"grading.evaluate.json", []string{"evaluate", "--format", "json"}},
		{"grading.report.grading.table", []string{"report", "--group-by", "grading"}},
		{"grading.report.grading.json", []string{"report", "--group-by", "grading", "--format", "json"}},
		{"grading.report.settled_by.json", []string{"report", "--group-by", "settled_by", "--format", "json"}},
		{"grading.report.settlement.json", []string{"report", "--group-by", "settlement", "--format", "json"}},
		{"grading.show.json", []string{"show", "--format", "json", "1", "9", "23", "27", "29"}},
		{"grading.show.table", []string{"show", "1", "5", "9", "27"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := runCLI(t, append(append([]string{}, tc.args...), "--source", "tracker", "--tracker-db", db)...)
			checkTrackerGolden(t, tc.name, out)
		})
	}
}

// Every row of the design's grading table is populated by exactly the fixture
// call meant for it, and the converted ask sits in the ask rows.
func TestSettleGrade_PlacesEveryTableRow(t *testing.T) {
	seedCetaHistory(t)
	db := trackerFixture(t, "tracker_grading.db")
	var rows []evalResult
	raw := runCLI(t, "evaluate", "--format", "json", "--source", "tracker", "--tracker-db", db)
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	want := map[int]string{
		1: "ask/person/approved", 3: "ask/person/approved", 5: "ask/person/approved", 7: "ask/person/refused",
		9: "ask/converted", 11: "abstain/classifier/approved", 13: "abstain/classifier/refused",
		15: "abstain/person", 17: "abstain/person", 19: "abstain/rule-or-mode", 21: "abstain/rule-or-mode",
		23: "ask/person/approved", 25: "ask/person/refused", 27: "", 29: "", 31: "",
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %d, want %d", len(rows), len(want))
	}
	for _, r := range rows {
		if r.GradingCell != want[r.ID] {
			t.Errorf("call %d: grading_cell %q, want %q", r.ID, r.GradingCell, want[r.ID])
		}
	}
	// the converted ask is an ask for the analysis: engine verdict ask
	for _, r := range rows {
		if r.ID == 9 && (r.EngineVerdict != "ask" || r.ConvertedAsk == nil || !*r.ConvertedAsk || r.SettledBy != "ceta") {
			t.Errorf("converted ask row: %+v", r)
		}
	}
	// the text table lists the ranked groups and the two counted totals
	out := runCLI(t, "evaluate", "--source", "tracker", "--tracker-db", db)
	for _, frag := range []string{
		fmt.Sprintf("  %-30s %5d\n      %5d  git push origin main\n      %5d  Write\n      %5d  go test ./...\n", "ask/person/approved", 4, 2, 1, 1),
		fmt.Sprintf("  %-30s %5d\n      %5d  curl example.invalid\n", "ask/converted", 1, 1),
		fmt.Sprintf("  %-30s %5d\n", "settled by CETA (not ranked)", 2),
		fmt.Sprintf("  %-30s %5d\n", "not placed (pending/unresolved/other)", 1),
	} {
		if !strings.Contains(out, frag) {
			t.Errorf("summary lacks %q:\n%s", frag, out)
		}
	}
}

// The correctness check is graded against settlement: approved is allow,
// refused is deny, a call that ran and errored is allow, and pending or
// unresolved are left out of the grading.
func TestSettleGrade_CorrectnessGradedAgainstSettlement(t *testing.T) {
	rows := gradingRows(t)
	grade := func(id int, replay string) string {
		r := rows[id]
		return categorize(evalResult{ReplayResult: replay, Settlement: r.Settlement}, r.DecisionRow)
	}
	cases := []struct {
		name   string
		id     int
		replay string
		want   string
	}{
		{"approved grades as allow: allow", 1, "allow", "correct"},
		{"approved grades as allow: ask misses", 1, "ask", "miss-uncaught"},
		{"refused grades as deny: deny", 7, "deny", "correct"},
		{"refused grades as deny: allow is a user decline", 7, "allow", "needs-review"},
		{"errored call grades as allow: allow", 23, "allow", "correct"},
		{"errored call grades as allow: deny misses", 23, "deny", "miss-uncaught"},
		{"refused call is deny even when CETA refused it itself", 9, "deny", "correct"},
		{"CETA refusal replayed as allow is a miss", 9, "allow", "miss-uncaught"},
		{"unresolved is left out: any replay", 27, "allow", "unresolved"},
	}
	for _, c := range cases {
		if got := grade(c.id, c.replay); got != c.want {
			t.Errorf("%s: call %d replay %s graded %s, want %s", c.name, c.id, c.replay, got, c.want)
		}
	}
	if rows[23].Outcome != asklog.OutcomeFailed || rows[23].SettledBy != "person" {
		t.Errorf("errored call: outcome %q settled_by %q", rows[23].Outcome, rows[23].SettledBy)
	}
	// pending (no call_end yet, session may still run) is left out too
	pending := trackeradapter.Row{}
	pending.Settlement = "pending"
	pending.Outcome = asklog.OutcomePending
	if got := categorize(evalResult{ReplayResult: "allow", Settlement: "pending"}, pending.DecisionRow); got != "unresolved" {
		t.Errorf("pending graded %s, want unresolved", got)
	}
}

// show prints the stored ceta.trace; a call with no stored trace prints none.
func TestSettleGrade_ShowRendersStoredTrace(t *testing.T) {
	seedCetaHistory(t)
	db := trackerFixture(t, "tracker_grading.db")
	var res []showResult
	raw := runCLI(t, "show", "1", "--format", "json", "--source", "tracker", "--tracker-db", db)
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	if len(res) != 1 || len(res[0].Trace) != 1 || res[0].Trace[0].RuleName != "fixture-rule" ||
		res[0].Trace[0].Decision != "ask" || res[0].Trace[0].RuleOrder != 1 || res[0].GradingCell != "ask/person/approved" {
		t.Errorf("trace not rendered: %+v", res)
	}
	table := runCLI(t, "show", "1", "--source", "tracker", "--tracker-db", db)
	if !strings.Contains(table, "TRACE:") || !strings.Contains(table, "fixture-rule") || !strings.Contains(table, "SETTLED: person, approved") {
		t.Errorf("table lacks trace or settlement:\n%s", table)
	}
	// the older fixture has a call with no ceta.trace: no trace, no error
	older := trackerFixture(t, "tracker.db")
	raw = runCLI(t, "show", "16", "--format", "json", "--source", "tracker", "--tracker-db", older)
	res = nil
	if err := json.Unmarshal([]byte(raw), &res); err != nil || len(res) != 1 || len(res[0].Trace) != 0 {
		t.Errorf("call without trace: %v %+v", err, res)
	}
}

// The three commands the identify-hook-misses skill runs keep their documented
// interface under both sources: evaluate --misses-only --format=json (a bare
// array of rows with the documented fields), evaluate --baseline (capture, then
// compare) and show <ids> --format=json (an array keyed by id).
func TestSettleGrade_SkillInterfaceUnderBothSources(t *testing.T) {
	seedCetaHistory(t)
	db := trackerFixture(t, "tracker_grading.db")
	for _, source := range [][]string{{}, {"--source", "tracker", "--tracker-db", db}} {
		label := strings.Join(source, " ")
		args := func(a ...string) []string { return append(append([]string{}, a...), source...) }

		var misses []map[string]any
		raw := runCLI(t, args("evaluate", "--misses-only", "--format=json")...)
		if err := json.Unmarshal([]byte(raw), &misses); err != nil {
			t.Fatalf("[%s] misses is not a bare array: %v\n%s", label, err, raw)
		}
		// every call of these fixtures replays as stale-cwd, so the misses list is
		// empty: check the documented fields on the unfiltered rows too
		var every []map[string]any
		if err := json.Unmarshal([]byte(runCLI(t, args("evaluate", "--format=json")...)), &every); err != nil || len(every) == 0 {
			t.Fatalf("[%s] evaluate json: %v", label, err)
		}
		for _, f := range []string{"id", "tool_name", "command_class", "hook_decision", "category", "outcome", "approval_source", "permission_mode", "agent_type", "outcome_notes", "tool_response", "replay_result", "sandbox_enabled"} {
			for _, m := range every {
				if _, ok := m[f]; !ok {
					t.Errorf("[%s] evaluate row lacks %q: %v", label, f, m)
				}
			}
		}
		for _, m := range misses {
			if c := m["category"]; c == "correct" || c == "unresolved" || c == "retired-root" {
				t.Errorf("[%s] --misses-only kept category %v", label, c)
			}
		}

		base := filepath.Join(t.TempDir(), "base.json")
		runCLI(t, args("evaluate", "--format=json", "--baseline", base)...)
		out := runCLI(t, args("evaluate", "--format=json", "--baseline", base)...)
		var env map[string]any
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Fatalf("[%s] baseline compare output: %v\n%s", label, err, out)
		}

		var shown []map[string]any
		showID := "1"
		raw = runCLI(t, args("show", showID, "--format=json")...)
		if err := json.Unmarshal([]byte(raw), &shown); err != nil || len(shown) != 1 || shown[0]["id"] != float64(1) {
			t.Errorf("[%s] show json: %v\n%s", label, err, raw)
		}
	}
}

func TestSettleGrade_GroupByNeedsTracker(t *testing.T) {
	seedCetaHistory(t)
	for _, g := range []string{"grading", "settled_by", "settlement"} {
		root := newRootCmd()
		root.SetArgs([]string{"report", "--group-by", g})
		if err := root.Execute(); err == nil {
			t.Errorf("--group-by %s must need --source tracker", g)
		}
	}
}
