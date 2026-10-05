package rules

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/decide"
	"github.com/phillipgreenii/pg-decider/internal/view"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

const fixciRuleID = "fixci.failing-on-head"

func fixciLoadView(t *testing.T, name string) *view.View {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "fixci", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := view.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// fixciEvaluate runs the registered rule through the same path the decider
// core uses, so the test also proves registration at the right ordinal.
func fixciEvaluate(t *testing.T, name string) decide.Result {
	t.Helper()
	v := fixciLoadView(t, name)
	in := decide.Input{View: v, Items: workitem.BuildIndex(v), EntityType: decide.EntityTypePR}
	return fixciRule{}.Evaluate(in)
}

func fixciOps(res decide.Result) []action.Op {
	var ops []action.Op
	for _, a := range res.Actions {
		ops = append(ops, a.Op)
	}
	return ops
}

func TestFixCIRegisteredAtOrdinal80(t *testing.T) {
	var found bool
	for _, r := range decide.RulesFor(decide.EntityTypePR) {
		if r.ID() == fixciRuleID {
			found = true
			if r.Kind() != workitem.KindFixCI {
				t.Fatalf("Kind() = %q", r.Kind())
			}
		}
	}
	if !found {
		t.Fatalf("rule %q is not registered for %q", fixciRuleID, decide.EntityTypePR)
	}
	if decide.OrdinalFixCIFailingOnHead != 80 {
		t.Fatalf("ordinal constant = %d", decide.OrdinalFixCIFailingOnHead)
	}
}

func TestFixCICreate(t *testing.T) {
	cases := []struct {
		fixture, parent, builds, checks string
	}{
		{"failing_no_item", "bd-1", "900:2,901:1", "lint,unit"},
		{"failing_no_anchor", action.AnchorParent, "900:2", "unit"},
		{"failing_coowned", "bd-1", "900:2", "unit"},
		{"failing_with_pending", "bd-1", "900:2", "unit"},
		{"failing_attempt_absent", "bd-1", "910:0,911:0", "unit"},
		{"failing_new_head_old_item_closed", "bd-1", "900:2", "unit"},
		{"failing_new_head_old_item_open", "bd-1", "900:2", "unit"},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			res := fixciEvaluate(t, tc.fixture)
			if len(res.Actions) != 1 || res.Skip != nil {
				t.Fatalf("want exactly one create, got %+v", res)
			}
			a := res.Actions[0]
			if a.Op != action.OpCreate || a.Kind != "fix-ci" || a.Target != nil {
				t.Fatalf("action = %+v", a)
			}
			f := a.Fields
			if f.Title != "fix-ci: acme/widgets#42" || f.IssueType != "task" || f.Parent != tc.parent {
				t.Fatalf("fields = %+v", f)
			}
			if !reflect.DeepEqual(f.Labels, []string{"mine", "worker-ready"}) {
				t.Fatalf("labels = %v", f.Labels)
			}
			want := map[string]string{
				"repo": "acme/widgets", "pr_number": "42", "branch": "feature/retry",
				"head_sha": "9f3c1e2", "failing_builds": tc.builds, "failing_checks": tc.checks,
				"dedup_key": "pr:acme/widgets#42:fix-ci:9f3c1e2",
			}
			if !reflect.DeepEqual(f.Metadata, want) {
				t.Fatalf("metadata = %v\nwant       %v", f.Metadata, want)
			}
			if strings.TrimSpace(f.Description) == "" {
				t.Fatal("empty description")
			}
		})
	}
}

func TestFixCICreateDescriptionNamesEachFailingCheckWithItsURL(t *testing.T) {
	res := fixciEvaluate(t, "failing_no_item")
	d := res.Actions[0].Fields.Description
	for _, want := range []string{"unit", "https://ci.example/runs/900", "lint", "https://ci.example/runs/901"} {
		if !strings.Contains(d, want) {
			t.Errorf("description lacks %q:\n%s", want, d)
		}
	}
	if strings.Contains(d, "https://ci.example/runs/902") {
		t.Errorf("description mentions a passing run:\n%s", d)
	}
}

