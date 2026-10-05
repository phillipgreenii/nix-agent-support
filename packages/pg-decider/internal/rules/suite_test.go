package rules

// The cross-cutting suite: tests that only make sense once every PR rule is
// registered. They run against the REAL registry, through decide.Decide and
// decide.RulesFor only (never a rule's Evaluate), over the shared synthetic
// view fixtures in ../../testdata/views. Every helper here is prefixed
// "suite" because sibling packets add test files to this package.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/decide"
	"github.com/phillipgreenii/pg-decider/internal/plan"
	"github.com/phillipgreenii/pg-decider/internal/view"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

// suiteRuleIDs are the twelve PR rules the sibling packets register.
var suiteRuleIDs = []string{
	"all.closed", "all.reopened", "anchor.lazy", "anchor.backfill", "anchor.priority",
	"adoption", "adoption.node-id", "review.head-advanced", "feedback.digest-changed",
	"fixci.failing-on-head", "conflict.present", "land.ready",
}

func suiteRaw(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "views", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func suiteLoad(t *testing.T, name string) *view.View {
	t.Helper()
	v, err := view.Parse(suiteRaw(t, name))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return v
}

// suitePatch loads a fixture, applies mutate to its decoded JSON object and
// parses the result, so View.Raw stays consistent with the typed members.
func suitePatch(t *testing.T, name string, mutate func(m map[string]any)) *view.View {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(suiteRaw(t, name), &m); err != nil {
		t.Fatal(err)
	}
	mutate(m)
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	v, err := view.Parse(b)
	if err != nil {
		t.Fatalf("%s (patched): %v", name, err)
	}
	return v
}

func suiteAnnotations(m map[string]any) map[string]any { return m["annotations"].(map[string]any) }

func suiteHide(m map[string]any) {
	suiteAnnotations(m)["hidden"] = map[string]any{"value": true, "reason": "parked by the operator"}
}

func suiteSuppress(kinds ...string) func(m map[string]any) {
	return func(m map[string]any) { suiteAnnotations(m)["suppress"] = kinds }
}

func suiteDecide(v *view.View) action.PlanResult { return decide.Decide(v, decide.EntityTypePR) }

func suiteRegisteredIDs() []string {
	var ids []string
	for _, r := range decide.RulesFor(decide.EntityTypePR) {
		ids = append(ids, r.ID())
	}
	return ids
}

func suiteSkipReason(res action.PlanResult, rule string) (string, bool) {
	for _, s := range res.Skipped {
		if s.Rule == rule {
			return s.Reason, true
		}
	}
	return "", false
}

func suiteActionsOfRule(res action.PlanResult, rule string) []action.Action {
	var out []action.Action
	for _, a := range res.Actions {
		if a.Rule == rule {
			out = append(out, a)
		}
	}
	return out
}

func suiteActionsOfKind(res action.PlanResult, kind string) []action.Action {
	var out []action.Action
	for _, a := range res.Actions {
		if a.Kind == kind {
			out = append(out, a)
		}
	}
	return out
}

// suiteShape is "<op> <kind or annotation key> <rule>", the identity of one
// action for order-independent comparison.
func suiteShape(a action.Action) string {
	subject := a.Kind
	if subject == "" && a.Target != nil {
		subject = *a.Target
	}
	return fmt.Sprintf("%s %s %s", a.Op, subject, a.Rule)
}

func suiteShapes(res action.PlanResult) []string {
	out := []string{}
	for _, a := range res.Actions {
		out = append(out, suiteShape(a))
	}
	sort.Strings(out)
	return out
}

func suiteWantShapes(t *testing.T, name string, res action.PlanResult, want ...string) {
	t.Helper()
	sort.Strings(want)
	if want == nil {
		want = []string{}
	}
	if got := suiteShapes(res); !reflect.DeepEqual(got, want) {
		t.Errorf("%s: actions = %v, want %v", name, got, want)
	}
}

// The trigger fixtures between them make every registered rule act.
var suiteTriggerFixtures = []string{
	"pr_new_everything", "pr_anchor_drift", "pr_merged_open_anchor", "pr_reopened_anchor_closed",
}

func TestSuiteRegistryHoldsAllTwelvePRRules(t *testing.T) {
	have := map[string]bool{}
	for _, id := range suiteRegisteredIDs() {
		have[id] = true
	}
	for _, id := range suiteRuleIDs {
		if !have[id] {
			t.Errorf("rule %q is not registered", id)
		}
	}
}

func TestHiddenEntityYieldsZeroActions(t *testing.T) {
	ids := suiteRegisteredIDs()

	// Precondition: unhidden, the trigger views make EVERY registered rule
	// produce an action, so a hidden zero is not a vacuous zero.
	acted := map[string]bool{}
	for _, name := range suiteTriggerFixtures {
		for _, a := range suiteDecide(suiteLoad(t, name)).Actions {
			acted[a.Rule] = true
		}
	}
	for _, id := range ids {
		if !acted[id] {
			t.Errorf("rule %q acts on none of the trigger fixtures %v; the hidden check would be vacuous for it", id, suiteTriggerFixtures)
		}
	}

	cases := map[string]*view.View{"pr_hidden (committed)": suiteLoad(t, "pr_hidden")}
	for _, name := range suiteTriggerFixtures {
		cases[name+" (hidden)"] = suitePatch(t, name, suiteHide)
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			if !v.Annotations.Hidden.Value {
				t.Fatal("fixture is not hidden")
			}
			res := suiteDecide(v)
			if len(res.Actions) != 0 {
				t.Fatalf("hidden entity produced actions: %+v", res.Actions)
			}
			if len(res.Skipped) != len(ids) {
				t.Fatalf("skipped %d rules, registry has %d", len(res.Skipped), len(ids))
			}
			for i, s := range res.Skipped {
				if s.Rule != ids[i] || s.Reason != action.ReasonHidden {
					t.Errorf("skipped[%d] = %+v, want rule %q reason %q", i, s, ids[i], action.ReasonHidden)
				}
			}

			// plan lists every registered rule as skipped with reason hidden.
			var buf bytes.Buffer
			if err := plan.JSON(&buf, res); err != nil {
				t.Fatal(err)
			}
			var printed struct {
				Actions []json.RawMessage `json:"actions"`
				Skipped []struct{ Rule, Reason string }
			}
			if err := json.Unmarshal(buf.Bytes(), &printed); err != nil {
				t.Fatal(err)
			}
			if printed.Actions == nil || len(printed.Actions) != 0 {
				t.Errorf("plan actions = %v, want an empty list", printed.Actions)
			}
			listed := map[string]string{}
			for _, s := range printed.Skipped {
				listed[s.Rule] = s.Reason
			}
			for _, id := range ids {
				if listed[id] != action.ReasonHidden {
					t.Errorf("plan lists rule %q as %q, want %q", id, listed[id], action.ReasonHidden)
				}
			}
			var text bytes.Buffer
			if err := plan.Text(&text, v, res); err != nil {
				t.Fatal(err)
			}
			for _, id := range ids {
				if !strings.Contains(text.String(), id) {
					t.Errorf("text plan does not mention rule %q:\n%s", id, text.String())
				}
			}
		})
	}
}

