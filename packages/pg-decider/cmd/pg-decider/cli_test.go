package main

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/exitcode"
	"github.com/phillipgreenii/pg-decider/internal/item"
	"github.com/phillipgreenii/pg-decider/internal/view"
)

func fixture(name string) string { return filepath.Join("..", "..", "testdata", name) }

// stubSeams swaps planFn/applyFn for recorders and restores them afterwards.
type seamCalls struct {
	planTyp, planID string
	planJSON        bool
	planView        *view.View
	applyTyp        string
	applyID         string
	applyItem       *item.Routed
	applyView       *view.View
	calls           int
}

func stubSeams(t *testing.T, planRC, applyRC int) *seamCalls {
	t.Helper()
	c := &seamCalls{}
	origPlan, origApply := planFn, applyFn
	t.Cleanup(func() { planFn, applyFn = origPlan, origApply })
	planFn = func(ctx context.Context, out, errOut io.Writer, typ, id string, asJSON bool) int {
		c.calls++
		c.planTyp, c.planID, c.planJSON = typ, id, asJSON
		c.planView = viewFromContext(ctx)
		return planRC
	}
	applyFn = func(ctx context.Context, out, errOut io.Writer, typ, id string, it *item.Routed) int {
		c.calls++
		c.applyTyp, c.applyID, c.applyItem = typ, id, it
		c.applyView = viewFromContext(ctx)
		return applyRC
	}
	return c
}

func TestPlanReadsViewAndCallsSeam(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	c := stubSeams(t, 0, 0)
	_, _, code := runCLI(t, "plan", "pr", "acme/widgets#42", "--json")
	if code != exitcode.OK {
		t.Fatalf("exit = %d", code)
	}
	if c.planTyp != "pr" || c.planID != "acme/widgets#42" || !c.planJSON {
		t.Fatalf("seam args: %+v", c)
	}
	if c.planView == nil || c.planView.ID != "acme/widgets#42" {
		t.Fatalf("seam did not receive the decoded view via context: %+v", c.planView)
	}
}

func TestPlanWithoutJSONFlag(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	c := stubSeams(t, 0, 0)
	if _, _, code := runCLI(t, "plan", "pr", "acme/widgets#42"); code != 0 || c.planJSON {
		t.Fatalf("code=%d json=%v", code, c.planJSON)
	}
}

func TestSeamExitCodeIsPropagated(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	stubSeams(t, exitcode.Partial, exitcode.Partial)
	if _, _, code := runCLI(t, "plan", "pr", "x"); code != exitcode.Partial {
		t.Fatalf("plan exit = %d", code)
	}
	if _, _, code := runCLI(t, "apply", "pr", "x"); code != exitcode.Partial {
		t.Fatalf("apply exit = %d", code)
	}
}

func TestDefaultSeamsReportNotImplemented(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	for _, sub := range []string{"plan", "apply"} {
		out, errOut, code := runCLI(t, sub, "pr", "x")
		if code != 1 || out != "" || !strings.Contains(errOut, "not implemented") {
			t.Fatalf("%s: code=%d out=%q err=%q", sub, code, out, errOut)
		}
	}
}

func TestUnreadableViewExits3WithNothingOnStdoutAndSeamNotCalled(t *testing.T) {
	cases := map[string][]string{
		"pg-desk failure": {"GO_HELPER_EXIT=1", "GO_HELPER_STDERR=no such entity"},
		"unparseable":     {"GO_HELPER_STDOUT=garbage"},
		"wrong contract":  {"GO_HELPER_STDOUT_FILE=" + fixture("pr_view_wrong_contract.json")},
	}
	for name, env := range cases {
		for _, args := range [][]string{{"plan", "pr", "x", "--json"}, {"apply", "pr", "x"}} {
			t.Run(name+"/"+args[0], func(t *testing.T) {
				withHelper(t, env...)
				c := stubSeams(t, 0, 0)
				out, errOut, code := runCLI(t, args...)
				if code != exitcode.ViewUnreadable {
					t.Fatalf("exit = %d, want 3", code)
				}
				if out != "" {
					t.Fatalf("stdout must be empty, got %q", out)
				}
				if !strings.Contains(errOut, "pg-desk") {
					t.Fatalf("stderr must name pg-desk: %q", errOut)
				}
				if c.calls != 0 {
					t.Fatalf("seam must not run on an unreadable view")
				}
			})
		}
	}
}

