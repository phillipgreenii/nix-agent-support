package cirun

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
)

func payload(t *testing.T, runs ...map[string]any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]any{"runs": runs})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func run(name, conclusion, sha, id string, attempt int) map[string]any {
	r := map[string]any{"name": name, "status": "completed", "conclusion": conclusion, "id": id, "attempt": attempt, "url": "https://ci.example.invalid/run/" + id}
	if sha != "" {
		r["head_sha"] = sha
	}
	return r
}

func TestEvaluate_CountedRunsOutcomes(t *testing.T) {
	raw := payload(
		t,
		run("lint", "success", "h", "1", 1),
		run("unit", "failure", "h", "2", 1),
		run("e2e", "cancelled", "h", "3", 1),
		map[string]any{"name": "slow", "status": "in_progress", "id": "4", "attempt": 1, "head_sha": "h"},
		run("old", "failure", "stale", "5", 1),
	)
	got := Evaluate(raw, nil, "h")
	want := map[string]Outcome{"lint": Passed, "unit": Failed, "e2e": Failed, "slow": Pending}
	if len(got) != len(want) {
		t.Fatalf("got %d runs, want %d: %+v", len(got), len(want), got)
	}
	for _, c := range got {
		if want[c.Name] != c.Outcome {
			t.Errorf("%s: outcome %v, want %v", c.Name, c.Outcome, want[c.Name])
		}
	}
}

func TestEvaluate_ExcludesInterpreterMatchesAndCollapsesReruns(t *testing.T) {
	raw := payload(
		t,
		run("policy-bot: x", "failure", "h", "1", 1),
		run("unit", "failure", "h", "2", 1),
		run("unit", "success", "h", "3", 1),
	)
	got := Evaluate(raw, []config.CheckInterpreterConfig{{Patterns: []string{"^policy-bot"}, Type: "x"}}, "h")
	if len(got) != 1 || got[0].Name != "unit" || got[0].Outcome != Passed || got[0].ID != "3" {
		t.Fatalf("got %+v; want only the newest unit run, passed", got)
	}
}

func TestEvaluate_MalformedAndEmptyDegradeToNoRuns(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`not json`), json.RawMessage(`{}`)} {
		if got := Evaluate(raw, nil, "h"); !reflect.DeepEqual(got, []Counted(nil)) && len(got) != 0 {
			t.Errorf("Evaluate(%s) = %+v; want none", raw, got)
		}
	}
}

func TestCompileExcluder_BadPatternIgnored(t *testing.T) {
	ex := CompileExcluder([]config.CheckInterpreterConfig{{Patterns: []string{"(", "^bot"}}})
	if !ex("bot-1") || ex("other") {
		t.Fatalf("excluder wrong: bot-1=%v other=%v", ex("bot-1"), ex("other"))
	}
}

func TestOnlyExemptJobsFailed(t *testing.T) {
	isExempt := CompileExempt([]string{"slow-nightly"})
	job := func(name, conclusion string) Job { return Job{Name: name, Status: "completed", Conclusion: conclusion} }
	cases := []struct {
		name string
		c    Counted
		want bool
	}{
		{"only exempt failed", Counted{Run: Run{Jobs: []Job{job("slow-nightly", "failure"), job("lint", "success")}}, Outcome: Failed}, true},
		{"exempt and other failed", Counted{Run: Run{Jobs: []Job{job("slow-nightly", "failure"), job("lint", "failure")}}, Outcome: Failed}, false},
		{"jobs not fetched", Counted{Run: Run{}, Outcome: Failed}, false},
		{"no failed job in a failed run", Counted{Run: Run{Jobs: []Job{job("slow-nightly", "success")}}, Outcome: Failed}, false},
		{"run not failed", Counted{Run: Run{Jobs: []Job{job("slow-nightly", "failure")}}, Outcome: Passed}, false},
		{"exact match only", Counted{Run: Run{Jobs: []Job{job("slow-nightly-2", "failure")}}, Outcome: Failed}, false},
	}
	for _, tc := range cases {
		if got := tc.c.OnlyExemptJobsFailed(isExempt); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
	if CompileExempt(nil)("slow-nightly") {
		t.Error("empty exempt list must exempt nothing")
	}
}

func TestEvaluate_DecodesJobs(t *testing.T) {
	r := run("PR Checks", "failure", "h", "1", 1)
	r["jobs"] = []map[string]any{{"id": "9", "name": "slow-nightly", "status": "completed", "conclusion": "failure", "url": "u"}}
	got := Evaluate(payload(t, r, run("Plain", "failure", "h", "2", 1)), nil, "h")
	if len(got) != 2 || len(got[0].Jobs) != 1 || got[0].Jobs[0].Name != "slow-nightly" || got[0].Jobs[0].ID != "9" {
		t.Fatalf("jobs not decoded: %+v", got)
	}
	if got[1].Jobs != nil {
		t.Fatalf("a run without jobs must stay not-fetched (nil): %+v", got[1].Jobs)
	}
}
