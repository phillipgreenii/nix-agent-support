package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pb/internal/gate"
	"github.com/phillipgreenii/pb/internal/run"
	"github.com/phillipgreenii/pb/internal/unstick"
)

// Synthetic fixtures only (public repo). Clock: unstickNow.
const (
	unstickNow  = "2026-10-10T12:00:00Z"
	unstickRoot = "/ws/root"
)

// unstickExport covers one branch per triage bucket:
//
//	a-1 ready task (drainable)          a-2 blocked on a-1 -> LIVE
//	a-3 deferred, elapsed defer_until   a-4 blocked on a-3 -> REVIEW (clusters with a-3)
//	a-5 valid marker, quiet -> marker   a-6 malformed (date-only) marker -> REVIEW
//	a-7 in_progress (claim candidate)   a-8 closed
//	a-9 open, assigned, not ready (claim candidate)
const unstickExport = `{"_type":"issue","id":"a-1","title":"Ready task","status":"open","priority":2,"issue_type":"task","created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-10T09:00:00Z"}
{"_type":"issue","id":"a-2","title":"Waits on a-1","status":"blocked","priority":2,"issue_type":"task","created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-10T09:00:00Z","dependencies":[{"issue_id":"a-2","depends_on_id":"a-1","type":"blocks"}]}
{"_type":"issue","id":"a-3","title":"Deferred long ago","status":"deferred","priority":3,"issue_type":"task","defer_until":"2026-01-01T00:00:00Z","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-02-01T00:00:00Z","labels":["alpha"]}
{"_type":"issue","id":"a-4","title":"Waits on a-3","status":"open","priority":3,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-02-01T00:00:00Z","dependencies":[{"issue_id":"a-4","depends_on_id":"a-3","type":"blocks"}]}
{"_type":"issue","id":"a-5","title":"Already reviewed","status":"blocked","priority":3,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-10-10T10:05:00Z","notes":"[unstick 2026-10-10T10:00:00Z] unchanged: waiting on vendor; recheck-when: 2027-01-01"}
{"_type":"issue","id":"a-6","title":"Sloppy marker","status":"open","priority":3,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-02-01T00:00:00Z","notes":"[unstick 2026-10-01] unchanged: x; recheck-when: on-change","labels":["beta"]}
{"_type":"issue","id":"a-7","title":"Being worked","status":"in_progress","priority":1,"issue_type":"task","assignee":"worker-1","created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-10T11:00:00Z"}
{"_type":"issue","id":"a-8","title":"Done","status":"closed","priority":2,"issue_type":"task","created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-09T00:00:00Z","closed_at":"2026-10-09T00:00:00Z","close_reason":"done"}
{"_type":"issue","id":"a-9","title":"Assigned but stuck","status":"open","priority":2,"issue_type":"task","assignee":"worker-2","created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-05T00:00:00Z","dependencies":[{"issue_id":"a-9","depends_on_id":"a-3","type":"blocks"}]}
`

const unstickReadyJSON = `{"data":[{"id":"a-1","status":"open","issue_type":"task","updated_at":"2026-10-10T09:00:00Z"}],"schema_version":1}`

// exportRunner wraps a FakeRunner and, like the real bd, materialises the
// file named by `export -o <path>` (the FakeRunner itself only scripts replies).
type exportRunner struct {
	*run.FakeRunner
	content string
}

func (e *exportRunner) Run(ctx context.Context, name string, args []string, o run.Options) (run.Result, error) {
	res, err := e.FakeRunner.Run(ctx, name, args, o)
	if err == nil && name == "bd" {
		for i, a := range args {
			if a == "export" && i+2 < len(args) && args[i+1] == "-o" {
				if werr := os.WriteFile(args[i+2], []byte(e.content), 0o600); werr != nil {
					return res, werr
				}
			}
		}
	}
	return res, err
}

func testEnv(r run.Runner) unstickEnv {
	return unstickEnv{
		Runner:  r,
		Now:     func() time.Time { t, _ := time.Parse(time.RFC3339, unstickNow); return t },
		TmpBase: os.TempDir(),
		Getenv:  func(string) string { return "" },
		Getwd:   func() (string, error) { return "/nowhere", nil },
		GateCheck: func(context.Context, string, time.Time) (gate.CheckResult, error) {
			return gate.CheckResult{Resolved: []string{}, Skipped: []gate.Skip{}, StaleActions: []gate.StaleAction{}}, nil
		},
	}
}

