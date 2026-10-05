package rules

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/decide"
	"github.com/phillipgreenii/pg-decider/internal/view"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

const conflictRuleID = "conflict.present"

func conflictLoadView(t *testing.T, name string) *view.View {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "conflict", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := view.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func conflictEvaluate(t *testing.T, name string) decide.Result {
	t.Helper()
	v := conflictLoadView(t, name)
	in := decide.Input{View: v, Items: workitem.BuildIndex(v), EntityType: decide.EntityTypePR}
	return conflictRule{}.Evaluate(in)
}

func conflictWantSkip(t *testing.T, name, reason string) {
	t.Helper()
	res := conflictEvaluate(t, name)
	if len(res.Actions) != 0 || res.Skip == nil || res.Skip.Reason != reason {
		t.Fatalf("%s: want no action and skip %q, got %+v (skip %+v)", name, reason, res.Actions, res.Skip)
	}
}

func TestConflictRegisteredForPR(t *testing.T) {
	var found bool
	for _, r := range decide.RulesFor(decide.EntityTypePR) {
		if r.ID() == conflictRuleID {
			found = true
			if r.Kind() != workitem.KindResolveConflict {
				t.Fatalf("Kind() = %q", r.Kind())
			}
		}
	}
	if !found {
		t.Fatalf("rule %q is not registered for %q", conflictRuleID, decide.EntityTypePR)
	}
	if decide.OrdinalConflictPresent != 90 {
		t.Fatalf("ordinal constant = %d", decide.OrdinalConflictPresent)
	}
}

func TestConflictCreate(t *testing.T) {
	cases := []struct {
		fixture, parent             string
		branch, head, base, baseSHA string
	}{
		{"conflicting_no_item", "bd-1", "feature/retry", "9f3c1e2", "main", "b45e000"},
		{"conflicting_coowned", "bd-1", "feature/retry", "9f3c1e2", "main", "b45e000"},
		{"conflicting_no_anchor", action.AnchorParent, "feature/retry", "9f3c1e2", "main", "b45e000"},
		{"conflicting_lowercase", "bd-1", "feature/retry", "9f3c1e2", "main", "b45e000"},
		// A different context is a different conflict: one tuple member each.
		{"new_base_sha_old_closed", "bd-1", "feature/retry", "9f3c1e2", "main", "b45e000"},
		{"new_base_sha_old_open", "bd-1", "feature/retry", "9f3c1e2", "main", "b45e000"},
		{"new_head_sha_old_closed", "bd-1", "feature/retry", "9f3c1e2", "main", "b45e000"},
		{"new_branch_old_closed", "bd-1", "feature/retry", "9f3c1e2", "main", "b45e000"},
		{"new_base_old_closed", "bd-1", "feature/retry", "9f3c1e2", "main", "b45e000"},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			res := conflictEvaluate(t, tc.fixture)
			if len(res.Actions) != 1 || res.Skip != nil {
				t.Fatalf("want exactly one create, got %+v", res)
			}
			a := res.Actions[0]
			if a.Op != action.OpCreate || a.Kind != "resolve-conflict" || a.Target != nil {
				t.Fatalf("action = %+v", a)
			}
			f := a.Fields
			if f.Title != "resolve-conflict: acme/widgets#42" || f.IssueType != "task" || f.Parent != tc.parent {
				t.Fatalf("fields = %+v", f)
			}
			if !reflect.DeepEqual(f.Labels, []string{"mine", "worker-ready"}) {
				t.Fatalf("labels = %v", f.Labels)
			}
			want := map[string]string{
				"repo": "acme/widgets", "pr_number": "42", "branch": tc.branch,
				"head_sha": tc.head, "base": tc.base, "base_sha": tc.baseSHA,
				"dedup_key": "pr:acme/widgets#42:resolve-conflict:" + tc.branch + ":" + tc.head + ":" + tc.base + ":" + tc.baseSHA,
			}
			if !reflect.DeepEqual(f.Metadata, want) {
				t.Fatalf("metadata = %v\nwant       %v", f.Metadata, want)
			}
			if !strings.Contains(f.Description, "main") || !strings.Contains(strings.ToLower(f.Description), "rebase") {
				t.Fatalf("description lacks the rebase-onto-base instruction: %q", f.Description)
			}
		})
	}
}

