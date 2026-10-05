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

// The anchor group's tests (all.closed, all.reopened, anchor.lazy,
// anchor.backfill, anchor.priority, adoption). Every helper here carries the
// "anchor" prefix because sibling rule packets add _test.go files to this
// same Go package. The rules are always looked up through the registry, so
// each test also proves the rule registered under its id.

func anchorLoadView(t *testing.T, name string) *view.View {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "anchor", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := view.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func anchorRule(t *testing.T, id string) decide.Rule {
	t.Helper()
	for _, r := range decide.RulesFor(decide.EntityTypePR) {
		if r.ID() == id {
			return r
		}
	}
	t.Fatalf("rule %q is not registered for %q", id, decide.EntityTypePR)
	return nil
}

func anchorInput(v *view.View) decide.Input {
	return decide.Input{View: v, Items: workitem.BuildIndex(v), EntityType: decide.EntityTypePR}
}

func anchorEval(t *testing.T, id, fixture string) decide.Result {
	t.Helper()
	return anchorRule(t, id).Evaluate(anchorInput(anchorLoadView(t, fixture)))
}

// anchorSummary renders each action as "<op> <kind> <target>" ("-" when the
// target is nil), which is what most cases assert.
func anchorSummary(res decide.Result) []string {
	out := []string{}
	for _, a := range res.Actions {
		tgt := "-"
		if a.Target != nil {
			tgt = *a.Target
		}
		out = append(out, string(a.Op)+" "+a.Kind+" "+tgt)
	}
	return out
}

func anchorSkipReason(res decide.Result) string {
	if res.Skip == nil {
		return ""
	}
	return res.Skip.Reason
}

func TestAnchorGroupRegisteredInOrdinalOrder(t *testing.T) {
	want := []string{"all.closed", "all.reopened", "anchor.lazy", "anchor.backfill", "anchor.priority", "adoption"}
	var got []string
	in := map[string]bool{}
	for _, id := range want {
		in[id] = true
	}
	for _, r := range decide.RulesFor(decide.EntityTypePR) {
		if in[r.ID()] {
			got = append(got, r.ID())
			if r.Kind() != "" {
				t.Errorf("%s: Kind() = %q, want \"\" (the anchor mirrors the PR and is never a suppressible kind)", r.ID(), r.Kind())
			}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("registered order = %v, want %v", got, want)
	}
	if _, ok := anchorRule(t, "anchor.lazy").(decide.Finalizer); !ok {
		t.Fatal("anchor.lazy must implement decide.Finalizer")
	}
}

// ---- all.closed -------------------------------------------------------

func TestAnchorAllClosed(t *testing.T) {
	cases := []struct {
		fixture string
		want    []string
		skip    string
	}{
		// Children first, anchor last; both a merged and a closed PR.
		{"closed_merged_open_anchor_two_children", []string{"close review-pr bd-2", "close fix-ci bd-3", "close anchor bd-1"}, ""},
		// A child nobody classified (an improvised "Human: ..." bead) is closed too.
		{"closed_closed_open_anchor_two_children", []string{"close review-pr bd-2", "close  bd-3", "close anchor bd-1"}, ""},
		// A retry after a partial failure closes only what is still open.
		{"closed_only_child_open_after_partial", []string{"close review-pr bd-2"}, ""},
		{"closed_all_closed_after", []string{}, action.ReasonAlreadyHandled},
		{"closed_no_anchor", []string{}, action.ReasonNotMatched},
		{"closed_open_pr_open_anchor", []string{}, action.ReasonNotMatched},
		// Not a child of this anchor.
		{"closed_foreign_parent_child_kept", []string{}, action.ReasonAlreadyHandled},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			res := anchorEval(t, "all.closed", tc.fixture)
			if got := anchorSummary(res); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("actions = %v, want %v", got, tc.want)
			}
			if len(tc.want) == 0 {
				if got := anchorSkipReason(res); got != tc.skip {
					t.Fatalf("skip reason = %q, want %q", got, tc.skip)
				}
			}
		})
	}
}

func TestAnchorAllClosedFactsNameThePRState(t *testing.T) {
	res := anchorEval(t, "all.closed", "closed_merged_open_anchor_two_children")
	for _, a := range res.Actions {
		if a.Facts["pr_state"] != "merged" {
			t.Errorf("%s facts = %v, want pr_state=merged", *a.Target, a.Facts)
		}
	}
	res = anchorEval(t, "all.closed", "closed_closed_open_anchor_two_children")
	if res.Actions[0].Facts["pr_state"] != "closed" {
		t.Errorf("facts = %v, want pr_state=closed", res.Actions[0].Facts)
	}
}

// ---- all.reopened -----------------------------------------------------