// suiteTerminal returns a mutator that makes the snapshot a merged or closed
// PR, the two terminal states.
func suiteTerminal(state string) func(m map[string]any) {
	return func(m map[string]any) {
		snap := m["snapshot"].(map[string]any)
		snap["state"] = "closed"
		snap["merged"] = state == "merged"
	}
}

// A merged or closed PR is dead: no creating rule may open work for it. With
// no anchor and no work items every rule must skip, so nothing is created
// only for all.closed to close again on the next run (pg2-pjpwe).
func TestTerminalPRWithNoAnchorYieldsZeroActions(t *testing.T) {
	// Precondition: the same view, open, makes the creating rules act, so the
	// zero below is not vacuous.
	open := suiteDecide(suiteLoad(t, "pr_new_everything"))
	for _, rule := range []string{"review.head-advanced", "feedback.digest-changed", "fixci.failing-on-head", "conflict.present", "anchor.lazy"} {
		if len(suiteActionsOfRule(open, rule)) == 0 {
			t.Fatalf("open pr_new_everything: rule %s produced no action; the terminal check would be vacuous", rule)
		}
	}

	for _, state := range []string{"merged", "closed"} {
		t.Run(state, func(t *testing.T) {
			res := suiteDecide(suitePatch(t, "pr_new_everything", suiteTerminal(state)))
			if len(res.Actions) != 0 {
				t.Fatalf("%s PR with no anchor produced actions: %+v", state, res.Actions)
			}
			if got, want := len(res.Skipped), len(suiteRegisteredIDs()); got != want {
				t.Errorf("skipped %d rules, registry has %d", got, want)
			}
		})
	}
}

