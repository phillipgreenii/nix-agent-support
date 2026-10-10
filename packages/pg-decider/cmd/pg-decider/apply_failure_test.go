package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/config"
	"github.com/phillipgreenii/pg-decider/internal/exitcode"
	"github.com/phillipgreenii/pg-decider/internal/view"
)

// These tests exercise the failure and metrics hooks through the real applyFn,
// with per-command answers (the shared recordWrites answers every exec alike).

const anchorKey = "pr:acme/widgets#42:anchor"

// scriptedWrites swaps decideFn for one returning actions and applyCommand for
// a factory that records every exec and answers through respond.
func scriptedWrites(t *testing.T, decide func(v *view.View) []action.Action, respond func(name, args string) (stdout string, exit string)) *[]execRec {
	t.Helper()
	var recs []execRec
	origDecide, origCmd := decideFn, applyCommand
	t.Cleanup(func() { decideFn, applyCommand = origDecide, origCmd })
	decideFn = func(v *view.View, _ string, _ *config.Config) action.PlanResult {
		return action.PlanResult{Actions: decide(v), Skipped: []action.Skip{}}
	}
	applyCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		a := strings.Join(args, " ")
		recs = append(recs, execRec{name, a})
		out, code := respond(name, a)
		cs := append([]string{"-test.run=^TestHelperProcess$", "--", name}, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], cs...)
		cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "GO_HELPER_STDOUT="+out, "GO_HELPER_EXIT="+code)
		return cmd
	}
	return &recs
}

func hasAnchorLink(v *view.View) bool {
	for _, l := range v.Links {
		if l.Metadata["dedup_key"] == anchorKey {
			return true
		}
	}
	return false
}

func anchorCreate() action.Action {
	return action.Action{
		Op: action.OpCreate, Kind: "anchor", Rule: "anchor.create",
		Fields: action.Fields{Title: "acme/widgets#42: Add retry to client", IssueType: "epic", Metadata: map[string]string{"dedup_key": anchorKey}},
	}
}

func TestADeciderWriteWhoseRefreshFailsExitsZeroIsNotUndoneAndTheNextRunWritesNothing(t *testing.T) {
	// Run 1: the anchor is missing, the create succeeds, the pg-desk issue
	// refresh double fails.
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	t.Setenv("PG_DECIDER_CONFIG", "")
	recs := scriptedWrites(t,
		func(v *view.View) []action.Action {
			if hasAnchorLink(v) {
				return nil
			}
			return []action.Action{anchorCreate()}
		},
		func(name, args string) (string, string) {
			switch {
			case name == "pg-desk" && strings.HasPrefix(args, "issue refresh"):
				return "", "1"
			case strings.HasPrefix(args, "issue list"):
				return `{"entities":[]}`, "0"
			case strings.HasPrefix(args, "issue create"):
				return `{"result":{"id":"wb-new"}}`, "0"
			}
			return `{"result":{}}`, "0"
		})
	out, errOut, code := runCLI(t, "apply", "pr", "acme/widgets#42", "--from-item", fixture("routed_item.json"))
	if code != exitcode.OK {
		t.Fatalf("a failed refresh must not fail the run: exit %d out %q err %q", code, out, errOut)
	}
	var created, refreshed, undone int
	for _, r := range *recs {
		switch {
		case strings.HasPrefix(r.args, "issue create"):
			created++
		case strings.HasPrefix(r.args, "issue refresh"):
			refreshed++
		case strings.HasPrefix(r.args, "issue close"), strings.HasPrefix(r.args, "issue delete"):
			undone++
		}
		if strings.Contains(r.args, "failures.") || strings.Contains(r.args, "escalated.") {
			t.Fatalf("a failed refresh is not a rule failure: %+v", r)
		}
	}
	if created != 1 || refreshed != 1 || undone != 0 {
		t.Fatalf("created %d refreshed %d undone %d: %+v", created, refreshed, undone, *recs)
	}
	if !strings.Contains(errOut, "refresh") {
		t.Fatalf("the refresh failure must be logged: %q", errOut)
	}

	// Run 2: the next hydration caught up, so the view links the anchor.
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_with_anchor.json"))
	*recs = nil
	if _, errOut, code := runCLI(t, "apply", "pr", "acme/widgets#42", "--from-item", fixture("routed_item.json")); code != exitcode.OK || len(*recs) != 0 {
		t.Fatalf("the re-run on the updated view must write nothing: exit %d err %q execs %+v", code, errOut, *recs)
	}
}

