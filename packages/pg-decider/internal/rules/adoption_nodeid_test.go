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

// The adoption.node-id rule's tests. Every helper here carries the "nodeid"
// prefix because sibling rule packets add _test.go files to this same Go
// package. The rule is always looked up through the registry, so each test
// also proves it registered under its id.

const nodeidRuleID = "adoption.node-id"

func nodeidLoadView(t *testing.T, name string) *view.View {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "nodeid", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := view.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func nodeidEval(t *testing.T, fixture string) decide.Result {
	t.Helper()
	v := nodeidLoadView(t, fixture)
	in := decide.Input{View: v, Items: workitem.BuildIndex(v), EntityType: decide.EntityTypePR}
	for _, r := range decide.RulesFor(decide.EntityTypePR) {
		if r.ID() == nodeidRuleID {
			return r.Evaluate(in)
		}
	}
	t.Fatalf("rule %q is not registered for %q", nodeidRuleID, decide.EntityTypePR)
	return decide.Result{}
}

func TestNodeIDBackfillRewritesDedupKey(t *testing.T) {
	want := []struct {
		kind, id, key string
	}{
		{"anchor", "bd-1", "pr:PR_node_42:anchor"},
		{"review-pr", "bd-2", "pr:PR_node_42:review-pr"}, // closed
		{"fix-ci", "bd-3", "pr:PR_node_42:fix-ci:9f3c1e2"},
		{"resolve-conflict", "bd-4", "pr:PR_node_42:resolve-conflict:feature/retry:9f3c1e2:main:b45e000"},
		{"process-feedback", "bd-5", "pr:PR_node_42:process-feedback:abc123"}, // human-parked
	}
	res := nodeidEval(t, "rewrite_all")
	if len(res.Actions) != len(want) {
		t.Fatalf("want %d updates, got %+v", len(want), res)
	}
	for i, w := range want {
		a := res.Actions[i]
		if a.Op != action.OpUpdate || a.Kind != w.kind || a.Target == nil || *a.Target != w.id {
			t.Fatalf("action %d = %+v", i, a)
		}
		// Only the dedup_key metadata: no status, label or other change.
		if !reflect.DeepEqual(a.Fields, action.Fields{Metadata: map[string]string{"dedup_key": w.key}}) {
			t.Fatalf("action %d (%s) must touch only the dedup_key, fields = %+v", i, w.id, a.Fields)
		}
		if a.Rule != "" && a.Rule != nodeidRuleID {
			t.Fatalf("action %d rule = %q", i, a.Rule)
		}
	}
}

func TestNodeIDBackfillYieldsNothing(t *testing.T) {
	cases := []struct{ name, fixture string }{
		{"idempotent: state after the rewrite (G6)", "rewrite_all_after"},
		{"snapshot has no node_id", "no_node_id"},
		{"item has no dedup_key (the adoption rule's job)", "no_dedup_key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := nodeidEval(t, tc.fixture)
			if len(res.Actions) != 0 {
				t.Fatalf("want no action, got %+v", res.Actions)
			}
			if res.Skip == nil || res.Skip.Reason != action.ReasonNotMatched {
				t.Fatalf("want skip %q, got %+v", action.ReasonNotMatched, res.Skip)
			}
		})
	}
}

func TestNodeIDBackfillRewritesOnlyTheItemsStillInRepoForm(t *testing.T) {
	res := nodeidEval(t, "rewrite_mixed")
	if len(res.Actions) != 1 || res.Actions[0].Target == nil || *res.Actions[0].Target != "bd-2" {
		t.Fatalf("only bd-2 is still in the <repo>#<n> form, got %+v", res.Actions)
	}
	if got := res.Actions[0].Fields.Metadata["dedup_key"]; got != "pr:PR_node_42:review-pr" {
		t.Fatalf("key = %q", got)
	}
}

func TestNodeIDBackfillRegisteredAfterAdoption(t *testing.T) {
	var ids []string
	for _, r := range decide.RulesFor(decide.EntityTypePR) {
		if r.ID() == "adoption" || r.ID() == nodeidRuleID {
			ids = append(ids, r.ID())
			if r.Kind() != "" {
				t.Errorf("%s: Kind() = %q, want \"\"", r.ID(), r.Kind())
			}
		}
	}
	if !reflect.DeepEqual(ids, []string{"adoption", nodeidRuleID}) {
		t.Fatalf("registered order = %v", ids)
	}
	if decide.OrdinalAdoptionNodeID != 56 {
		t.Fatalf("ordinal = %d, want 56", decide.OrdinalAdoptionNodeID)
	}
}
