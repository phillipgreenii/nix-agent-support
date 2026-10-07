package rules

// Registry-wide terminal-PR guard (pg2-umq37, follow-up to pg2-pjpwe).
//
// The liveness gate for a merged or closed PR is applied per rule (design
// 7.3 has no central engine gate for it), so nothing but a test stops a newly
// registered PR rule from forgetting it. This test enumerates the registry
// at run time (no rule id is named for the rules under test), so a rule added
// later is covered automatically and fails CI if it emits an action for a
// dead PR that has no anchor and no work items.

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/decide"
	"github.com/phillipgreenii/pg-decider/internal/view"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

// terminalNoAnchorFixtures are the committed terminal views with no anchor and
// no work items; the PR is dead, so there is nothing for any rule to do.
var terminalNoAnchorFixtures = []string{"pr_merged_no_anchor", "pr_closed_no_anchor"}

// terminalAnchorGroup reports whether a rule belongs to the anchor group
// (all.closed, all.reopened, anchor.*, adoption), the only rules that keep
// evaluating a terminal PR because mirroring and closing the anchor is their
// job. Every other rule MUST skip a terminal PR.
func terminalAnchorGroup(id string) bool {
	return id == "all.closed" || id == "all.reopened" || id == "adoption" || strings.HasPrefix(id, "anchor.")
}

// terminalViolations evaluates every rule outside the anchor group against v
// and describes each one that emits an action.
func terminalViolations(rules []decide.Rule, v *view.View) []string {
	in := decide.Input{View: v, Items: workitem.BuildIndex(v), EntityType: decide.EntityTypePR}
	var out []string
	for _, r := range rules {
		if terminalAnchorGroup(r.ID()) {
			continue
		}
		for _, a := range r.Evaluate(in).Actions {
			out = append(out, r.ID()+": "+suiteShape(a))
		}
	}
	return out
}

// Every registered rule outside the anchor group emits nothing for a merged or
// closed PR with no anchor.
func TestEveryNonAnchorRuleSkipsTerminalPRWithNoAnchor(t *testing.T) {
	rules := decide.RulesFor(decide.EntityTypePR)

	var guarded int
	for _, r := range rules {
		if !terminalAnchorGroup(r.ID()) {
			guarded++
		}
	}
	if guarded == 0 {
		t.Fatal("no rule outside the anchor group is registered; the registry enumeration is broken")
	}

	for _, name := range terminalNoAnchorFixtures {
		t.Run(name, func(t *testing.T) {
			v := suiteLoad(t, name)
			if len(v.Links) != 0 {
				t.Fatalf("fixture %s has %d links; it must have no anchor and no work items", name, len(v.Links))
			}
			if got := anchorPRState(v.Snapshot); got != anchorPRStateClosed && got != anchorPRStateMerged {
				t.Fatalf("fixture %s is not terminal: pr_state %q", name, got)
			}
			for _, viol := range terminalViolations(rules, v) {
				t.Errorf("non-anchor rule acted on a terminal PR with no anchor (missing anchorTerminalSkip guard?): %s", viol)
			}
		})
	}
}

// The check is not vacuous: the same views made OPEN make the non-anchor rules
// act, so a rule that omits the guard would be caught, not silently pass.
func TestTerminalGuardTestIsNotVacuous(t *testing.T) {
	rules := decide.RulesFor(decide.EntityTypePR)
	for _, name := range terminalNoAnchorFixtures {
		t.Run(name, func(t *testing.T) {
			open := suitePatch(t, name, func(m map[string]any) {
				snap := m["snapshot"].(map[string]any)
				snap["state"] = "open"
				snap["merged"] = false
			})
			if len(terminalViolations(rules, open)) == 0 {
				t.Fatalf("%s made open: no rule outside the anchor group acts, so the terminal check proves nothing", name)
			}
		})
	}
}

// unguardedRule is a stand-in for a PR rule that forgot the terminal guard: it
// creates a work item whatever the PR state is.
type unguardedRule struct{}

func (unguardedRule) ID() string          { return "test.unguarded" }
func (unguardedRule) Kind() workitem.Kind { return workitem.KindReviewPR }
func (unguardedRule) Evaluate(decide.Input) decide.Result {
	return decide.Result{Actions: []action.Action{{
		Op: action.OpCreate, Kind: string(workitem.KindReviewPR),
		Fields: action.Fields{Parent: action.AnchorParent},
	}}}
}

// The harness reports a rule without the guard (and exempts an anchor-group id).
func TestTerminalViolationsFlagsAnUnguardedRule(t *testing.T) {
	v := suiteLoad(t, "pr_merged_no_anchor")
	got := terminalViolations([]decide.Rule{unguardedRule{}}, v)
	if len(got) != 1 || !strings.HasPrefix(got[0], "test.unguarded: ") {
		t.Fatalf("violations = %v, want exactly one from test.unguarded", got)
	}
	// The same rule, were it an anchor-group member, is exempt by design.
	if terminalAnchorGroup("test.unguarded") {
		t.Fatal("test.unguarded must not be treated as an anchor-group rule")
	}
	for _, id := range []string{"all.closed", "all.reopened", "anchor.lazy", "anchor.backfill", "anchor.priority", "adoption"} {
		if !terminalAnchorGroup(id) {
			t.Errorf("%s must be in the anchor group", id)
		}
	}
}