func TestAnchorAllReopenedIgnoresWhoClosedTheAnchor(t *testing.T) {
	var first []action.Action
	for _, fixture := range []string{"reopened_anchor_closed_done", "reopened_anchor_closed_wontfix", "reopened_anchor_closed_duplicate"} {
		res := anchorEval(t, "all.reopened", fixture)
		if got := anchorSummary(res); !reflect.DeepEqual(got, []string{"reopen anchor bd-1"}) {
			t.Fatalf("%s: actions = %v", fixture, got)
		}
		if first == nil {
			first = res.Actions
		} else if !reflect.DeepEqual(first, res.Actions) {
			t.Fatalf("%s: action differs from the first close_reason:\n%+v\n%+v", fixture, res.Actions, first)
		}
	}
}

func TestAnchorAllReopenedNoAction(t *testing.T) {
	cases := []struct{ fixture, skip string }{
		{"reopened_anchor_open_after", action.ReasonAlreadyHandled},
		{"reopened_pr_closed_anchor_closed", action.ReasonNotMatched},
		{"reopened_no_anchor", action.ReasonNotMatched},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			res := anchorEval(t, "all.reopened", tc.fixture)
			if len(res.Actions) != 0 || anchorSkipReason(res) != tc.skip {
				t.Fatalf("got %+v, want no action, skip %q", res, tc.skip)
			}
		})
	}
}

func TestAnchorAllReopenedTouchesOnlyTheAnchor(t *testing.T) {
	res := anchorEval(t, "all.reopened", "reopened_anchor_closed_children_open")
	if got := anchorSummary(res); !reflect.DeepEqual(got, []string{"reopen anchor bd-1"}) {
		t.Fatalf("actions = %v", got)
	}
}

// ---- anchor.lazy ------------------------------------------------------

func anchorChildCreate(kind string) action.Action {
	return action.Action{
		Op: action.OpCreate, Kind: kind, Rule: "review.head-advanced",
		Fields: action.Fields{Title: kind + ": acme/widgets#42", Parent: action.AnchorParent},
	}
}

func anchorFinalize(t *testing.T, fixture string, produced []action.Action) []action.Action {
	t.Helper()
	v := anchorLoadView(t, fixture)
	f, ok := anchorRule(t, "anchor.lazy").(decide.Finalizer)
	if !ok {
		t.Fatal("anchor.lazy is not a Finalizer")
	}
	return f.Finalize(anchorInput(v), produced)
}

func TestAnchorLazyCreatesAnchorBeforeTheFirstChild(t *testing.T) {
	produced := []action.Action{
		{Op: action.OpUpdate, Kind: "fix-ci", Target: anchorStr("bd-9"), Rule: "fixci.failing-on-head"},
		anchorChildCreate("review-pr"),
		anchorChildCreate("process-feedback"),
	}
	got := anchorFinalize(t, "lazy_no_anchor", produced)
	if len(got) != 4 {
		t.Fatalf("want 4 actions, got %+v", got)
	}
	if got[0].Kind != "fix-ci" || got[2].Kind != "review-pr" || got[3].Kind != "process-feedback" {
		t.Fatalf("other actions must keep their relative order: %+v", got)
	}
	a := got[1]
	if a.Op != action.OpCreate || a.Kind != "anchor" || a.Target != nil || a.Rule != "anchor.lazy" {
		t.Fatalf("anchor create = %+v", a)
	}
	f := a.Fields
	if f.Title != "acme/widgets#42: Add retry to client" || f.IssueType != "merge-request" || f.Parent != "" {
		t.Fatalf("fields = %+v", f)
	}
	if len(f.Labels) != 0 || f.Priority != "" {
		t.Fatalf("a non-conflicting mine PR gets no label and no priority: %+v", f)
	}
	wantMD := map[string]string{
		"repo": "acme/widgets", "pr_number": "42", "state": "open", "branch": "feature/retry", "base": "main",
		"author": "teammate", "url": "https://example.test/acme/widgets/pull/42", "draft": "false",
		"dedup_key": "pr:acme/widgets#42:anchor",
	}
	if !reflect.DeepEqual(f.Metadata, wantMD) {
		t.Fatalf("metadata = %v\nwant       %v", f.Metadata, wantMD)
	}
}

func TestAnchorLazyCoOwnedAnchorCarriesTheLabel(t *testing.T) {
	got := anchorFinalize(t, "lazy_no_anchor_coowned", []action.Action{anchorChildCreate("review-pr")})
	if !reflect.DeepEqual(got[0].Fields.Labels, []string{"co-owned"}) {
		t.Fatalf("labels = %v", got[0].Fields.Labels)
	}
}

func TestAnchorLazyAppliesTheConflictNudgeOnCreate(t *testing.T) {
	got := anchorFinalize(t, "lazy_no_anchor_team_conflict", []action.Action{anchorChildCreate("review-pr")})
	f := got[0].Fields
	if !reflect.DeepEqual(f.Labels, []string{"pbase:2"}) || f.Priority != "P3" {
		t.Fatalf("a team PR in conflict is created lowered with its baseline stashed: %+v", f)
	}
}

