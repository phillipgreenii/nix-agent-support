package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/config"
	"github.com/phillipgreenii/pg-decider/internal/exitcode"
	"github.com/phillipgreenii/pg-decider/internal/view"
)

// These tests exercise the real applyFn: the view comes from the pg-desk
// double (view.ExecCommand, see testmain_test.go) and the writes go to a
// recording command factory standing in for pg-connector and pg-desk.

type execRec struct{ name, args string }

// recordWrites swaps decideFn for one returning actions and applyCommand for a
// factory that records every exec and answers through the helper process.
func recordWrites(t *testing.T, actions []action.Action, stdout string, exit string) *[]execRec {
	t.Helper()
	var recs []execRec
	origDecide, origCmd := decideFn, applyCommand
	t.Cleanup(func() { decideFn, applyCommand = origDecide, origCmd })
	decideFn = func(_ *view.View, _ string, _ *config.Config) action.PlanResult {
		return action.PlanResult{Actions: actions, Skipped: []action.Skip{}}
	}
	applyCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		recs = append(recs, execRec{name, strings.Join(args, " ")})
		cs := append([]string{"-test.run=^TestHelperProcess$", "--", name}, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], cs...)
		cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "GO_HELPER_STDOUT="+stdout, "GO_HELPER_EXIT="+exit)
		return cmd
	}
	return &recs
}

func str(s string) *string { return &s }

// withoutComments drops the audit hook's timestamped comment execs, which have
// their own tests.
func withoutComments(in []execRec) []execRec {
	var out []execRec
	for _, r := range in {
		if !strings.HasPrefix(r.args, "issue comment ") {
			out = append(out, r)
		}
	}
	return out
}

func last(in []execRec) execRec { return in[len(in)-1] }

func TestApplyAppliesTheDecidersActionsAndExits0(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	recs := recordWrites(t, []action.Action{{Op: action.OpClose, Rule: "r.close", Target: str("wb-1")}}, `{"result":{}}`, "0")
	t.Setenv("PG_DECIDER_CONFIG", "")
	out, errOut, code := runCLI(t, "apply", "pr", "acme/widgets#42")
	if code != exitcode.OK {
		t.Fatalf("exit %d out %q err %q", code, out, errOut)
	}
	want := []execRec{
		{"pg-connector", "issue close wb-1 --reason r.close: close work item"},
		{"pg-desk", "issue refresh wb-1"},
	}
	if !reflect.DeepEqual(withoutComments(*recs), want) {
		t.Fatalf("execs: %+v", *recs)
	}
}

func TestApplyWithNoActionsWritesNothingAndExits0(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	recs := recordWrites(t, nil, `{"result":{}}`, "0")
	if _, errOut, code := runCLI(t, "apply", "pr", "acme/widgets#42"); code != 0 || len(*recs) != 0 {
		t.Fatalf("code %d err %q execs %+v", code, errOut, *recs)
	}
}

func TestApplyExits2WhenAnActionFails(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	recordWrites(t, []action.Action{{Op: action.OpClose, Rule: "r", Target: str("wb-1")}}, `{"error":{"code":"unavailable","message":"x"}}`, "1")
	_, errOut, code := runCLI(t, "apply", "pr", "acme/widgets#42")
	if code != exitcode.Partial || !strings.Contains(errOut, "wb-1") {
		t.Fatalf("code %d err %q", code, errOut)
	}
}

func TestApplyFromItemReadsTheViewExactlyOnceAndNeverDecidesFromTheKind(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	reads := 0
	inner := view.ExecCommand
	view.ExecCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		reads++
		return inner(ctx, name, args...)
	}
	decides := 0
	recordWrites(t, nil, `{"result":{}}`, "0")
	innerDecide := decideFn
	decideFn = func(v *view.View, typ string, cfg *config.Config) action.PlanResult {
		decides++
		return innerDecide(v, typ, cfg)
	}
	_, errOut, code := runCLI(t, "apply", "pr", "acme/widgets#42", "--from-item", fixture("routed_item.json"))
	if code != 0 || decides != 1 || reads != 1 {
		t.Fatalf("code %d decides %d view reads %d err %q", code, decides, reads, errOut)
	}
}

func TestApplyBadConfigExits1BeforeAnyWrite(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	recs := recordWrites(t, []action.Action{{Op: action.OpClose, Rule: "r", Target: str("wb-1")}}, `{"result":{}}`, "0")
	p := filepath.Join(t.TempDir(), "c.json")
	writeFile(t, p, `{"escalate_after":0}`)
	t.Setenv("PG_DECIDER_CONFIG", p)
	_, errOut, code := runCLI(t, "apply", "pr", "x")
	if code != exitcode.Failure || !strings.Contains(errOut, "escalate_after") || len(*recs) != 0 {
		t.Fatalf("code %d err %q execs %+v", code, errOut, *recs)
	}
}

func TestApplyWithoutADeciderForTheTypeExits1(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	recs := recordWrites(t, nil, `{"result":{}}`, "0")
	_, errOut, code := runCLI(t, "apply", "thread", "x")
	if code != exitcode.Failure || !strings.Contains(errOut, "no decider") || len(*recs) != 0 {
		t.Fatalf("code %d err %q", code, errOut)
	}
}

func TestApplyUsesTheConfigBackendAndBeadsDir(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	recs := recordWrites(t, []action.Action{{Op: action.OpClose, Rule: "r", Target: str("wb-1"), Fields: action.Fields{Description: "done"}}}, `{"result":{}}`, "0")
	p := filepath.Join(t.TempDir(), "c.json")
	writeFile(t, p, `{"agent_tracker_backend":"trk","beads_dir":"/tmp/beads","actor":"bot"}`)
	t.Setenv("PG_DECIDER_CONFIG", p)
	if _, errOut, code := runCLI(t, "apply", "pr", "x"); code != 0 {
		t.Fatalf("code %d err %q", code, errOut)
	}
	if (*recs)[0].args != "issue close wb-1 --reason r: done --backend trk" || !strings.HasSuffix(last(*recs).args, " --backend trk") || !strings.HasPrefix(last(*recs).args, "issue comment wb-1 --body ") {
		t.Fatalf("execs: %+v", *recs)
	}
}

func TestApplyInstallsTheAuditHookAndCommentsOncePerAppliedExternalAction(t *testing.T) {
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"))
	recs := recordWrites(t, []action.Action{
		{Op: action.OpClose, Rule: "r.close", Target: str("wb-1")},
		{Op: action.OpAnnotate, Rule: "r.note", Target: str("k"), Fields: action.Fields{Value: str("v")}},
	}, `{"result":{}}`, "0")
	if _, errOut, code := runCLI(t, "apply", "pr", "acme/widgets#42", "--from-item", fixture("routed_item.json")); code != 0 {
		t.Fatalf("code %d err %q", code, errOut)
	}
	var comments []execRec
	for _, r := range *recs {
		if strings.HasPrefix(r.args, "issue comment ") {
			comments = append(comments, r)
		}
	}
	if len(comments) != 1 || comments[0].name != "pg-connector" || !strings.HasPrefix(comments[0].args, "issue comment wb-1 --body ") || !strings.Contains(comments[0].args, "rule: r.close") {
		t.Fatalf("comments: %+v (all %+v)", comments, *recs)
	}
}
