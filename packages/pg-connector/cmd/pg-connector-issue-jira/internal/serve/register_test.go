package serve_test

import (
	"slices"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/ops/explain"
	opsfeed "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/ops/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/ops/notice"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/ops/read"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/ops/status"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/serve"
)

func TestInstanceIsIssueAndExitCodesArePinned(t *testing.T) {
	if serve.Instance != "issue" {
		t.Errorf("Instance = %q, want issue", serve.Instance)
	}
	if serve.ExitOK != 0 || serve.ExitRefused != 5 {
		t.Errorf("exit codes = %d, %d, want 0 and 5", serve.ExitOK, serve.ExitRefused)
	}
}

// The drift guard: the ops registered under Instance are exactly the union of
// the op names the sibling packages declare. An op added to a sibling package and
// not registered (or the reverse) fails here.
func TestTheRegisteredOpTableEqualsTheUnionOfTheSiblingPackagesOps(t *testing.T) {
	var union []string
	union = append(union, read.Ops...)
	union = append(union, opsfeed.Ops...)
	union = append(union, notice.Ops...)
	union = append(union, status.Op)
	union = append(union, explain.Ops...)
	slices.Sort(union)
	if dup := slices.Compact(slices.Clone(union)); len(dup) != len(union) {
		t.Fatalf("the sibling packages declare an op twice: %v", union)
	}
	if got := serve.OpNames(); !slices.Equal(sortedCopy(got), union) {
		t.Errorf("serve.OpNames() = %v, want the union %v", sortedCopy(got), union)
	}

	r := newRig(t, fakePjiraOK, "", nil)
	d := r.start(serve.Hooks{})
	rt := d.waitReady()
	if got := sortedCopy(rt.Ops(serve.Instance)); !slices.Equal(got, union) {
		t.Errorf("registered ops = %v, want the union %v", got, union)
	}
	// Every op is under the one instance; the name of a former instance is not
	// served.
	if other := rt.Ops("pr"); len(other) != 0 {
		t.Errorf("an instance other than %q has ops: %v", serve.Instance, other)
	}
	if code := d.stop(); code != serve.ExitOK {
		t.Errorf("exit = %d; stderr: %s", code, d.stderr.String())
	}
}

func TestEveryRegisteredOpAnswersAndNoOtherOpIsKnown(t *testing.T) {
	r := newRig(t, fakePjiraOK, "", nil)
	d := r.start(serve.Hooks{})
	d.waitReady()
	for _, op := range serve.OpNames() {
		resp := r.mustCall(serve.Instance, op, map[string]any{})
		if resp.Error != nil && resp.Error.Code == "unknown_op" {
			t.Errorf("%s must be registered, got unknown_op", op)
		}
	}
	// create, comment, transition, update and close are client-mode writes: the
	// daemon has no such op.
	for _, op := range []string{"create", "comment", "transition", "update", "close"} {
		resp := r.mustCall(serve.Instance, op, map[string]any{})
		if resp.Error == nil || resp.Error.Code != "unknown_op" {
			t.Errorf("%s = %+v, want unknown_op", op, resp.Error)
		}
	}
	if code := d.stop(); code != serve.ExitOK {
		t.Errorf("exit = %d; stderr: %s", code, d.stderr.String())
	}
}

func sortedCopy(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}
