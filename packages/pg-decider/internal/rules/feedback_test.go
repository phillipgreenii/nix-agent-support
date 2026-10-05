package rules

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/decide"
	"github.com/phillipgreenii/pg-decider/internal/view"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

const (
	feedbackD12  = "5ff7c41ebe23" // c-101,c-102
	feedbackD123 = "15b98a689116" // c-101,c-102,c-103
	feedbackD13  = "234aa3d824fd" // c-101,c-103
	feedbackD2   = "de7ef7ee219f" // c-102
)

func feedbackLoadView(t *testing.T, name string) *view.View {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "feedback", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := view.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func feedbackRule(t *testing.T) decide.Rule {
	t.Helper()
	for _, r := range decide.RulesFor(decide.EntityTypePR) {
		if r.ID() == feedbackRuleID {
			return r
		}
	}
	t.Fatalf("rule %s is not registered", feedbackRuleID)
	return nil
}

func feedbackEvaluate(t *testing.T, fixture string) decide.Result {
	t.Helper()
	v := feedbackLoadView(t, fixture)
	return feedbackRule(t).Evaluate(decide.Input{View: v, Items: workitem.BuildIndex(v), EntityType: decide.EntityTypePR})
}

func feedbackWantSkip(t *testing.T, fixture, reason string) {
	t.Helper()
	res := feedbackEvaluate(t, fixture)
	if len(res.Actions) != 0 {
		t.Fatalf("%s: want no action, got %+v", fixture, res.Actions)
	}
	if res.Skip == nil || res.Skip.Reason != reason {
		t.Fatalf("%s: want skip %q, got %+v", fixture, reason, res.Skip)
	}
}

func feedbackWantOne(t *testing.T, fixture string) action.Action {
	t.Helper()
	res := feedbackEvaluate(t, fixture)
	if len(res.Actions) != 1 {
		t.Fatalf("%s: want exactly one action, got %+v (skip %+v)", fixture, res.Actions, res.Skip)
	}
	return res.Actions[0]
}

func TestFeedbackRuleRegisteredForPR(t *testing.T) {
	r := feedbackRule(t)
	if r.Kind() != workitem.KindProcessFeedback {
		t.Errorf("kind = %q", r.Kind())
	}
}

func TestFeedbackCreateForMineAndCoOwned(t *testing.T) {
	for _, tc := range []struct {
		fixture, parent string
	}{
		{"mine_create", "bd-1"},
		{"coowned_create_no_anchor", action.AnchorParent},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			a := feedbackWantOne(t, tc.fixture)
			if a.Op != action.OpCreate || a.Kind != "process-feedback" || a.Target != nil {
				t.Fatalf("shape: %+v", a)
			}
			f := a.Fields
			if f.Title != "process-feedback: acme/widgets#42" || f.IssueType != "task" || f.Parent != tc.parent {
				t.Errorf("fields: %+v", f)
			}
			if !reflect.DeepEqual(f.Labels, []string{"mine", "fbsum:" + feedbackD12}) {
				t.Errorf("labels: %v", f.Labels)
			}
			wantMD := map[string]string{
				"repo": "acme/widgets", "pr_number": "42", "branch": "feature/retry",
				"covered_comments": "c-101,c-102",
				"dedup_key":        "pr:acme/widgets#42:process-feedback:" + feedbackD12,
			}
			if !reflect.DeepEqual(f.Metadata, wantMD) {
				t.Errorf("metadata: %v", f.Metadata)
			}
			if f.Description != fbsumCycleDescription("acme/widgets", 42, []string{"c-101", "c-102"}) {
				t.Errorf("description: %q", f.Description)
			}
		})
	}
}

func TestFeedbackUpdateOpenCycleWithNewUncoveredComment(t *testing.T) {
	a := feedbackWantOne(t, "open_cycle_new_comment")
	if a.Op != action.OpUpdate || a.Kind != "process-feedback" || a.Target == nil || *a.Target != "bd-2" {
		t.Fatalf("shape: %+v", a)
	}
	if !reflect.DeepEqual(a.Fields.AddLabels, []string{"fbsum:" + feedbackD12}) {
		t.Errorf("add: %v", a.Fields.AddLabels)
	}
	if !reflect.DeepEqual(a.Fields.RemoveLabels, []string{"fbsum:" + feedbackD2}) {
		t.Errorf("remove: %v", a.Fields.RemoveLabels)
	}
	if got := a.Fields.Metadata["covered_comments"]; got != "c-101,c-102" {
		t.Errorf("covered_comments: %q", got)
	}
}