// A terminal PR gets no ready_to_land annotation either, and no work item
// rule reopens or updates an existing item for it (only the anchor group
// writes: the all.closed cascade and anchor.backfill's mirror).
func TestTerminalPRNeverGainsReadinessOrReopensWork(t *testing.T) {
	for _, state := range []string{"merged", "closed"} {
		t.Run(state, func(t *testing.T) {
			res := suiteDecide(suitePatch(t, "pr_land_ready", suiteTerminal(state)))
			for _, a := range res.Actions {
				// The anchor group keeps mirroring and closing; nothing else acts.
				if a.Rule != "all.closed" && a.Rule != "anchor.backfill" {
					t.Errorf("%s PR: rule %s acted: %s", state, a.Rule, suiteShape(a))
				}
			}
			if r, ok := suiteSkipReason(res, "land.ready"); !ok || r != action.ReasonNotMatched {
				t.Errorf("land.ready = %q (listed %v), want %q", r, ok, action.ReasonNotMatched)
			}
		})
	}
}

func TestSuppressKindSkipsOnlyThatKind(t *testing.T) {
	ruleOfKind := map[workitem.Kind]string{}
	for _, r := range decide.RulesFor(decide.EntityTypePR) {
		if k := r.Kind(); k != "" {
			if prev, dup := ruleOfKind[k]; dup {
				t.Fatalf("kind %q is governed by both %q and %q", k, prev, r.ID())
			}
			ruleOfKind[k] = r.ID()
		}
	}

	baseline := suiteDecide(suiteLoad(t, "pr_all_kinds"))
	kinds := []workitem.Kind{workitem.KindReviewPR, workitem.KindFixCI, workitem.KindResolveConflict, workitem.KindProcessFeedback}
	for _, k := range kinds {
		if len(suiteActionsOfKind(baseline, string(k))) == 0 {
			t.Fatalf("the all-kinds fixture yields no %s action; the suppression check would be vacuous", k)
		}
	}

	for _, k := range kinds {
		t.Run(string(k), func(t *testing.T) {
			rule, ok := ruleOfKind[k]
			if !ok {
				t.Fatalf("no registered rule governs kind %q", k)
			}
			res := suiteDecide(suitePatch(t, "pr_all_kinds", suiteSuppress(string(k))))

			if got := suiteActionsOfKind(res, string(k)); len(got) != 0 {
				t.Errorf("suppressed kind %s still acted: %+v", k, got)
			}
			if reason, ok := suiteSkipReason(res, rule); !ok || reason != action.ReasonSuppressed {
				t.Errorf("rule %s skip = %q (listed %v), want %q", rule, reason, ok, action.ReasonSuppressed)
			}
			for _, s := range res.Skipped {
				if s.Reason == action.ReasonSuppressed && s.Rule != rule {
					t.Errorf("rule %s was suppressed too, but only kind %s is", s.Rule, k)
				}
			}

			// Every other action is exactly what the unsuppressed view yields.
			var want []action.Action
			for _, a := range baseline.Actions {
				if a.Kind != string(k) {
					want = append(want, a)
				}
			}
			if !reflect.DeepEqual(res.Actions, want) {
				t.Errorf("other actions changed:\n got %+v\nwant %+v", res.Actions, want)
			}
		})
	}
}

