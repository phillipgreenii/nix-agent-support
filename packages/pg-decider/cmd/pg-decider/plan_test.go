package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/exitcode"
)

// These tests exercise the real planFn (no seam stubs) against the pg-desk
// test double, with whatever rules the binary registers (none yet).

func TestPlanTextForPRWithNoRulesPrintsHeaderAndEmptyBlocks(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	out, errOut, code := runCLI(t, "plan", "pr", "acme/widgets#42")
	if code != exitcode.OK || errOut != "" {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	lines := strings.Split(out, "\n")
	wantHeader := "pr acme/widgets#42  mine  open  ready  head=9f3c1e2  ci=success  conflict=no"
	if lines[0] != wantHeader {
		t.Fatalf("header = %q, want %q", lines[0], wantHeader)
	}
	if !strings.Contains(out, "\nactions:\n") || !strings.Contains(out, "\nskipped:\n") {
		t.Fatalf("missing blocks:\n%s", out)
	}
	if !strings.Contains(out, "linked work: ") {
		t.Fatalf("missing linked work line:\n%s", out)
	}
}

func TestPlanJSONForPRWithNoRules(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	out, errOut, code := runCLI(t, "plan", "pr", "acme/widgets#42", "--json")
	if code != exitcode.OK || errOut != "" {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	a, aok := got["actions"].([]any)
	s, sok := got["skipped"].([]any)
	if len(got) != 2 || !aok || !sok || len(a) != 0 || len(s) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestPlanWithoutADeciderForTheTypeExits1(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	for _, typ := range []string{"issue", "thread"} {
		out, errOut, code := runCLI(t, "plan", typ, "x")
		if code != 1 || out != "" || !strings.Contains(errOut, "no decider") || !strings.Contains(errOut, typ) {
			t.Fatalf("%s: code=%d out=%q err=%q", typ, code, out, errOut)
		}
	}
}

func TestPlanUnreadableViewStillExits3(t *testing.T) {
	withHelper(t, "GO_HELPER_EXIT=1", "GO_HELPER_STDERR=no such entity")
	out, _, code := runCLI(t, "plan", "pr", "x", "--json")
	if code != exitcode.ViewUnreadable || out != "" {
		t.Fatalf("code=%d out=%q", code, out)
	}
}