func TestFeedbackOpenCycleStaleDigestWithoutNewCommentIsNoAction(t *testing.T) {
	feedbackWantSkip(t, "open_cycle_stale_no_new", action.ReasonAlreadyHandled)
}

func TestFeedbackClosedCycleCoversAllRegardlessOfCloseReason(t *testing.T) {
	var skips []action.Skip
	for _, reason := range []string{"done", "wontfix", "duplicate"} {
		fixture := "closed_covered_" + reason
		feedbackWantSkip(t, fixture, action.ReasonAlreadyHandled)
		skips = append(skips, *feedbackEvaluate(t, fixture).Skip)
	}
	for _, s := range skips[1:] {
		if !reflect.DeepEqual(s, skips[0]) {
			t.Errorf("skip differs by close reason: %+v vs %+v", s, skips[0])
		}
	}
}

func TestFeedbackExtraCommentAfterClosedCycleCreatesNewCycle(t *testing.T) {
	a := feedbackWantOne(t, "closed_then_extra_comment")
	if a.Op != action.OpCreate || a.Fields.Parent != "bd-1" {
		t.Fatalf("shape: %+v", a)
	}
	if !reflect.DeepEqual(a.Fields.Labels, []string{"mine", "fbsum:" + feedbackD123}) {
		t.Errorf("labels: %v", a.Fields.Labels)
	}
	if got := a.Fields.Metadata["dedup_key"]; got != "pr:acme/widgets#42:process-feedback:"+feedbackD123 {
		t.Errorf("dedup_key: %q", got)
	}
	if got := a.Fields.Metadata["covered_comments"]; got != "c-101,c-102,c-103" {
		t.Errorf("covered_comments: %q", got)
	}
}

func TestFeedbackNotMatched(t *testing.T) {
	feedbackWantSkip(t, "team_unaddressed", action.ReasonNotMatched)
	feedbackWantSkip(t, "no_unaddressed", action.ReasonNotMatched)
}

func TestFeedbackPreCutoverClosedCycleCoversCurrentDigest(t *testing.T) {
	feedbackWantSkip(t, "precutover_closed", action.ReasonAlreadyHandled)
}

func TestFeedbackEffectiveDispositionOverrideWins(t *testing.T) {
	// c-101: computed will-fix, override open -> unaddressed.
	// c-102: computed open, override no-action -> addressed.
	// c-103: computed open, no override -> unaddressed. c-104: wont-fix.
	a := feedbackWantOne(t, "override_effective")
	if a.Op != action.OpCreate {
		t.Fatalf("shape: %+v", a)
	}
	if !reflect.DeepEqual(a.Fields.Labels, []string{"mine", "fbsum:" + feedbackD13}) {
		t.Errorf("labels: %v", a.Fields.Labels)
	}
	if got := a.Fields.Metadata["covered_comments"]; got != "c-101,c-103" {
		t.Errorf("covered_comments: %q", got)
	}
}

func TestFeedbackOpenHumanParkedCycleIsTheExistingCycle(t *testing.T) {
	feedbackWantSkip(t, "human_parked", action.ReasonAlreadyHandled)
}

// G6: the companion fixture represents the state after the action was
// applied; the rule yields no action on it.
func TestFeedbackIdempotentOnPostActionState(t *testing.T) {
	for _, fixture := range []string{
		"mine_create_after",
		"coowned_create_no_anchor_after",
		"open_cycle_new_comment_after",
		"closed_then_extra_comment_after",
		"override_effective_after",
	} {
		t.Run(fixture, func(t *testing.T) {
			feedbackWantSkip(t, fixture, action.ReasonAlreadyHandled)
		})
	}
}

func TestFeedbackDecideIsPure(t *testing.T) {
	v := feedbackLoadView(t, "mine_create")
	a := feedbackRule(t).Evaluate(decide.Input{View: v, Items: workitem.BuildIndex(v), EntityType: decide.EntityTypePR})
	b := feedbackRule(t).Evaluate(decide.Input{View: v, Items: workitem.BuildIndex(v), EntityType: decide.EntityTypePR})
	if !reflect.DeepEqual(a, b) {
		t.Errorf("not deterministic: %+v vs %+v", a, b)
	}
}