func TestLandReadyNeverCreatesWorkItem(t *testing.T) {
	const rule = "land.ready"
	ready := func(t *testing.T) *view.View { return suiteLoad(t, "pr_land_ready") }
	cases := []struct {
		name string
		load func(t *testing.T) *view.View
		want []string // annotate values land.ready writes; none when empty
	}{
		{"ready", ready, []string{"true"}},
		{"ready and already annotated true", func(t *testing.T) *view.View { return suiteLoad(t, "pr_land_ready_annotated") }, nil},
		{"not ready, annotated true", func(t *testing.T) *view.View { return suiteLoad(t, "pr_land_not_ready_annotated") }, []string{"false"}},
		{"not ready", func(t *testing.T) *view.View { return suiteLoad(t, "pr_land_not_ready") }, nil},
		{"team", func(t *testing.T) *view.View { return suiteLoad(t, "pr_team_ready") }, nil},
		{"hidden", func(t *testing.T) *view.View { return suitePatch(t, "pr_land_ready", suiteHide) }, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := suiteDecide(tc.load(t))
			var got []string
			for _, a := range suiteActionsOfRule(res, rule) {
				if a.Op != action.OpAnnotate {
					t.Errorf("land.ready produced a %s action, only annotate is allowed: %+v", a.Op, a)
				}
				if a.Kind != "" {
					t.Errorf("land.ready produced a work item action of kind %q: %+v", a.Kind, a)
				}
				if a.Target == nil || *a.Target != "ready_to_land" || a.Fields.Value == nil {
					t.Errorf("land.ready annotation shape: %+v", a)
					continue
				}
				got = append(got, *a.Fields.Value)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("land.ready annotations = %v, want %v", got, tc.want)
			}
			if len(tc.want) == 0 {
				if _, skipped := suiteSkipReason(res, rule); !skipped {
					t.Errorf("land.ready neither acted nor was listed as skipped")
				}
			}
		})
	}
}

func TestTeamPRGetsOnlyOperatorPendingReview(t *testing.T) { // S16
	const (
		anchorCreate = "create anchor anchor.lazy"
		reviewCreate = "create review-pr review.head-advanced"
		priority     = "update anchor anchor.priority"
	)
	cases := []struct {
		fixture string
		want    []string
	}{
		// Unaddressed comments, failing CI, a conflict: only the review item
		// (and the anchor create it needs).
		{"pr_team_trouble", []string{anchorCreate, reviewCreate}},
		// Green and approved: still no ready_to_land annotation.
		{"pr_team_ready", []string{anchorCreate, reviewCreate}},
		// With an anchor and a conflict, the design's rule table (anchor.priority:
		// team lowers) adds exactly one anchor update.
		{"pr_team_anchor_conflict", []string{priority, reviewCreate}},
		// With the review item already in place: that update alone, no work-item
		// create.
		{"pr_team_anchor_conflict_reviewed", []string{priority}},
	}
	excludedKinds := []string{"process-feedback", "fix-ci", "resolve-conflict"}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			res := suiteDecide(suiteLoad(t, tc.fixture))
			suiteWantShapes(t, tc.fixture, res, tc.want...)
			for _, a := range res.Actions {
				for _, k := range excludedKinds {
					if a.Kind == k {
						t.Errorf("team PR got a %s action: %+v", k, a)
					}
				}
				if a.Target != nil && *a.Target == "ready_to_land" {
					t.Errorf("team PR got a ready_to_land annotation: %+v", a)
				}
			}
			for _, rule := range []string{"feedback.digest-changed", "fixci.failing-on-head", "conflict.present", "land.ready"} {
				if reason, ok := suiteSkipReason(res, rule); !ok || reason != action.ReasonNotMatched {
					t.Errorf("rule %s skip = %q (listed %v), want %q", rule, reason, ok, action.ReasonNotMatched)
				}
			}
		})
	}

	t.Run("the team anchor update lowers the priority", func(t *testing.T) {
		res := suiteDecide(suiteLoad(t, "pr_team_anchor_conflict_reviewed"))
		if len(res.Actions) != 1 {
			t.Fatalf("actions = %+v", res.Actions)
		}
		a := res.Actions[0]
		if a.Target == nil || *a.Target != "bd-1" || a.Fields.Priority != "P3" ||
			!reflect.DeepEqual(a.Fields.AddLabels, []string{"pbase:2"}) {
			t.Errorf("anchor update: %+v", a)
		}
	})
}