func TestApplyInstallsTheFailureAndMetricsHooksAndEscalatesWithTheExactRunCounters(t *testing.T) {
	// The view records two consecutive failures of r.fail at seq 3; the
	// routed item is seq 17, so this failing run is the third and escalates.
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_failing.json"))
	t.Setenv("PG_DECIDER_CONFIG", "")
	two, three := "wb-fail", "wb-dup"
	recs := scriptedWrites(t,
		func(*view.View) []action.Action {
			return []action.Action{
				{Op: action.OpCreate, Kind: "review-pr", Rule: "r.ok", Fields: action.Fields{Title: "ok", Metadata: map[string]string{"dedup_key": "pr:acme/widgets#42:review-pr:aaa"}}},
				{Op: action.OpCreate, Kind: "review-pr", Rule: "r.dup", Fields: action.Fields{Title: "dup", Metadata: map[string]string{"dedup_key": "pr:acme/widgets#42:review-pr:bbb"}}},
				{Op: action.OpUpdate, Rule: "r.fail", Target: &two, Fields: action.Fields{Title: "x"}},
				{Op: action.OpClose, Rule: "r.dup", Target: &three},
			}
		},
		func(name, args string) (string, string) {
			switch {
			case strings.HasPrefix(args, "issue list"):
				return `{"entities":[{"id":"wb-existing","metadata":{"dedup_key":"pr:acme/widgets#42:review-pr:bbb"}}]}`, "0"
			case strings.HasPrefix(args, "issue create --title ok"):
				return `{"result":{"id":"wb-ok"}}`, "0"
			case strings.HasPrefix(args, "issue create"):
				return `{"result":{"id":"wb-esc"}}`, "0"
			case strings.HasPrefix(args, "issue update wb-fail"):
				return `{"error":{"code":"unavailable","message":"tracker down"}}`, "1"
			}
			return `{"result":{}}`, "0"
		})
	_, errOut, code := runCLI(t, "apply", "pr", "acme/widgets#42", "--from-item", fixture("routed_item.json"))
	if code != exitcode.Partial {
		t.Fatalf("exit %d err %q", code, errOut)
	}

	var escalations []string
	var annotations []string
	for _, r := range *recs {
		if strings.HasPrefix(r.args, "issue create --title pg-decider: rule r.fail") {
			escalations = append(escalations, r.args)
		}
		if r.name == "pg-desk" && strings.HasPrefix(r.args, "pr annotate") {
			annotations = append(annotations, r.args)
		}
	}
	if len(escalations) != 1 || !strings.Contains(escalations[0], "--labels human") || !strings.Contains(escalations[0], "tracker down") {
		t.Fatalf("escalations: %q (all %+v)", escalations, *recs)
	}
	wantAnn := []string{
		"pr annotate acme/widgets#42 --key decider.pr-decider.failures.r.fail --value 3 --origin decider:pr-decider --actor pg-decider",
		"pr annotate acme/widgets#42 --key decider.pr-decider.failure_seq.r.fail --value 17 --origin decider:pr-decider --actor pg-decider",
		"pr annotate acme/widgets#42 --key decider.pr-decider.escalated.r.fail --value 1 --origin decider:pr-decider --actor pg-decider",
	}
	if strings.Join(annotations, "\n") != strings.Join(wantAnn, "\n") {
		t.Fatalf("annotations:\n got %q\nwant %q", annotations, wantAnn)
	}

	// r.dup: one deduped create and one applied close; the metrics line carries
	// the exact per-rule counters and the one escalation.
	wantLine := `{"contract":"pg-decider.run-counters/v1","type":"pr","id":"acme/widgets#42","rules":{` +
		`"r.dup":{"planned":2,"applied":1,"deduped":1,"failed":0,"skipped":0},` +
		`"r.fail":{"planned":1,"applied":0,"deduped":0,"failed":1,"skipped":0},` +
		`"r.ok":{"planned":1,"applied":1,"deduped":0,"failed":0,"skipped":0}},` +
		`"escalations":1,"seq":17,"from_item":"item-1"}`
	var lines []string
	for _, l := range strings.Split(errOut, "\n") {
		if strings.Contains(l, `"contract":"pg-decider.run-counters/v1"`) {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 || lines[0] != wantLine {
		t.Fatalf("counter lines %q want %q\nstderr %q", lines, wantLine, errOut)
	}
}

func TestAnIdleRunStillEmitsOneCounterLine(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	t.Setenv("PG_DECIDER_CONFIG", "")
	recs := scriptedWrites(t, func(*view.View) []action.Action { return nil }, func(string, string) (string, string) { return `{"result":{}}`, "0" })
	_, errOut, code := runCLI(t, "apply", "pr", "acme/widgets#42")
	want := `{"contract":"pg-decider.run-counters/v1","type":"pr","id":"acme/widgets#42","rules":{},"escalations":0}` + "\n"
	if code != 0 || errOut != want || len(*recs) != 0 {
		t.Fatalf("exit %d err %q execs %+v", code, errOut, *recs)
	}
}

// A focus hold the live re-read abandons is the apply outcome skipped-stale: it
// exits 0, writes no update and no audit comment, repairs the stale store row
// with an issue refresh, and the run counters line counts it under skipped so
// that planned = applied + deduped + failed + skipped still holds.
func TestApplyAbandonedFocusHoldIsSkippedStaleExitsZeroAndWritesNoComment(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	t.Setenv("PG_DECIDER_CONFIG", "")
	target := "bd-focus-1"
	recs := scriptedWrites(t,
		func(*view.View) []action.Action {
			return []action.Action{{
				Op: action.OpUpdate, Kind: "focus-item", Target: &target, Rule: "focus.item",
				Fields: action.Fields{Status: "deferred", Metadata: map[string]string{"focus_hold": "struck"}},
				Facts:  map[string]any{"transition": "hold"},
			}}
		},
		func(name, args string) (string, string) {
			if strings.HasPrefix(args, "issue show bd-focus-1 --fresh") {
				return `{"result":{"id":"bd-focus-1","state":"in_progress","assignee":"worker-1","served_from":"origin","stale":false}}`, "0"
			}
			return `{"result":{}}`, "0"
		})
	_, errOut, code := runCLI(t, "apply", "pr", "acme/widgets#42")
	if code != exitcode.OK {
		t.Fatalf("a skipped-stale outcome is not a failure: exit %d, stderr %q", code, errOut)
	}
	var updates, comments, refreshes int
	for _, r := range *recs {
		switch {
		case strings.HasPrefix(r.args, "issue update"):
			updates++
		case strings.HasPrefix(r.args, "issue comment"):
			comments++
		case r.name == "pg-desk" && r.args == "issue refresh bd-focus-1":
			refreshes++
		}
	}
	if updates != 0 || comments != 0 || refreshes != 1 {
		t.Fatalf("updates %d comments %d refreshes %d: %+v", updates, comments, refreshes, *recs)
	}
	if !strings.Contains(errOut, "skipped-stale") || !strings.Contains(errOut, `"skipped":1`) || !strings.Contains(errOut, `"planned":1`) {
		t.Fatalf("stderr must name the outcome and count it under skipped: %q", errOut)
	}
}