func TestFixCICreateMetadataStaysInsideTheContract(t *testing.T) {
	allowed := map[string]bool{}
	for _, k := range workitem.ContractFor(workitem.KindFixCI).MetadataKeys {
		allowed[k] = true
	}
	for _, name := range []string{"failing_no_item", "failing_item_open_new_id"} {
		for _, a := range fixciEvaluate(t, name).Actions {
			for k := range a.Fields.Metadata {
				if !allowed[k] {
					t.Errorf("%s: metadata key %q is not in the fix-ci contract", name, k)
				}
			}
		}
	}
}

func TestFixCINewFailingIDOnExistingItem(t *testing.T) {
	t.Run("open item gets an update only", func(t *testing.T) {
		res := fixciEvaluate(t, "failing_item_open_new_id")
		if got := fixciOps(res); !reflect.DeepEqual(got, []action.Op{action.OpUpdate}) {
			t.Fatalf("ops = %v", got)
		}
		u := res.Actions[0]
		if u.Target == nil || *u.Target != "bd-3" || u.Kind != "fix-ci" || u.RequiresPrior {
			t.Fatalf("update = %+v", u)
		}
	})
	t.Run("closed item is reopened then updated", func(t *testing.T) {
		res := fixciEvaluate(t, "failing_item_closed_new_id")
		if got := fixciOps(res); !reflect.DeepEqual(got, []action.Op{action.OpReopen, action.OpUpdate}) {
			t.Fatalf("ops = %v", got)
		}
		if *res.Actions[0].Target != "bd-3" || *res.Actions[1].Target != "bd-3" {
			t.Fatalf("targets: %+v", res.Actions)
		}
		if !res.Actions[1].RequiresPrior {
			t.Fatal("the update after a reopen must require the reopen")
		}
	})
	t.Run("claimed item is updated, not reopened", func(t *testing.T) {
		res := fixciEvaluate(t, "failing_item_claimed_new_id")
		if got := fixciOps(res); !reflect.DeepEqual(got, []action.Op{action.OpUpdate}) {
			t.Fatalf("ops = %v", got)
		}
	})
	t.Run("update keeps the existing ids and adds the new one", func(t *testing.T) {
		for _, name := range []string{"failing_item_open_new_id", "failing_item_closed_new_id"} {
			res := fixciEvaluate(t, name)
			u := res.Actions[len(res.Actions)-1]
			got := u.Fields.Metadata
			if got["failing_builds"] != "900:1,900:2,901:1" {
				t.Errorf("%s: failing_builds = %q", name, got["failing_builds"])
			}
			if got["failing_checks"] != "lint,unit" {
				t.Errorf("%s: failing_checks = %q", name, got["failing_checks"])
			}
			if len(got) != 2 {
				t.Errorf("%s: update metadata must carry only the two failing lists: %v", name, got)
			}
		}
	})
}

func TestFixCIEveryFailingIDAlreadyListedIsAlreadyHandled(t *testing.T) {
	for _, name := range []string{"failing_item_covered_open", "failing_item_covered_closed"} {
		t.Run(name, func(t *testing.T) {
			res := fixciEvaluate(t, name)
			if len(res.Actions) != 0 || res.Skip == nil || res.Skip.Reason != action.ReasonAlreadyHandled {
				t.Fatalf("res = %+v", res)
			}
		})
	}
}

func TestFixCIGreenClosesUnclaimedOpenItem(t *testing.T) {
	res := fixciEvaluate(t, "green_item_open")
	if len(res.Actions) != 1 {
		t.Fatalf("res = %+v", res)
	}
	a := res.Actions[0]
	if a.Op != action.OpClose || a.Kind != "fix-ci" || a.Target == nil || *a.Target != "bd-3" {
		t.Fatalf("action = %+v", a)
	}
}

