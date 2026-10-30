package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/exitcode"
	"github.com/phillipgreenii/pg-decider/internal/view"
)

// `apply --json` prints the plan it applied on stdout, the same object `plan
// --json` prints, so pg-pr-change needs no separate pre-apply plan.

func TestApplyJSONPrintsThePlanObjectOfPlanJSON(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	t.Setenv("PG_DECIDER_CONFIG", "")
	scriptedWrites(t, func(*view.View) []action.Action { return nil }, func(string, string) (string, string) { return `{"result":{}}`, "0" })
	out, errOut, code := runCLI(t, "apply", "pr", "acme/widgets#42", "--json")
	if code != 0 {
		t.Fatalf("exit %d err %q", code, errOut)
	}
	if want := "{\n  \"actions\": [],\n  \"skipped\": []\n}\n"; out != want {
		t.Fatalf("apply --json stdout %q, want %q", out, want)
	}
	// Without --json apply prints nothing on stdout.
	if plain, _, _ := runCLI(t, "apply", "pr", "acme/widgets#42"); plain != "" {
		t.Fatalf("apply without --json printed %q", plain)
	}
}

func TestApplyJSONListsEachActionWithItsOpAndTheSkippedRulesOnPartialFailure(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_failing.json"))
	t.Setenv("PG_DECIDER_CONFIG", "")
	target := "wb-fail"
	scriptedWrites(t,
		func(*view.View) []action.Action {
			return []action.Action{
				{Op: action.OpCreate, Kind: "review-pr", Rule: "r.ok", Fields: action.Fields{Title: "ok", Metadata: map[string]string{"dedup_key": "pr:acme/widgets#42:review-pr:aaa"}}},
				{Op: action.OpUpdate, Rule: "r.fail", Target: &target, Fields: action.Fields{Title: "x"}},
			}
		},
		func(_, args string) (string, string) {
			switch {
			case strings.HasPrefix(args, "issue create --title ok"):
				return `{"result":{"id":"wb-ok"}}`, "0"
			case strings.HasPrefix(args, "issue update wb-fail"):
				return `{"error":{"code":"unavailable","message":"tracker down"}}`, "1"
			}
			return `{"result":{}}`, "0"
		})
	out, errOut, code := runCLI(t, "apply", "pr", "acme/widgets#42", "--json")
	if code != exitcode.Partial {
		t.Fatalf("exit %d err %q", code, errOut)
	}
	var got struct {
		Actions []struct{ Op, Rule, Kind string }
		Skipped []map[string]any
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout %q is not the plan object: %v", out, err)
	}
	if len(got.Actions) != 2 || got.Actions[0].Op != "create" || got.Actions[0].Rule != "r.ok" || got.Actions[1].Op != "update" || got.Actions[1].Rule != "r.fail" {
		t.Fatalf("actions %+v", got.Actions)
	}
	if got.Skipped == nil {
		t.Fatalf("skipped member missing: %q", out)
	}
}

func TestApplyJSONPrintsNothingWhenTheRunFailsBeforeApplying(t *testing.T) {
	withHelper(t, "GO_HELPER_EXIT=1")
	out, _, code := runCLI(t, "apply", "pr", "acme/widgets#42", "--json")
	if code == 0 || out != "" {
		t.Fatalf("exit %d stdout %q", code, out)
	}
}