func TestConflictNewContextKeyDiffersFromEveryExistingItem(t *testing.T) {
	// The dedup key of the created item names the whole current context, so it
	// is not the key of the item left over from the old context.
	for _, name := range []string{"new_base_sha_old_closed", "new_head_sha_old_closed", "new_branch_old_closed", "new_base_old_closed"} {
		v := conflictLoadView(t, name)
		old := workitem.BuildIndex(v).ByKind(workitem.KindResolveConflict)
		res := conflictEvaluate(t, name)
		if len(old) != 1 || len(res.Actions) != 1 {
			t.Fatalf("%s: old=%d actions=%d", name, len(old), len(res.Actions))
		}
		if res.Actions[0].Fields.Metadata["dedup_key"] == old[0].DedupKey {
			t.Errorf("%s: new key equals old key %q", name, old[0].DedupKey)
		}
	}
}

func TestConflictNotMatched(t *testing.T) {
	for _, name := range []string{
		"conflicting_team",        // not mine or co-owned
		"conflicting_no_base_sha", // context incomplete: never create a keyless item
		"cleared_no_item",
		"cleared_item_closed",
		"cleared_open_claimed", // left for the worker
		"cleared_team_open_item",
		"unknown_mergeable_no_item",
		"unknown_mergeable_open_item", // neither conflicting nor cleared
	} {
		t.Run(name, func(t *testing.T) { conflictWantSkip(t, name, action.ReasonNotMatched) })
	}
}

func TestConflictSameContextIsAlreadyHandledAndNeverReopened(t *testing.T) {
	for _, name := range []string{
		"item_open_same", "item_open_same_claimed",
		"item_closed_done", "item_closed_wontfix", "item_closed_duplicate", "item_closed_no_reason",
		"item_legacy_no_key",
	} {
		t.Run(name, func(t *testing.T) {
			conflictWantSkip(t, name, action.ReasonAlreadyHandled)
			for _, a := range conflictEvaluate(t, name).Actions {
				if a.Op == action.OpReopen {
					t.Fatalf("reopen produced: %+v", a)
				}
			}
		})
	}
}

func TestConflictClosesClearedOpenUnclaimedItem(t *testing.T) {
	res := conflictEvaluate(t, "cleared_open_unclaimed")
	if len(res.Actions) != 1 || res.Skip != nil {
		t.Fatalf("want exactly one close, got %+v", res)
	}
	a := res.Actions[0]
	if a.Op != action.OpClose || a.Kind != "resolve-conflict" || a.Target == nil || *a.Target != "bd-4" {
		t.Fatalf("action = %+v", a)
	}
}

// Idempotency (G6): the after fixture is hand-written to represent the state
// once the first fixture's action was applied.
func TestConflictIdempotentOnAppliedState(t *testing.T) {
	cases := []struct {
		before, after string
		op            action.Op
	}{
		{"conflicting_no_item", "item_open_same", action.OpCreate},
		{"conflicting_coowned", "item_open_same", action.OpCreate},
		{"new_base_sha_old_closed", "new_base_sha_applied", action.OpCreate},
		{"cleared_open_unclaimed", "cleared_item_closed", action.OpClose},
	}
	for _, tc := range cases {
		t.Run(tc.before, func(t *testing.T) {
			res := conflictEvaluate(t, tc.before)
			if len(res.Actions) != 1 || res.Actions[0].Op != tc.op {
				t.Fatalf("before: %+v", res)
			}
			if after := conflictEvaluate(t, tc.after); len(after.Actions) != 0 {
				t.Fatalf("after %s still acts: %+v", tc.after, after.Actions)
			}
		})
	}
}