func TestFixCIGreenNeverActsOtherwise(t *testing.T) {
	for _, name := range []string{
		"green_item_claimed",            // claimed: left for the worker
		"green_item_closed",             // already closed
		"green_no_item",                 // nothing to close
		"green_old_head_item_open",      // an older head's item is not this head's
		"pending_item_open",             // a pending run is not green
		"empty_runs_item_open",          // no runs is not green
		"pending_conclusions_item_open", // pending/expected conclusions are pending
		"missing_ci_section",            // a binary that predates the section
	} {
		res := fixciEvaluate(t, name)
		if len(res.Actions) != 0 {
			t.Errorf("%s: actions = %+v", name, res.Actions)
		}
		if res.Skip == nil || res.Skip.Reason != action.ReasonNotMatched {
			t.Errorf("%s: skip = %+v, want not matched", name, res.Skip)
		}
	}
}

func TestFixCINeutralAndSkippedRunsCountAsGreen(t *testing.T) {
	res := fixciEvaluate(t, "neutral_only_item_open")
	if got := fixciOps(res); !reflect.DeepEqual(got, []action.Op{action.OpClose}) {
		t.Fatalf("ops = %v", got)
	}
}

func TestFixCITeamAndHeadlessAreNotMatched(t *testing.T) {
	for _, name := range []string{"failing_team", "failing_no_head"} {
		res := fixciEvaluate(t, name)
		if len(res.Actions) != 0 || res.Skip == nil || res.Skip.Reason != action.ReasonNotMatched {
			t.Errorf("%s: %+v", name, res)
		}
	}
}

func TestFixCIAdoptableItemIsNotDuplicated(t *testing.T) {
	res := fixciEvaluate(t, "failing_item_adoptable")
	if got := fixciOps(res); !reflect.DeepEqual(got, []action.Op{action.OpUpdate}) {
		t.Fatalf("ops = %v (a keyless item for this head must be updated, never re-created)", got)
	}
	if *res.Actions[0].Target != "bd-9" {
		t.Fatalf("target = %v", *res.Actions[0].Target)
	}
}

