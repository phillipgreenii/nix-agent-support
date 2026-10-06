package parity

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
)

// rule is one expected action: op, kind and the rule that produced it.
type rule struct{ op, kind, rule string }

// newWantActions is, per scenario, the actions the NEW decider must plan (a
// subset: other actions may follow), and newWantAbsent the ones it must not.
var newWantActions = map[string][]rule{
	"01-": {{"create", "anchor", "anchor.lazy"}, {"create", "review-pr", "review.head-advanced"}, {"create", "process-feedback", "feedback.digest-changed"}},
	"02-": {{"create", "anchor", "anchor.lazy"}, {"create", "review-pr", "review.head-advanced"}},
	"03-": {{"create", "anchor", "anchor.lazy"}, {"create", "review-pr", "review.head-advanced"}},
	"04-": nil,
	"05-": nil,
	"06-": {{"create", "fix-ci", "fixci.failing-on-head"}},
	"07-": {{"create", "resolve-conflict", "conflict.present"}},
	"08-": {{"annotate", "", "land.ready"}},
	"09-": {{"close", "anchor", "all.closed"}, {"close", "review-pr", "all.closed"}, {"close", "process-feedback", "all.closed"}},
	"10-": {{"update", "anchor", "adoption"}, {"update", "review-pr", "adoption"}, {"update", "process-feedback", "adoption"}},
	"11-": {{"reopen", "anchor", "all.reopened"}},
	"12-": {{"reopen", "review-pr", "review.head-advanced"}},
	"13-": {{"update", "review-pr", "adoption"}, {"update", "process-feedback", "adoption"}},
	"14-": {{"create", "process-feedback", "feedback.digest-changed"}},
	"15-": {{"create", "process-feedback", "feedback.digest-changed"}},
}

var newWantAbsent = map[string][]rule{
	"02-": {{"create", "process-feedback", "feedback.digest-changed"}},
	"03-": {{"create", "process-feedback", "feedback.digest-changed"}},
	"10-": {{"create", "anchor", "anchor.lazy"}, {"create", "review-pr", "review.head-advanced"}},
	// The legacy anchor has no repo or pr_number metadata, so nothing links it
	// to the PR on the new side: it is neither adopted nor found.
	"13-": {{"update", "anchor", "adoption"}},
	// S26: the closed review request for the unchanged head is not reopened,
	// and the one comment the closed cycle covered does not re-trigger it.
	"14-": {{"reopen", "review-pr", "review.head-advanced"}, {"reopen", "process-feedback", "feedback.digest-changed"}},
	// The new side reads closed items however the work-beads listing is
	// filtered, so scenario 15 plans exactly what scenario 14 does.
	"15-": {{"create", "review-pr", "review.head-advanced"}, {"reopen", "review-pr", "review.head-advanced"}, {"reopen", "process-feedback", "feedback.digest-changed"}},
}

func hasAction(p action.PlanResult, r rule) bool {
	for _, a := range p.Actions {
		if string(a.Op) == r.op && a.Kind == r.kind && a.Rule == r.rule {
			return true
		}
	}
	return false
}

func TestRunNewPlansWhatTheDeciderPlans(t *testing.T) {
	env := realEnv(t)
	scs, err := Scenarios()
	if err != nil {
		t.Fatal(err)
	}
	for _, sc := range scs {
		t.Run(sc.Name, func(t *testing.T) {
			want := wantFor(t, newWantActions, sc.Name)
			res, err := RunNew(context.Background(), env, sc)
			if err != nil {
				t.Fatalf("RunNew: %v", err)
			}
			for _, id := range sc.Entities {
				plan, ok := res.Plans[id]
				if !ok {
					t.Fatalf("no plan for %s: %v", id, res.Plans)
				}
				if plan.Actions == nil || plan.Skipped == nil {
					t.Errorf("%s: a plan list is nil (printed lists are arrays, never null): %+v", id, plan)
				}
				for _, r := range want {
					if !hasAction(plan, r) {
						t.Errorf("%s: no %s %s action from %s in %+v", id, r.op, r.kind, r.rule, plan.Actions)
					}
				}
				for _, r := range newWantAbsent[sc.Name[:3]] {
					if hasAction(plan, r) {
						t.Errorf("%s: unexpected %s %s action from %s", id, r.op, r.kind, r.rule)
					}
				}
				if len(want) == 0 && len(plan.Actions) != 0 {
					t.Errorf("%s: want no actions, got %+v", id, plan.Actions)
				}
			}
		})
	}
}

func TestRunNewSkipsAHiddenEntityForEveryRule(t *testing.T) {
	env := realEnv(t)
	sc := scenarioByPrefix(t, "05-")
	res, err := RunNew(context.Background(), env, sc)
	if err != nil {
		t.Fatal(err)
	}
	plan := res.Plans["acme/api#105"]
	if len(plan.Skipped) == 0 {
		t.Fatal("no skips")
	}
	for _, s := range plan.Skipped {
		if s.Reason != action.ReasonHidden {
			t.Errorf("rule %s skipped for %q, want %q", s.Rule, s.Reason, action.ReasonHidden)
		}
	}
}

func TestRunNewIsDeterministic(t *testing.T) {
	env := realEnv(t)
	sc := scenarioByPrefix(t, "06-")
	a, err := RunNew(context.Background(), env, sc)
	if err != nil {
		t.Fatal(err)
	}
	b, err := RunNew(context.Background(), env, sc)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("two runs of one scenario differ:\n%+v\n%+v", a, b)
	}
}

func TestRunNewIsHermetic(t *testing.T) {
	env := realEnv(t)
	realState, realHome := t.TempDir(), t.TempDir()
	t.Setenv("XDG_STATE_HOME", realState)
	t.Setenv("HOME", realHome)
	t.Setenv("PG_DESK_CONFIG", filepath.Join(realHome, "no-such-config.yaml"))

	sc := scenarioByPrefix(t, "10-")
	if _, err := RunNew(context.Background(), env, sc); err != nil {
		t.Fatalf("RunNew: %v", err)
	}
	for _, dir := range []string{realState, realHome} {
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("%s was written to: %v", dir, entries)
		}
	}
	assertRunArtifacts(t, env.TempDir, "new-", []string{
		"pr show acme/api#110 --fresh", "issue show bd-110a --fresh", "issue show bd-110r --fresh", "issue show bd-110f --fresh",
	})
}