// bdScript scripts the two prepare reads for workdir w.
func bdScript(f *run.FakeRunner, w, ready string) {
	f.AddResponse("bd", []string{"-C", unstickRoot, "export", "-o", filepath.Join(w, unstick.ExportFile)}, run.Result{}, nil)
	f.AddResponse("bd", []string{"-C", unstickRoot, "ready", "-n", "0", "--json"}, run.Result{Stdout: ready}, nil)
}

// execUnstick runs `pb unstick <args>` and returns stdout, stderr and the error.
func execUnstick(env unstickEnv, stdin string, args ...string) (string, string, error) {
	cmd := newUnstickCmdWith(env)
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errb.String(), err
}

// preparedWorkdir runs a successful prepare and returns the work directory.
func preparedWorkdir(t *testing.T) string {
	t.Helper()
	w := filepath.Join(t.TempDir(), "w")
	f := run.NewFakeRunner()
	bdScript(f, w, unstickReadyJSON)
	env := testEnv(&exportRunner{FakeRunner: f, content: unstickExport})
	if _, _, err := execUnstick(env, "", "prepare", "--root", unstickRoot, "--workdir", w); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	return w
}

func updateCmdGolden() bool { return os.Getenv("UNSTICK_CMD_UPDATE_GOLDEN") != "" }

// checkCmdGolden compares got with testdata/unstick/<name> (.txt goldens only:
// the prettier hook would reformat .json).
func checkCmdGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "unstick", name)
	if updateCmdGolden() {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (UNSTICK_CMD_UPDATE_GOLDEN=1 to create): %v", path, err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("golden %s differs (UNSTICK_CMD_UPDATE_GOLDEN=1 to update)\n--- want\n%s\n--- got\n%s", path, want, got)
	}
}

func TestExitCodeFor(t *testing.T) {
	plain := errors.New("boom")
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"plain error", plain, 1},
		{"bd failure", bdFailure(plain), 2},
		{"wrapped bd failure", fmt.Errorf("ctx: %w", bdFailure(plain)), 2},
		{"explicit code", newExitError(7, plain), 7},
	}
	for _, c := range cases {
		if got := exitCodeFor(c.err); got != c.want {
			t.Errorf("%s: exitCodeFor = %d, want %d", c.name, got, c.want)
		}
	}
	if err := bdFailure(plain); !errors.Is(err, plain) || err.Error() != "boom" {
		t.Errorf("bdFailure must keep the message and unwrap: %v", err)
	}
}

func TestUnstick_subcommandsRegistered(t *testing.T) {
	cmd := newUnstickCmd()
	for _, name := range []string{"prepare", "batch", "marker", "report"} {
		if c, _, err := cmd.Find([]string{name}); err != nil || c.Name() != name {
			t.Errorf("subcommand %q not registered", name)
		}
	}
	found := false
	for _, c := range newRootCmd().Commands() {
		found = found || c.Name() == "unstick"
	}
	if !found {
		t.Error("unstick not wired into the root command")
	}
}

func TestUnstick_hiddenNowFlag(t *testing.T) {
	for _, name := range []string{"prepare", "marker", "report"} {
		c, _, _ := newUnstickCmd().Find([]string{name})
		f := c.Flags().Lookup("now")
		if f == nil || !f.Hidden {
			t.Errorf("%s: want a hidden --now flag", name)
		}
	}
}

func TestResolveRoot(t *testing.T) {
	env := testEnv(run.NewFakeRunner())
	if got, err := resolveRoot(env, "/abs/ws"); err != nil || got != "/abs/ws" {
		t.Errorf("explicit = %q, %v", got, err)
	}
	if _, err := resolveRoot(env, "rel/ws"); err == nil {
		t.Error("relative --root must be rejected")
	}
	env.Getenv = func(k string) string {
		if k == "PN_WORKSPACE_ROOT" {
			return "/from/env"
		}
		return ""
	}
	if got, err := resolveRoot(env, ""); err != nil || got != "/from/env" {
		t.Errorf("env fallback = %q, %v", got, err)
	}
}
