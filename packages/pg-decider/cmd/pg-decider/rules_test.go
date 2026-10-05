package main

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/decide"
	"github.com/phillipgreenii/pg-decider/internal/exitcode"
)

// The binary's registry is populated by the blank import in rules.go.
func TestBinaryRegistryHoldsTheRules(t *testing.T) {
	rules := decide.RulesFor(decide.EntityTypePR)
	if len(rules) < 6 {
		t.Fatalf("len(RulesFor(pr)) = %d, want at least 6", len(rules))
	}
	have := map[string]bool{}
	for _, r := range rules {
		have[r.ID()] = true
	}
	for _, id := range []string{"all.closed", "all.reopened", "anchor.lazy", "anchor.backfill", "anchor.priority", "adoption"} {
		if !have[id] {
			t.Errorf("rule %q is not registered in the binary", id)
		}
	}
}

// plan, run against a view through the pg-desk double, lists the registered
// rules in its skipped and actions output.
func TestPlanListsTheRegisteredRules(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	out, errOut, code := runCLI(t, "plan", "pr", "acme/widgets#42", "--json")
	if code != exitcode.OK || errOut != "" {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	var plan struct {
		Actions []struct{ Rule string } `json:"actions"`
		Skipped []struct{ Rule string } `json:"skipped"`
	}
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	seen := map[string]bool{}
	for _, a := range plan.Actions {
		seen[a.Rule] = true
	}
	for _, s := range plan.Skipped {
		seen[s.Rule] = true
	}
	for _, id := range []string{"all.closed", "all.reopened", "anchor.lazy", "anchor.backfill", "anchor.priority", "adoption"} {
		if !seen[id] {
			t.Errorf("rule %q is in neither actions nor skipped:\n%s", id, out)
		}
	}
}