func TestApplyFromItemPathStillReReadsView(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	c := stubSeams(t, 0, 0)
	_, _, code := runCLI(t, "apply", "pr", "acme/widgets#42", "--from-item", fixture("routed_item.json"))
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if c.applyItem == nil || c.applyItem.Metadata.Kind != "changed" || c.applyItem.Metadata.Seq != 17 {
		t.Fatalf("item: %+v", c.applyItem)
	}
	if c.applyView == nil || c.applyView.ID != "acme/widgets#42" {
		t.Fatalf("apply must receive a freshly read view: %+v", c.applyView)
	}
}

func TestApplyWithoutItemPassesNil(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	c := stubSeams(t, 0, 0)
	if _, _, code := runCLI(t, "apply", "pr", "x"); code != 0 || c.applyItem != nil {
		t.Fatalf("code=%d item=%+v", code, c.applyItem)
	}
}

func TestApplyDoesNotBranchOnItemKind(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	c := stubSeams(t, 0, 0)
	dir := t.TempDir()
	// A routed item of a kind no rule knows must not change the CLI's behavior.
	p := filepath.Join(dir, "odd.json")
	writeFile(t, p, `{"id":"i","type":"pr.zzz","title":"t","metadata":{"entity_type":"pr","entity_id":"x","kind":"zzz","seq":1,"version":1}}`)
	if _, _, code := runCLI(t, "apply", "pr", "x", "--from-item", p); code != 0 || c.calls != 1 {
		t.Fatalf("code=%d calls=%d", code, c.calls)
	}
}

func TestApplyBadItemExits1(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	c := stubSeams(t, 0, 0)
	out, errOut, code := runCLI(t, "apply", "pr", "x", "--from-item", filepath.Join(t.TempDir(), "missing.json"))
	if code != 1 || out != "" || errOut == "" || c.calls != 0 {
		t.Fatalf("code=%d out=%q err=%q calls=%d", code, out, errOut, c.calls)
	}
}

func TestUsageErrors(t *testing.T) {
	stubSeams(t, 0, 0)
	for _, args := range [][]string{
		{"plan"},
		{"plan", "pr"},
		{"plan", "pr", "x", "extra"},
		{"plan", "bogus", "x"},
		{"apply", "pr"},
		{"nope"},
	} {
		_, errOut, code := runCLI(t, args...)
		if code != 1 || errOut == "" {
			t.Errorf("%v: code=%d err=%q", args, code, errOut)
		}
	}
}

func TestAcceptedTypes(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	stubSeams(t, 0, 0)
	for _, typ := range []string{"pr", "issue", "thread"} {
		if _, errOut, code := runCLI(t, "plan", typ, "x"); code != 0 {
			t.Errorf("%s: code=%d err=%q", typ, code, errOut)
		}
	}
}

func TestHelpDocumentsFlags(t *testing.T) {
	out, _, code := runCLI(t, "plan", "--help")
	if code != 0 || !strings.Contains(out, "--json") || !strings.Contains(out, "plan <type> <id>") {
		t.Fatalf("plan --help: code=%d out=%q", code, out)
	}
	out, _, code = runCLI(t, "apply", "--help")
	if code != 0 || !strings.Contains(out, "--from-item") || !strings.Contains(out, "apply <type> <id>") {
		t.Fatalf("apply --help: code=%d out=%q", code, out)
	}
}
