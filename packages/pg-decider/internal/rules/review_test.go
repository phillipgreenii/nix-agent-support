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

const reviewHead = "9f3c1e2"

func reviewLoadView(t *testing.T, name string) *view.View {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "review", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := view.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func reviewRule(t *testing.T) decide.Rule {
	t.Helper()
	for _, r := range decide.RulesFor(decide.EntityTypePR) {
		if r.ID() == reviewRuleID {
			return r
		}
	}
	t.Fatalf("rule %s is not registered", reviewRuleID)
	return nil
}

func reviewEvaluate(t *testing.T, fixture string) decide.Result {
	t.Helper()
	v := reviewLoadView(t, fixture)
	return reviewRule(t).Evaluate(decide.Input{View: v, Items: workitem.BuildIndex(v), EntityType: decide.EntityTypePR})
}

func reviewWantSkip(t *testing.T, fixture, reason string) {
	t.Helper()
	res := reviewEvaluate(t, fixture)
	if len(res.Actions) != 0 {
		t.Fatalf("%s: want no action, got %+v", fixture, res.Actions)
	}
	if res.Skip == nil || res.Skip.Reason != reason {
		t.Fatalf("%s: want skip %q, got %+v", fixture, reason, res.Skip)
	}
}

func reviewWantActions(t *testing.T, fixture string, n int) []action.Action {
	t.Helper()
	res := reviewEvaluate(t, fixture)
	if len(res.Actions) != n {
		t.Fatalf("%s: want %d actions, got %+v (skip %+v)", fixture, n, res.Actions, res.Skip)
	}
	return res.Actions
}

func reviewMetadata(head string) map[string]string {
	return map[string]string{
		"repo": "acme/widgets", "pr_number": "42", "branch": "feature/retry",
		"head_sha": head, "ownership": "mine",
	}
}

func reviewWantCreate(t *testing.T, a action.Action, parent, ownership string) {
	t.Helper()
	if a.Op != action.OpCreate || a.Kind != "review-pr" || a.Target != nil || a.Rule != "" && a.Rule != reviewRuleID {
		t.Fatalf("shape: %+v", a)
	}
	f := a.Fields
	if f.Title != "review-pr: acme/widgets#42" || f.IssueType != "task" || f.Parent != parent {
		t.Errorf("fields: %+v", f)
	}
	want := reviewMetadata(reviewHead)
	want["ownership"] = ownership
	want["dedup_key"] = "pr:acme/widgets#42:review-pr"
	if !reflect.DeepEqual(f.Metadata, want) {
		t.Errorf("metadata: %v", f.Metadata)
	}
}

func reviewWantReopen(t *testing.T, a action.Action) {
	t.Helper()
	if a.Op != action.OpReopen || a.Kind != "review-pr" || a.Target == nil || *a.Target != "bd-2" {
		t.Fatalf("reopen shape: %+v", a)
	}
}

func reviewWantUpdate(t *testing.T, a action.Action, head string) {
	t.Helper()
	if a.Op != action.OpUpdate || a.Kind != "review-pr" || a.Target == nil || *a.Target != "bd-2" {
		t.Fatalf("update shape: %+v", a)
	}
	if !reflect.DeepEqual(a.Fields.Metadata, reviewMetadata(head)) {
		t.Errorf("metadata: %v", a.Fields.Metadata)
	}
}

func reviewWantConsumption(t *testing.T, annotations []action.Action, sha string) {
	t.Helper()
	if len(annotations) != 2 {
		t.Fatalf("want 2 annotations, got %+v", annotations)
	}
	rec, clr := annotations[0], annotations[1]
	if rec.Op != action.OpAnnotate || rec.Kind != "" || rec.Target == nil ||
		*rec.Target != "decider.pr-decider.force_review_consumed" ||
		rec.Fields.Value == nil || *rec.Fields.Value != sha || rec.Fields.Clear || !rec.RequiresPrior {
		t.Errorf("consumption record: %+v", rec)
	}
	if clr.Op != action.OpAnnotate || clr.Kind != "" || clr.Target == nil || *clr.Target != "force_review" ||
		!clr.Fields.Clear || clr.Fields.Value != nil || !clr.RequiresPrior {
		t.Errorf("clear: %+v", clr)
	}
}

func TestReviewRuleRegisteredForPR(t *testing.T) {
	r := reviewRule(t)
	if r.Kind() != workitem.KindReviewPR {
		t.Errorf("kind = %q", r.Kind())
	}
	if reviewRuleID != "review.head-advanced" {
		t.Errorf("rule id = %q", reviewRuleID)
	}
	var ids []string
	for _, x := range decide.RulesFor(decide.EntityTypePR) {
		ids = append(ids, x.ID())
	}
	found := false
	for _, id := range ids {
		found = found || id == "review.head-advanced"
	}
	if !found {
		t.Errorf("not registered: %v", ids)
	}
}

func TestReviewCreateForQualifyingPR(t *testing.T) {
	for _, tc := range []struct {
		fixture, parent, ownership string
	}{
		{"mine_create", "bd-1", "mine"},
		{"mine_create_no_anchor", action.AnchorParent, "mine"},
		{"coowned_draft_create", "bd-1", "co-owned"},
		{"team_nondraft_create", "bd-1", "team"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			as := reviewWantActions(t, tc.fixture, 1)
			reviewWantCreate(t, as[0], tc.parent, tc.ownership)
		})
	}
}

func TestReviewNotMatchedWhenPRDoesNotQualify(t *testing.T) {
	reviewWantSkip(t, "team_draft", action.ReasonNotMatched)
	reviewWantSkip(t, "no_head", action.ReasonNotMatched)
}

