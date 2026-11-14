package pipeline_test

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/locks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams/seamstest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/worktrees"
)

// These are the process-free unit tests of Start's step 1 (resolve): every
// git answer is scripted on the x/cmdruntest.Fake, so no process is launched.
// The real-git scenarios of the pipeline (locks, rebase, hooks, resume,
// signals) are in integration/pipelinetests behind the integration tag.

const (
	fakeRepo   = "/fake/repo"
	fakeCommon = "/fake/repo/.git"
)

// gitArgs strips the options every git command carries.
func gitArgs(c cmdrun.Call) []string {
	return c.Args[len(gitexec.PinnedOptions):]
}

// scriptedGit answers the read-only git commands of Start's step 1 for a
// repository whose commits are refs/heads/<each of branches>.
func scriptedGit(t *testing.T, branches ...string) *cmdruntest.Fake {
	t.Helper()
	fk := cmdruntest.New(t)
	fk.On(func(c cmdrun.Call) bool {
		return c.Name == "git" && slices.Equal(gitArgs(c), []string{"rev-parse", "--path-format=absolute", "--git-common-dir"})
	}).Respond(func(c cmdrun.Call) cmdrun.Result {
		if c.Dir == "/not/a/repo" {
			return cmdruntest.Fail(128, "fatal: not a git repository")
		}
		return cmdruntest.Out(fakeCommon + "\n")
	})
	fk.On(func(c cmdrun.Call) bool {
		return c.Name == "git" && slices.Equal(gitArgs(c), []string{"worktree", "list", "--porcelain"})
	}).Return(cmdruntest.Out("worktree " + fakeRepo + "\nHEAD " + strings.Repeat("a", 40) + "\nbranch refs/heads/main\n\n"))
	fk.On(func(c cmdrun.Call) bool { // no origin: the primary branch falls back to main
		return c.Name == "git" && slices.Equal(gitArgs(c), []string{"symbolic-ref", "-q", "refs/remotes/origin/HEAD"})
	}).Return(cmdruntest.Fail(1, ""))
	fk.On(func(c cmdrun.Call) bool {
		a := gitArgs(c)
		return c.Name == "git" && len(a) == 4 && a[0] == "rev-parse" && a[1] == "--verify"
	}).Respond(func(c cmdrun.Call) cmdrun.Result {
		rev := strings.TrimSuffix(gitArgs(c)[3], "^{commit}")
		for _, b := range branches {
			if rev == "refs/heads/"+b {
				return cmdruntest.Out(strings.Repeat("b", 40) + "\n")
			}
		}
		return cmdruntest.Fail(1, "")
	})
	return fk
}

func fakeDeps(t *testing.T, fk *cmdruntest.Fake) pipeline.Deps {
	t.Helper()
	return pipeline.Deps{
		Git:   &gitexec.Git{Runner: fk.Runner(), Env: seams.FilteredEnv([]string{"HOME=/fake/home"})},
		Clock: seamstest.NewClock(time.Now()), Liveness: seamstest.NewLiveness(),
		Host: seamstest.Host("laptop.local"), Paths: seamstest.ConfigPaths(t.TempDir()),
		Owner: locks.Owner{Host: "laptop.local", PID: 41721, PIDStart: 1794132898, Token: "t1", ToolVersion: "26.11.10"},
		Sleep: func(context.Context, time.Duration) error { return nil },
		Chdir: func(string) error { return nil },
	}
}

func wantUsage(t *testing.T, fk *cmdruntest.Fake, req pipeline.Request, msg string) {
	t.Helper()
	res, err := pipeline.Start(t.Context(), fakeDeps(t, fk), req)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if res.Prepared != nil || res.Outcome == nil || res.Outcome.Outcome != outcome.UsageError || res.Outcome.Exit != 2 {
		t.Fatalf("got prepared=%v outcome=%+v, want a usage error", res.Prepared != nil, res.Outcome)
	}
	if !strings.Contains(res.Outcome.Message, msg) {
		t.Errorf("message = %q, want it to contain %q", res.Outcome.Message, msg)
	}
	for _, c := range fk.Calls() { // nothing past step 1: no lock ref, no work branch
		if a := gitArgs(c); a[0] == "update-ref" || a[0] == "branch" || a[0] == "worktree" && a[1] != "list" {
			t.Errorf("a usage error ran git %v", a)
		}
	}
}

func TestUsageErrorsTakeNoLock(t *testing.T) {
	t.Parallel()
	land := func(branch, target string) pipeline.Request {
		return pipeline.Request{Operation: worktrees.Land, Branch: branch, Target: target, Dir: fakeRepo}
	}
	cases := []struct {
		name string
		req  pipeline.Request
		msg  string
	}{
		{"unknown operation", pipeline.Request{Operation: "merge", Branch: "a", Dir: fakeRepo}, "unknown operation"},
		{"no branch", land("", "main"), "BRANCH is required"},
		{"branch equals target", land("feature/x", "feature/x"), "both feature/x"},
		{"default target is the branch", land("main", ""), "primary branch"},
		{"target does not exist", land("feature/x", "nope"), "TARGET nope does not exist"},
		{"branch does not exist", land("nope", "main"), "BRANCH nope does not exist"},
		{"missing onto", pipeline.Request{Operation: worktrees.Rebase, Branch: "feature/x", Dir: fakeRepo}, "--onto"},
		{"onto not a commit", pipeline.Request{Operation: worktrees.Rebase, Branch: "feature/x", Onto: "nope", Dir: fakeRepo}, "REF nope"},
		{"not a repository", pipeline.Request{Operation: worktrees.Land, Branch: "a", Target: "b", Dir: "/not/a/repo"}, "not inside a git repository"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantUsage(t, scriptedGit(t, "main", "feature/x"), tc.req, tc.msg)
		})
	}
}

func TestNamespaceCollisionNeedsNoGit(t *testing.T) {
	t.Parallel()
	for _, req := range []pipeline.Request{
		{Operation: worktrees.Land, Branch: "pg-git-lander/idle/x", Target: "main", Dir: fakeRepo},
		{Operation: worktrees.Land, Branch: "feature/x", Target: "pg-git-lander/y", Dir: fakeRepo},
		{Operation: worktrees.Rebase, Branch: "pg-git-lander", Onto: "main", Dir: fakeRepo},
	} {
		fk := cmdruntest.New(t) // any git call fails the test: a refusal needs none
		res, err := pipeline.Start(t.Context(), fakeDeps(t, fk), req)
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		if res.Prepared != nil || res.Outcome == nil || res.Outcome.Outcome != outcome.Refused || res.Outcome.Reason != string(outcome.RefNamespaceCollision) {
			t.Fatalf("%+v: got prepared=%v outcome=%+v", req, res.Prepared != nil, res.Outcome)
		}
		if n := len(fk.Calls()); n != 0 {
			t.Errorf("%d git calls, want none", n)
		}
	}
}

// TestPackageTestsLaunchNoProcess is the guard of the unit tier: the files of
// this package may not import the real-git fixtures or name the real Runner,
// and a real-git test belongs in integration/pipelinetests.
func TestPackageTestsLaunchNoProcess(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*_test.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob: %v (%d files)", err, len(files))
	}
	banned := []string{"github.com/phillipgreenii/x/gitfixture", "github.com/phillipgreenii/x/gittest", "os/exec"}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "cmdrun.Exec") && f != "pipeline_test.go" {
			t.Errorf("%s uses the real Runner", f)
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range af.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if slices.Contains(banned, p) {
				t.Errorf("%s imports %s: real-git tests belong in integration/pipelinetests", f, p)
			}
		}
	}
}
