package main

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/exitcode"
)

// The thread-activity rule ships in plan mode: `apply` holds its annotation
// back and says so in the plan it prints, and writes it only once
// thread_activity_mode is "apply".

func threadAnnotateAction() action.Action {
	key := action.ThreadActivityKey("example-thread-1", "example-msg-1")
	val := action.ThreadActivityValue{ThreadID: "example-thread-1", MessageID: "example-msg-1", Author: "example-reviewer"}.String()
	return action.Action{Op: action.OpAnnotate, Rule: action.ThreadActivityRuleID, Target: &key, Fields: action.Fields{Value: &val}}
}

func TestApplyHoldsThreadActivityInPlanMode(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	t.Setenv("PG_DECIDER_CONFIG", "")
	recs := recordWrites(t, []action.Action{threadAnnotateAction()}, `{"result":{}}`, "0")
	out, errOut, code := runCLI(t, "apply", "pr", "acme/widgets#42", "--json")
	if code != exitcode.OK || len(*recs) != 0 {
		t.Fatalf("exit %d err %q writes %+v", code, errOut, *recs)
	}
	if !strings.Contains(out, `"mode": "plan"`) || strings.Contains(out, `"op": "annotate"`) {
		t.Fatalf("the applied plan must show the held rule and no annotate: %s", out)
	}
}

func TestApplyWritesThreadActivityInApplyMode(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	lookupConfig(t, `"thread_activity_mode":"apply"`)
	recs := recordWrites(t, []action.Action{threadAnnotateAction()}, `{"result":{}}`, "0")
	_, errOut, code := runCLI(t, "apply", "pr", "acme/widgets#42")
	if code != exitcode.OK {
		t.Fatalf("exit %d err %q", code, errOut)
	}
	if len(*recs) != 1 || !strings.Contains((*recs)[0].args, "annotate acme/widgets#42 --key decider.pr-decider.thread-activity.example-thread-1.example-msg-1") {
		t.Fatalf("writes %+v", *recs)
	}
}
