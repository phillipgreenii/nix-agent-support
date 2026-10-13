package gate

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/phillipgreenii/pb/internal/bd"
	"github.com/phillipgreenii/pb/internal/patchid"
	"github.com/phillipgreenii/pb/internal/pn"
	"github.com/phillipgreenii/pb/internal/run"
)

func attachDeps(f *run.FakeRunner) CreateDeps {
	return CreateDeps{PN: pn.Client{R: f}, BD: bd.Client{R: f}, PatchID: patchid.Client{R: f}, R: f, Discover: stubDiscoverWS}
}

// implShowJSON is the implementation bead as bd show reports it: its repo and
// project labels (agent-support, pb) alongside workflow-state ones.
const implShowJSON = `{"data":[{"id":"pg2-impl","title":"the implementation",
	"labels":["pb","human","agent-support","escalated","has-acceptance-criteria"]}],"schema_version":1}`

var implShowArgv = []string{"-C", "/ws", "show", "pg2-impl", "--json"}

// attachCreateArgv is the bd create call Attach makes for p against implShowJSON:
// p's description and acceptance when given, the derived ones otherwise.
func attachCreateArgv(p AttachParams) []string {
	desc, acc := p.Description, p.Acceptance
	if strings.TrimSpace(desc) == "" {
		desc = childDescription(bd.Bead{Title: "the implementation"}, p)
	}
	if strings.TrimSpace(acc) == "" {
		acc = childAcceptance(p)
	}
	return []string{
		"-C", "/ws", "create", p.Title,
		"--defer", "2126-01-01", "--deps", "discovered-from:pg2-impl",
		"--labels", "agent-support,has-acceptance-criteria,pb",
		"--description", desc,
		"--acceptance", acc,
		"--actor", "sess-1", "--json",
	}
}

// scriptAttachPreamble scripts Attach's own preamble: info, impl-DB resolve,
// the impl bead read, child create, and the deferred-first ready check (child
// absent).
func scriptAttachPreamble(f *run.FakeRunner) { scriptAttachPreambleFor(f, attachParams()) }

func scriptAttachPreambleFor(f *run.FakeRunner, p AttachParams) {
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: createInfoJSON}, nil)
	f.AddResponse("bd", implShowArgv, run.Result{Stdout: "{}"}, nil)         // resolveBeadDB: HasBead
	f.AddResponse("bd", implShowArgv, run.Result{Stdout: implShowJSON}, nil) // ShowBead
	f.AddResponse("bd", attachCreateArgv(p), run.Result{Stdout: `{"data":{"id":"pg2-child"}}`}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "ready", "--json", "-n", "0"},
		run.Result{Stdout: `{"data":[{"id":"pg2-other"}]}`}, nil)
}

// scriptOneGate scripts one inner gate.Create for repo-a at sha1 blocking the child.
func scriptOneGate(f *run.FakeRunner, gateID string) {
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: createInfoJSON}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "show", "pg2-child", "--json"}, run.Result{Stdout: "{}"}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "show", "--no-ext-diff", "--no-textconv", "--no-color", "sha1"}, run.Result{Stdout: "diff..."}, nil)
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
	f.AddResponse("bd", implShowArgv, run.Result{Stdout: "{}"}, nil)
	f.AddResponse("bd", implShowArgv, run.Result{Stdout: implShowJSON}, nil)
	f.AddResponse("bd", attachCreateArgv(attachParams()), run.Result{Stdout: `{"data":{"id":"pg2-child"}}`}, nil)
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

func TestAttach_titleOver500RejectedBeforeAnyCall(t *testing.T) {
	f := run.NewFakeRunner()
	p := attachParams()
	p.Title = strings.Repeat("t", 501)
	_, err := Attach(context.Background(), attachDeps(f), p)
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v, want a message naming the 500-character limit", err)
	}
	if errors.Is(err, ErrGatingIncomplete) || errors.Is(err, ErrChildMayBeWorkable) {
		t.Errorf("a title typo is a plain exit-1 error, got %v", err)
	}
	if len(f.Calls()) != 0 {
		t.Errorf("no pn/bd/git call may run (nothing may be created): %v", f.Calls())
	}
}