func TestFixCISuppressedKindIsSkippedByTheCore(t *testing.T) {
	v := fixciLoadView(t, "failing_no_item")
	v.Annotations.Suppress = []string{"fix-ci"}
	res := decide.Decide(v, decide.EntityTypePR)
	for _, a := range res.Actions {
		if a.Rule == fixciRuleID {
			t.Fatalf("suppressed kind still produced %+v", a)
		}
	}
	var seen bool
	for _, s := range res.Skipped {
		if s.Rule == fixciRuleID && s.Reason == action.ReasonSuppressed {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("skipped = %+v", res.Skipped)
	}
}

func TestFixCIThroughDecideStampsTheRuleID(t *testing.T) {
	res := decide.Decide(fixciLoadView(t, "failing_no_item"), decide.EntityTypePR)
	var n int
	for _, a := range res.Actions {
		if a.Rule == fixciRuleID {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("actions = %+v", res.Actions)
	}
}

func TestFixCIIsAPureFunctionOfTheView(t *testing.T) {
	for _, name := range []string{"failing_no_item", "failing_item_closed_new_id", "green_item_open", "failing_item_covered_open"} {
		a, b := fixciEvaluate(t, name), fixciEvaluate(t, name)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%s: two evaluations differ:\n%+v\n%+v", name, a, b)
		}
	}
}

// Idempotence: feeding the create's result back into the view as the
// existing item yields no further action.
func TestFixCIRerunAfterApplyWritesNothing(t *testing.T) {
	create := fixciEvaluate(t, "failing_no_item").Actions[0]
	v := fixciLoadView(t, "failing_no_item")
	md := map[string]string{}
	for k, val := range create.Fields.Metadata {
		md[k] = val
	}
	v.Links = append(v.Links, view.Link{
		Type: "issue", ID: "bd-new", Relation: "work", State: "open",
		Title: create.Fields.Title, Labels: create.Fields.Labels, Metadata: md,
	})
	in := decide.Input{View: v, Items: workitem.BuildIndex(v), EntityType: decide.EntityTypePR}
	res := fixciRule{}.Evaluate(in)
	if len(res.Actions) != 0 || res.Skip == nil || res.Skip.Reason != action.ReasonAlreadyHandled {
		t.Fatalf("res = %+v", res)
	}
}

func TestFixCIHelpersSortIDsAndChecks(t *testing.T) {
	runs := []ciRun{
		{ID: "9", Attempt: 1, Name: "b", Status: "completed", Conclusion: "failure"},
		{ID: "10", Attempt: 3, Name: "a", Status: "completed", Conclusion: "cancelled"},
		{ID: "9", Attempt: 1, Name: "b", Status: "completed", Conclusion: "failure"},
	}
	ids, checks := fixciFailing(runs)
	if !sort.StringsAreSorted(ids) || !reflect.DeepEqual(ids, []string{"10:3", "9:1"}) {
		t.Fatalf("ids = %v", ids)
	}
	if !reflect.DeepEqual(checks, []string{"a", "b"}) {
		t.Fatalf("checks = %v", checks)
	}
}

func TestCIViewDecodesRunsAndToleratesAbsence(t *testing.T) {
	runs := ciRuns(fixciLoadView(t, "failing_no_item"))
	if len(runs) != 3 || runs[0] != (ciRun{ID: "900", Attempt: 2, Name: "unit", Status: "completed", Conclusion: "failure", URL: "https://ci.example/runs/900"}) {
		t.Fatalf("runs = %+v", runs)
	}
	if got := ciRuns(fixciLoadView(t, "missing_ci_section")); got != nil {
		t.Fatalf("absent section: %+v", got)
	}
	if got := ciRuns(nil); got != nil {
		t.Fatalf("nil view: %+v", got)
	}
}

func TestCIFailingAndGreenPredicatesMatchDeskCIStatus(t *testing.T) {
	r := func(status, conclusion string) ciRun { return ciRun{Status: status, Conclusion: conclusion} }
	failing := []ciRun{r("completed", "failure"), r("completed", "cancelled"), r("completed", "timed_out"), r("completed", "action_required"), r("completed", "stale")}
	for _, x := range failing {
		if !ciRunFailing(x) {
			t.Errorf("%+v should be failing", x)
		}
	}
	notFailing := []ciRun{
		r("completed", "success"), r("completed", "neutral"), r("completed", "skipped"),
		r("in_progress", ""), r("in_progress", "failure"), r("queued", ""),
		r("completed", ""), r("completed", "pending"), r("completed", "expected"),
	}
	for _, x := range notFailing {
		if ciRunFailing(x) {
			t.Errorf("%+v should not be failing", x)
		}
	}
	green := [][]ciRun{
		{r("completed", "success")},
		{r("completed", "success"), r("completed", "neutral"), r("completed", "skipped")},
	}
	for _, g := range green {
		if !ciGreen(g) {
			t.Errorf("%+v should be green", g)
		}
	}
	notGreen := [][]ciRun{
		nil,
		{},
		{r("completed", "success"), r("in_progress", "")},
		{r("completed", "success"), r("completed", "failure")},
		{r("completed", "pending")},
		{r("completed", "expected")},
		{r("queued", "")},
	}
	for _, g := range notGreen {
		if ciGreen(g) {
			t.Errorf("%+v should not be green", g)
		}
	}
}

func TestCIRunDecodingToleratesNumericIDAndMissingAttempt(t *testing.T) {
	v, err := view.Parse([]byte(`{"contract":"pg-desk.view/v1","type":"pr","id":"x","ci":{"runs":[{"id":123,"name":"n","status":"completed","conclusion":"failure"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	runs := ciRuns(v)
	if len(runs) != 1 || runs[0].ID != "123" || runs[0].Attempt != 0 {
		t.Fatalf("runs = %+v", runs)
	}
}