func TestAnchorLazyIsNeverEager(t *testing.T) {
	for _, produced := range [][]action.Action{
		nil,
		{{Op: action.OpUpdate, Kind: "fix-ci", Target: anchorStr("bd-9")}},
		{{Op: action.OpCreate, Kind: "review-pr", Fields: action.Fields{Parent: "bd-7"}}},
	} {
		got := anchorFinalize(t, "lazy_no_anchor", produced)
		if !reflect.DeepEqual(got, produced) {
			t.Fatalf("produced %+v, finalize returned %+v", produced, got)
		}
	}
}

func TestAnchorLazyLeavesAnExistingAnchorAlone(t *testing.T) {
	for _, fixture := range []string{"lazy_anchor_exists_after", "lazy_anchor_closed"} {
		produced := []action.Action{anchorChildCreate("review-pr")}
		got := anchorFinalize(t, fixture, produced)
		if !reflect.DeepEqual(got, produced) {
			t.Fatalf("%s: finalize changed the list: %+v", fixture, got)
		}
	}
}

func TestAnchorLazyDoesNotDuplicateAnAnchorCreateAlreadyInTheList(t *testing.T) {
	produced := []action.Action{
		{Op: action.OpCreate, Kind: "anchor", Rule: "someone.else"},
		anchorChildCreate("review-pr"),
	}
	if got := anchorFinalize(t, "lazy_no_anchor", produced); !reflect.DeepEqual(got, produced) {
		t.Fatalf("got %+v", got)
	}
}

func TestAnchorLazyEvaluateItselfNeverActs(t *testing.T) {
	res := anchorEval(t, "anchor.lazy", "lazy_no_anchor")
	if len(res.Actions) != 0 || anchorSkipReason(res) != action.ReasonNotMatched {
		t.Fatalf("got %+v", res)
	}
}

func TestAnchorLazyThroughTheDecideCore(t *testing.T) {
	// No anchor: the review-pr create the review rule makes names $anchor, so
	// the core puts the anchor create first.
	res := decide.Decide(anchorLoadView(t, "lazy_no_anchor"), decide.EntityTypePR)
	if len(res.Actions) < 2 || res.Actions[0].Op != action.OpCreate || res.Actions[0].Kind != "anchor" || res.Actions[0].Rule != "anchor.lazy" {
		t.Fatalf("want the anchor create first, got %+v", res.Actions)
	}
	var sawChild bool
	for _, a := range res.Actions[1:] {
		if a.Kind == "review-pr" && a.Fields.Parent == action.AnchorParent {
			sawChild = true
		}
	}
	if !sawChild {
		t.Fatalf("want a review-pr create under $anchor, got %+v", res.Actions)
	}

	// Companion: the state after the anchor was created. No anchor create.
	res = decide.Decide(anchorLoadView(t, "lazy_anchor_exists_after"), decide.EntityTypePR)
	for _, a := range res.Actions {
		if a.Op == action.OpCreate && a.Kind == "anchor" {
			t.Fatalf("an anchor create after the anchor exists: %+v", a)
		}
	}
}

// ---- anchor.backfill --------------------------------------------------

func TestAnchorBackfill(t *testing.T) {
	cases := []struct {
		fixture string
		want    map[string]string // nil: no action
		skip    string
	}{
		{"backfill_missing_repo_pr", map[string]string{"repo": "acme/widgets", "pr_number": "42"}, ""},
		{"backfill_missing_repo_pr_keyed", map[string]string{"repo": "acme/widgets", "pr_number": "42"}, ""},
		{"backfill_missing_and_drift", map[string]string{"repo": "acme/widgets", "pr_number": "42", "base": "release"}, ""},
		{"backfill_drift_branch_draft", map[string]string{"branch": "feature/retry-2", "draft": "true"}, ""},
		{"backfill_drift_merged_state", map[string]string{"state": "merged"}, ""},
		// Companions: the state after each update above.
		{"backfill_complete_after", nil, action.ReasonAlreadyHandled},
		{"backfill_drift_after", nil, action.ReasonAlreadyHandled},
		{"backfill_drift_merged_state_after", nil, action.ReasonAlreadyHandled},
		{"backfill_no_anchor", nil, action.ReasonNotMatched},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			res := anchorEval(t, "anchor.backfill", tc.fixture)
			if tc.want == nil {
				if len(res.Actions) != 0 || anchorSkipReason(res) != tc.skip {
					t.Fatalf("got %+v, want no action, skip %q", res, tc.skip)
				}
				return
			}
			if len(res.Actions) != 1 {
				t.Fatalf("want exactly one update, got %+v", res)
			}
			a := res.Actions[0]
			if a.Op != action.OpUpdate || a.Kind != "anchor" || a.Target == nil || *a.Target != "bd-1" {
				t.Fatalf("action = %+v", a)
			}
			if !reflect.DeepEqual(a.Fields.Metadata, tc.want) {
				t.Fatalf("metadata = %v, want %v", a.Fields.Metadata, tc.want)
			}
			if a.Fields.Title != "" || len(a.Fields.AddLabels)+len(a.Fields.RemoveLabels) != 0 {
				t.Fatalf("backfill writes metadata only: %+v", a.Fields)
			}
		})
	}
}

func anchorStr(s string) *string { return &s }
