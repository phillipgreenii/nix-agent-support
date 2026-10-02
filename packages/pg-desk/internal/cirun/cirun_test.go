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