func TestAttach_title500Accepted(t *testing.T) {
	f := run.NewFakeRunner()
	long := strings.Repeat("t", 500)
	p := attachParams()
	p.Title = long
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: createInfoJSON}, nil)
	f.AddResponse("bd", implShowArgv, run.Result{Stdout: "{}"}, nil)
	f.AddResponse("bd", implShowArgv, run.Result{Stdout: implShowJSON}, nil)
	f.AddResponse("bd", attachCreateArgv(p), run.Result{Stdout: `{"data":{"id":"pg2-child"}}`}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "ready", "--json", "-n", "0"}, run.Result{Stdout: `{"data":[]}`}, nil)
	// The inner gate step fails on the unscripted calls; the point is that the
	// 500-char title got PAST validation and reached bd create.
	_, err := Attach(context.Background(), attachDeps(f), p)
	if err == nil || !errors.Is(err, ErrGatingIncomplete) {
		t.Fatalf("err = %v, want to reach the gating step", err)
	}
}

func TestAttach_createFailureCarriesBdStdoutMessage(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: createInfoJSON}, nil)
	f.AddResponse("bd", implShowArgv, run.Result{Stdout: "{}"}, nil)
	f.AddResponse("bd", implShowArgv, run.Result{Stdout: implShowJSON}, nil)
	f.AddResponse("bd", attachCreateArgv(attachParams()),
		run.Result{Stdout: `{"error":"validation failed"}`, ExitCode: 1}, errors.New("bd create: exit 1: "))
	_, err := Attach(context.Background(), attachDeps(f), attachParams())
	if err == nil || !strings.Contains(err.Error(), "validation failed") {
		t.Fatalf("err = %v, want bd's message", err)
	}
}

// createCallArgs returns the args of the one recorded bd create call.
func createCallArgs(t *testing.T, f *run.FakeRunner) []string {
	t.Helper()
	for _, c := range f.Calls() {
		if c.Name == "bd" && len(c.Args) > 3 && c.Args[2] == "create" {
			return c.Args
		}
	}
	t.Fatal("no bd create call was made")
	return nil
}

// flagValue returns the value following flag in args.
func flagValue(t *testing.T, args []string, flag string) string {
	t.Helper()
	i := slices.Index(args, flag)
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("%s is missing from %q", flag, args)
	}
	return args[i+1]
}

// The child is born with the implementation bead's repo and project labels, no
// workflow-state label, and has-acceptance-criteria so a drain worker may claim
// it once the gates resolve. The attach then fails at the (unscripted) gating
// step: only what bd create received matters here.
func TestAttach_childBornWithScopeLabelsAndNoWorkflowState(t *testing.T) {
	f := run.NewFakeRunner()
	scriptAttachPreamble(f)
	if _, err := Attach(context.Background(), attachDeps(f), attachParams()); !errors.Is(err, ErrGatingIncomplete) {
		t.Fatalf("err = %v, want to reach the gating step", err)
	}
	labels := strings.Split(flagValue(t, createCallArgs(t, f), "--labels"), ",")
	if want := []string{"agent-support", "has-acceptance-criteria", "pb"}; !slices.Equal(labels, want) {
		t.Errorf("labels = %v, want %v (implementation labels were pb, human, agent-support, escalated)", labels, want)
	}
}

func TestAttach_childBornWithDerivedDescriptionAndAcceptance(t *testing.T) {
	f := run.NewFakeRunner()
	scriptAttachPreamble(f)
	if _, err := Attach(context.Background(), attachDeps(f), attachParams()); !errors.Is(err, ErrGatingIncomplete) {
		t.Fatalf("err = %v, want to reach the gating step", err)
	}
	args := createCallArgs(t, f)
	desc := flagValue(t, args, "--description")
	for _, want := range []string{"pg2-impl", "the implementation", "repo-a=sha1", "bd show pg2-impl"} {
		if !strings.Contains(desc, want) {
			t.Errorf("derived description lacks %q: %q", want, desc)
		}
	}
	acc := flagValue(t, args, "--acceptance")
	if !strings.HasPrefix(acc, "- ") || !strings.Contains(acc, attachParams().Title) {
		t.Errorf("derived acceptance = %q, want a checklist naming the title", acc)
	}
}