func TestWorkItemForCurrentContextNeverRecreated(t *testing.T) { // S26
	kinds := []struct {
		kind    workitem.Kind
		rule    string
		closed  string // fixture name stem: pr_ctx_<stem>_closed_<reason>
		rearmed []struct {
			fixture string
			ops     []action.Op
		}
	}{
		{workitem.KindReviewPR, "review.head-advanced", "review", []struct {
			fixture string
			ops     []action.Op
		}{{"pr_ctx_review_new_head", []action.Op{action.OpReopen, action.OpUpdate}}}},
		{workitem.KindProcessFeedback, "feedback.digest-changed", "feedback", []struct {
			fixture string
			ops     []action.Op
		}{{"pr_ctx_feedback_new_comment", []action.Op{action.OpCreate}}}},
		{workitem.KindResolveConflict, "conflict.present", "conflict", []struct {
			fixture string
			ops     []action.Op
		}{{"pr_ctx_conflict_new_tuple", []action.Op{action.OpCreate}}}},
		{workitem.KindFixCI, "fixci.failing-on-head", "fixci", []struct {
			fixture string
			ops     []action.Op
		}{
			{"pr_ctx_fixci_new_head", []action.Op{action.OpCreate}},
			{"pr_ctx_fixci_new_build", []action.Op{action.OpReopen, action.OpUpdate}},
		}},
	}

	for _, kc := range kinds {
		t.Run(string(kc.kind)+"/closed item is never recreated", func(t *testing.T) {
			var first action.PlanResult
			for i, reason := range []string{"done", "wontfix", "duplicate"} {
				name := fmt.Sprintf("pr_ctx_%s_closed_%s", kc.closed, reason)
				v := suiteLoad(t, name)
				suiteWantClosedWith(t, v, kc.kind, reason)
				res := suiteDecide(v)

				for _, a := range suiteActionsOfKind(res, string(kc.kind)) {
					if a.Op == action.OpCreate || a.Op == action.OpReopen {
						t.Errorf("%s: closed %s item was %sd again: %+v", name, kc.kind, a.Op, a)
					}
				}
				if len(res.Actions) != 0 {
					t.Errorf("%s: want no actions at all, got %+v", name, res.Actions)
				}
				if got, ok := suiteSkipReason(res, kc.rule); !ok || got != action.ReasonAlreadyHandled {
					t.Errorf("%s: rule %s skip = %q (listed %v), want %q", name, kc.rule, got, ok, action.ReasonAlreadyHandled)
				}
				// No rule depends on who closed the item: identical output.
				if i == 0 {
					first = res
				} else if !reflect.DeepEqual(res, first) {
					t.Errorf("%s: output differs from the done-closed output:\n got %+v\nwant %+v", name, res, first)
				}
			}
		})

		for _, rc := range kc.rearmed {
			t.Run(string(kc.kind)+"/"+rc.fixture+" re-arms exactly that kind", func(t *testing.T) {
				res := suiteDecide(suiteLoad(t, rc.fixture))
				var ops []action.Op
				for _, a := range res.Actions {
					if a.Kind != string(kc.kind) || a.Rule != kc.rule {
						t.Errorf("action outside kind %s: %+v", kc.kind, a)
					}
					ops = append(ops, a.Op)
				}
				if !reflect.DeepEqual(ops, rc.ops) {
					t.Errorf("ops = %v, want %v (actions %+v)", ops, rc.ops, res.Actions)
				}
			})
		}
	}
}

// suiteWantClosedWith checks the fixture's work item of kind k really is
// closed and carries the close_reason the test name claims.
func suiteWantClosedWith(t *testing.T, v *view.View, k workitem.Kind, reason string) {
	t.Helper()
	items := workitem.BuildIndex(v).ByKind(k)
	if len(items) != 1 {
		t.Fatalf("want exactly one %s item, got %d", k, len(items))
	}
	if items[0].Open() || items[0].Metadata["close_reason"] != reason {
		t.Fatalf("%s item state=%q close_reason=%q, want closed/%s", k, items[0].State, items[0].Metadata["close_reason"], reason)
	}
}