func TestReviewHeadAdvancedOpenItemIsRefreshed(t *testing.T) {
	as := reviewWantActions(t, "advanced_open", 1)
	reviewWantUpdate(t, as[0], reviewHead)
}

func TestReviewHeadAdvancedClosedItemIsReopenedThenRefreshed(t *testing.T) {
	as := reviewWantActions(t, "advanced_closed", 2)
	reviewWantReopen(t, as[0])
	reviewWantUpdate(t, as[1], reviewHead)
}

func TestReviewUnchangedHeadOpenItemIsReviewPending(t *testing.T) {
	reviewWantSkip(t, "open_same_head", action.ReasonReviewPending)
}

func TestReviewUnchangedHeadClosedItemIsAlreadyHandledHoweverClosed(t *testing.T) {
	var skips []action.Skip
	for _, f := range []string{
		"closed_same_head_done", "closed_same_head_wontfix",
		"closed_same_head_duplicate", "closed_same_head_no_reason",
	} {
		reviewWantSkip(t, f, action.ReasonAlreadyHandled)
		skips = append(skips, *reviewEvaluate(t, f).Skip)
	}
	for _, s := range skips[1:] {
		if !reflect.DeepEqual(s, skips[0]) {
			t.Errorf("skip differs by close reason: %+v vs %+v", s, skips[0])
		}
	}
}

func TestReviewForceOpenItemSameHeadIsRefreshedAndConsumed(t *testing.T) {
	as := reviewWantActions(t, "force_open_same_head", 3)
	reviewWantUpdate(t, as[0], reviewHead)
	reviewWantConsumption(t, as[1:], reviewHead)
}

func TestReviewForceClosedItemSameHeadIsReopenedAndConsumed(t *testing.T) {
	as := reviewWantActions(t, "force_closed_same_head", 4)
	reviewWantReopen(t, as[0])
	reviewWantUpdate(t, as[1], reviewHead)
	reviewWantConsumption(t, as[2:], reviewHead)
}

func TestReviewForceWithAdvancedHead(t *testing.T) {
	as := reviewWantActions(t, "force_closed_advanced", 4)
	reviewWantReopen(t, as[0])
	reviewWantUpdate(t, as[1], reviewHead)
	reviewWantConsumption(t, as[2:], reviewHead)

	as = reviewWantActions(t, "force_open_advanced", 3)
	reviewWantUpdate(t, as[0], reviewHead)
	reviewWantConsumption(t, as[1:], reviewHead)
}

func TestReviewForceWithoutItemCreatesThenConsumes(t *testing.T) {
	as := reviewWantActions(t, "force_no_item", 3)
	reviewWantCreate(t, as[0], "bd-1", "mine")
	reviewWantConsumption(t, as[1:], reviewHead)
}

func TestReviewForceWritesCurrentHeadNotTheRequestedSHA(t *testing.T) {
	as := reviewWantActions(t, "force_sha_differs", 4)
	reviewWantReopen(t, as[0])
	reviewWantUpdate(t, as[1], reviewHead)
	reviewWantConsumption(t, as[2:], "0ldsha1")
}

func TestReviewForceSHAMissingFallsBackToCurrentHead(t *testing.T) {
	as := reviewWantActions(t, "force_sha_null", 4)
	reviewWantConsumption(t, as[2:], reviewHead)
}

func TestReviewForceOfNonQualifyingPRNeitherActsNorClears(t *testing.T) {
	reviewWantSkip(t, "force_team_draft", action.ReasonNotMatched)
	reviewWantSkip(t, "force_team_draft_with_item", action.ReasonNotMatched)
}

// A flag present is a fresh request: the consumed record is audit only, so a
// second forced review of the same SHA still acts.
func TestReviewFlagPresentIsFreshRequestDespiteConsumedRecord(t *testing.T) {
	as := reviewWantActions(t, "force_with_prior_consumed_record", 3)
	reviewWantUpdate(t, as[0], reviewHead)
	reviewWantConsumption(t, as[1:], reviewHead)
}

func TestReviewOnlyAnnotationsRequirePrior(t *testing.T) {
	for _, f := range []string{"force_closed_same_head", "force_no_item", "advanced_closed"} {
		for _, a := range reviewEvaluate(t, f).Actions {
			if a.Op == action.OpAnnotate {
				continue
			}
			if a.RequiresPrior {
				t.Errorf("%s: %s must not require prior", f, a.Op)
			}
		}
	}
}

// G6: the companion fixture represents the state after the actions were
// applied; the rule yields no action on it.
func TestReviewIdempotentOnPostActionState(t *testing.T) {
	for _, tc := range []struct{ fixture, reason string }{
		{"mine_create_after", action.ReasonReviewPending},
		{"advanced_open_after", action.ReasonReviewPending},
		{"advanced_closed_after", action.ReasonReviewPending},
		{"force_open_same_head_after", action.ReasonReviewPending},
		{"force_closed_same_head_after", action.ReasonReviewPending},
		{"force_no_item_after", action.ReasonReviewPending},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			reviewWantSkip(t, tc.fixture, tc.reason)
		})
	}
}

func TestReviewDecideIsPure(t *testing.T) {
	for _, f := range []string{"force_closed_advanced", "mine_create"} {
		v := reviewLoadView(t, f)
		in := decide.Input{View: v, Items: workitem.BuildIndex(v), EntityType: decide.EntityTypePR}
		a, b := reviewRule(t).Evaluate(in), reviewRule(t).Evaluate(in)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%s: not deterministic: %+v vs %+v", f, a, b)
		}
	}
}