func TestAttach_givenDescriptionAndAcceptanceAreUsedVerbatim(t *testing.T) {
	p := attachParams()
	p.Description = "## Checks\n\n- run `tldr wsplan` and compare it with a sibling page\n"
	p.Acceptance = "- tldr wsplan renders the new page\n- the sibling page is unchanged"
	f := run.NewFakeRunner()
	scriptAttachPreambleFor(f, p)
	if _, err := Attach(context.Background(), attachDeps(f), p); !errors.Is(err, ErrGatingIncomplete) {
		t.Fatalf("err = %v, want to reach the gating step", err)
	}
	args := createCallArgs(t, f)
	if got := flagValue(t, args, "--description"); got != p.Description {
		t.Errorf("description = %q, want the caller's %q", got, p.Description)
	}
	if got := flagValue(t, args, "--acceptance"); got != p.Acceptance {
		t.Errorf("acceptance = %q, want the caller's %q", got, p.Acceptance)
	}
}

// A blank description or acceptance reads as "not given", never as an empty
// field: the child must always be claimable.
func TestAttach_blankDescriptionAndAcceptanceAreDerived(t *testing.T) {
	p := attachParams()
	p.Description, p.Acceptance = " \n", "  "
	f := run.NewFakeRunner()
	scriptAttachPreambleFor(f, p)
	if _, err := Attach(context.Background(), attachDeps(f), p); !errors.Is(err, ErrGatingIncomplete) {
		t.Fatalf("err = %v, want to reach the gating step", err)
	}
	args := createCallArgs(t, f)
	if !strings.Contains(flagValue(t, args, "--description"), "pg2-impl") || !strings.HasPrefix(flagValue(t, args, "--acceptance"), "- ") {
		t.Errorf("blank input was not replaced by the derived text: %q", args)
	}
}

// An unreadable implementation bead is an invocation mistake like a bad --gate
// repo: exit 1 with nothing created.
func TestAttach_implBeadUnreadableFailsBeforeChildCreate(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: createInfoJSON}, nil)
	f.AddResponse("bd", implShowArgv, run.Result{Stdout: "{}"}, nil) // HasBead; ShowBead's call is unscripted
	_, err := Attach(context.Background(), attachDeps(f), attachParams())
	if err == nil || errors.Is(err, ErrGatingIncomplete) || errors.Is(err, ErrChildMayBeWorkable) {
		t.Fatalf("want a plain pre-creation error (exit 1), got %v", err)
	}
	for _, c := range f.Calls() {
		if c.Name == "bd" && len(c.Args) > 3 && c.Args[2] == "create" {
			t.Fatal("child must not be created when the implementation bead cannot be read")
		}
	}
}

func TestChildLabels(t *testing.T) {
	cases := []struct {
		name string
		impl []string
		want []string
	}{
		{"no labels", nil, []string{"has-acceptance-criteria"}},
		{
			"scope and topic labels are inherited, sorted",
			[]string{"pb", "agent-support", "observability"},
			[]string{"agent-support", "has-acceptance-criteria", "observability", "pb"},
		},
		{
			"workflow-state labels are dropped",
			[]string{
				"agent-support", "human", "human-focus-required", "needs-split-review", "escalated",
				"worker-ready", "mine", "worktree-review", "docket", "pb",
			},
			[]string{"agent-support", "has-acceptance-criteria", "pb"},
		},
		{
			"has-acceptance-criteria is set once",
			[]string{"has-acceptance-criteria", "pb"},
			[]string{"has-acceptance-criteria", "pb"},
		},
		{"duplicates and blanks collapse", []string{"pb", "", "pb"}, []string{"has-acceptance-criteria", "pb"}},
	}
	for _, tc := range cases {
		if got := childLabels(tc.impl); !slices.Equal(got, tc.want) {
			t.Errorf("%s: childLabels(%v) = %v, want %v", tc.name, tc.impl, got, tc.want)
		}
	}
}
