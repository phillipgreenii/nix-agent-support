package rules

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/decide"
	"github.com/phillipgreenii/pg-decider/internal/view"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

const landreadyRuleID = "land.ready"

func landreadyFixtures(t *testing.T) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join("testdata", "landready", "*.json"))
	if err != nil || len(m) == 0 {
		t.Fatalf("no landready fixtures: %v", err)
	}
	var names []string
	for _, p := range m {
		names = append(names, p[len(filepath.Join("testdata", "landready"))+1:len(p)-len(".json")])
	}
	return names
}

func landreadyLoadView(t *testing.T, name string) *view.View {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "landready", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := view.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func landreadyEvaluate(t *testing.T, name string) decide.Result {
	t.Helper()
	v := landreadyLoadView(t, name)
	in := decide.Input{View: v, Items: workitem.BuildIndex(v), EntityType: decide.EntityTypePR}
	return landReadyRule{}.Evaluate(in)
}

func landreadyWantAnnotate(t *testing.T, name, value string) {
	t.Helper()
	res := landreadyEvaluate(t, name)
	if len(res.Actions) != 1 || res.Skip != nil {
		t.Fatalf("%s: want exactly one annotate, got %+v", name, res)
	}
	a := res.Actions[0]
	if a.Op != action.OpAnnotate || a.Target == nil || *a.Target != "ready_to_land" ||
		a.Fields.Value == nil || *a.Fields.Value != value || a.Fields.Clear || a.Kind != "" {
		t.Fatalf("%s: action = %+v", name, a)
	}
}

func landreadyWantSkip(t *testing.T, name, reason string) {
	t.Helper()
	res := landreadyEvaluate(t, name)
	if len(res.Actions) != 0 || res.Skip == nil || res.Skip.Reason != reason {
		t.Fatalf("%s: want no action and skip %q, got %+v (skip %+v)", name, reason, res.Actions, res.Skip)
	}
}

func TestLandReadyRegisteredForPR(t *testing.T) {
	var found bool
	for _, r := range decide.RulesFor(decide.EntityTypePR) {
		if r.ID() == landreadyRuleID {
			found = true
			if r.Kind() != "" {
				t.Fatalf("Kind() = %q, want none", r.Kind())
			}
		}
	}
	if !found {
		t.Fatalf("rule %q is not registered for %q", landreadyRuleID, decide.EntityTypePR)
	}
	if decide.OrdinalLandReady != 100 {
		t.Fatalf("ordinal constant = %d", decide.OrdinalLandReady)
	}
}

func TestLandReadyAnnotatesTrue(t *testing.T) {
	for _, name := range []string{
		"ready_mine", "ready_coowned", "ready_lowercase", "ready_annotated_false",
		"with_open_work_item",
	} {
		t.Run(name, func(t *testing.T) { landreadyWantAnnotate(t, name, "true") })
	}
}

func TestLandReadyAnnotatesFalseWhenNoLongerReady(t *testing.T) {
	for _, name := range []string{
		"draft_annotated_true", "not_approved_annotated_true",
		"rollup_pending_annotated_true", "ci_empty_runs_annotated_true",
	} {
		t.Run(name, func(t *testing.T) { landreadyWantAnnotate(t, name, "false") })
	}
}

func TestLandReadyAlreadyInTheRightState(t *testing.T) {
	landreadyWantSkip(t, "ready_annotated_true", action.ReasonAlreadyHandled)
	landreadyWantSkip(t, "not_ready_annotated_false", action.ReasonNotMatched)
}

func TestLandReadyNotMatched(t *testing.T) {
	for _, name := range []string{
		"ready_team", "team_annotated_true",
		"draft", "not_approved", "not_approved_no_decision",
		"rollup_failure", "ci_failing", "ci_pending", "ci_empty_runs", "ci_absent",
	} {
		t.Run(name, func(t *testing.T) { landreadyWantSkip(t, name, action.ReasonNotMatched) })
	}
}

func TestLandReadyNeverCreatesWorkItem(t *testing.T) {
	for _, name := range landreadyFixtures(t) {
		t.Run(name, func(t *testing.T) {
			for _, a := range landreadyEvaluate(t, name).Actions {
				if a.Op != action.OpAnnotate {
					t.Fatalf("non-annotate action: %+v", a)
				}
			}
		})
	}
}

// Idempotency (G6): the after fixture is hand-written to represent the state
// once the first fixture's annotation was applied.
func TestLandReadyIdempotentOnAppliedState(t *testing.T) {
	cases := []struct{ before, after string }{
		{"ready_mine", "ready_annotated_true"},
		{"ready_coowned", "ready_annotated_true"},
		{"ready_annotated_false", "ready_annotated_true"},
		{"draft_annotated_true", "not_ready_annotated_false"},
	}
	for _, tc := range cases {
		t.Run(tc.before, func(t *testing.T) {
			if res := landreadyEvaluate(t, tc.before); len(res.Actions) != 1 {
				t.Fatalf("before: %+v", res)
			}
			if after := landreadyEvaluate(t, tc.after); len(after.Actions) != 0 {
				t.Fatalf("after %s still acts: %+v", tc.after, after.Actions)
			}
		})
	}
}
