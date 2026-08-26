package gate

import (
	"context"
	"errors"
	"testing"

	"github.com/phillipgreenii/pb/internal/bd"
	"github.com/phillipgreenii/pb/internal/patchid"
	"github.com/phillipgreenii/pb/internal/pn"
	"github.com/phillipgreenii/pb/internal/run"
)

func attachDeps(f *run.FakeRunner) CreateDeps {
	return CreateDeps{PN: pn.Client{R: f}, BD: bd.Client{R: f}, PatchID: patchid.Client{R: f}, R: f, Discover: stubDiscoverWS}
}

// scriptAttachPreamble scripts Attach's own preamble: info, impl-DB resolve,
// child create, and the deferred-first ready check (child absent).
func scriptAttachPreamble(f *run.FakeRunner) {
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: createInfoJSON}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "show", "pg2-impl", "--json"}, run.Result{Stdout: "{}"}, nil)
	f.AddResponse("bd", []string{
		"-C", "/ws", "create", "verify thing after apply (pg2-impl)",
		"--defer", "2126-01-01", "--deps", "discovered-from:pg2-impl",
		"--actor", "sess-1", "--json",
	}, run.Result{Stdout: `{"data":{"id":"pg2-child"}}`}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "ready", "--json", "-n", "0"},
		run.Result{Stdout: `{"data":[{"id":"pg2-other"}]}`}, nil)
}

// scriptOneGate scripts one inner gate.Create for repo-a at sha1 blocking the child.
func scriptOneGate(f *run.FakeRunner, gateID string) {
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: createInfoJSON}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "show", "pg2-child", "--json"}, run.Result{Stdout: "{}"}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "show", "sha1"}, run.Result{Stdout: "diff..."}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "patch-id", "--stable"}, run.Result{Stdout: "pid1 sha1\n"}, nil)
	f.AddResponse("bd", []string{
		"-C", "/ws", "gate", "create", "--type=pn:applied", "--blocks", "pg2-child",
		"--await-id", "home:repo-a:pid1", "--reason", "post-deploy verify for pg2-impl", "--json",
	}, run.Result{Stdout: `{"data":{"id":"` + gateID + `"}}`}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "update", gateID, "--set-metadata", "applied_baseline=base1"},
		run.Result{}, nil)
}

func attachParams() AttachParams {
	return AttachParams{
		WorkspaceDir: "/ws", ImplID: "pg2-impl",
		Title: "verify thing after apply (pg2-impl)",
		Gates: []GateSpec{{Repo: "repo-a", Commit: "sha1"}},
		Actor: "sess-1", Reason: "post-deploy verify for pg2-impl",
	}
}

func TestAttach_happyPathDeferredFirst(t *testing.T) {
	f := run.NewFakeRunner()
	scriptAttachPreamble(f)
	scriptOneGate(f, "g-1")
	// un-defer, then re-confirm the GATES (not the defer) hold the child
	f.AddResponse("bd", []string{"-C", "/ws", "update", "pg2-child", "--defer", "", "--actor", "sess-1"},
		run.Result{}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "ready", "--json", "-n", "0"},
		run.Result{Stdout: `{"data":[]}`}, nil)
	f.AddResponse("bd", []string{
		"-C", "/ws", "comment", "pg2-impl",
		"post-deploy verification gated as pg2-child (pn:applied).", "--actor", "sess-1",
	},
		run.Result{}, nil)

	out, err := Attach(context.Background(), attachDeps(f), attachParams())
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if out.ChildID != "pg2-child" || len(out.Gates) != 1 || out.CommentFailed {
		t.Errorf("out = %+v", out)
	}
}

func TestAttach_gateCreateFailureLeavesChildDeferred(t *testing.T) {
	f := run.NewFakeRunner()
	scriptAttachPreamble(f)
	// inner Create fails at pn info (unscripted call → FakeRunner error)
	out, err := Attach(context.Background(), attachDeps(f), attachParams())
	if !errors.Is(err, ErrGatingIncomplete) {
		t.Fatalf("err = %v, want ErrGatingIncomplete", err)
	}
	if out.ChildID != "pg2-child" {
		t.Errorf("partial result must still name the child: %+v", out)
	}
	for _, c := range f.Calls() {
		if c.Name == "bd" && len(c.Args) >= 6 && c.Args[2] == "update" && c.Args[4] == "--defer" && c.Args[5] == "" {
			t.Fatal("child must NOT be un-deferred after a gate failure")
		}
	}
}

func TestAttach_childInReadyRepairsOnceThenFails(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: createInfoJSON}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "show", "pg2-impl", "--json"}, run.Result{Stdout: "{}"}, nil)
	f.AddResponse("bd", []string{
		"-C", "/ws", "create", "verify thing after apply (pg2-impl)",
		"--defer", "2126-01-01", "--deps", "discovered-from:pg2-impl",
		"--actor", "sess-1", "--json",
	}, run.Result{Stdout: `{"data":{"id":"pg2-child"}}`}, nil)
	// child present in ready → repair (re-apply defer) → still present → fail
	f.AddResponse("bd", []string{"-C", "/ws", "ready", "--json", "-n", "0"},
		run.Result{Stdout: `{"data":[{"id":"pg2-child"}]}`}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "update", "pg2-child", "--defer", "2126-01-01", "--actor", "sess-1"},
		run.Result{}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "ready", "--json", "-n", "0"},
		run.Result{Stdout: `{"data":[{"id":"pg2-child"}]}`}, nil)

	_, err := Attach(context.Background(), attachDeps(f), attachParams())
	if !errors.Is(err, ErrChildMayBeWorkable) {
		t.Fatalf("err = %v, want ErrChildMayBeWorkable", err)
	}
}

func TestAttach_zeroGatesRejected(t *testing.T) {
	f := run.NewFakeRunner()
	p := attachParams()
	p.Gates = nil
	if _, err := Attach(context.Background(), attachDeps(f), p); err == nil {
		t.Fatal("expected error for zero gates (would un-defer a completely ungated child)")
	}
	if len(f.Calls()) != 0 {
		t.Errorf("no external call may run for an invalid invocation: %v", f.Calls())
	}
}

func TestAttach_unknownGateRepoFailsBeforeChildCreate(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: createInfoJSON}, nil)
	p := attachParams()
	p.Gates = []GateSpec{{Repo: "typo", Commit: "sha1"}}
	_, err := Attach(context.Background(), attachDeps(f), p)
	if err == nil || errors.Is(err, ErrGatingIncomplete) || errors.Is(err, ErrChildMayBeWorkable) {
		t.Fatalf("want a plain pre-creation error (exit 1, an invocation typo), got %v", err)
	}
	for _, c := range f.Calls() {
		if c.Name == "bd" && len(c.Args) >= 3 && c.Args[2] == "create" {
			t.Fatal("child must not be created for an invalid --gate repo")
		}
	}
}

func TestAttach_commentFailureIsNotFatal(t *testing.T) {
	f := run.NewFakeRunner()
	scriptAttachPreamble(f)
	scriptOneGate(f, "g-1")
	f.AddResponse("bd", []string{"-C", "/ws", "update", "pg2-child", "--defer", "", "--actor", "sess-1"},
		run.Result{}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "ready", "--json", "-n", "0"},
		run.Result{Stdout: `{"data":[]}`}, nil)
	// comment unscripted → fails; gating is already complete and safe
	out, err := Attach(context.Background(), attachDeps(f), attachParams())
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if !out.CommentFailed {
		t.Error("CommentFailed must be reported")
	}
}
