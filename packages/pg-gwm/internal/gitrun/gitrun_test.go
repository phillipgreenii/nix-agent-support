package gitrun_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-gwm/internal/gitrun"
	"github.com/phillipgreenii/pg-gwm/internal/gitrun/gitruntest"
	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

func TestExecDropsAmbientGitDir(t *testing.T) {
	a := gittest.New(t, gitfixture.RepoOptions{InitialCommit: true})
	b := gittest.New(t, gitfixture.RepoOptions{InitialCommit: true})
	t.Setenv("GIT_DIR", b.Dir+"/.git")
	t.Setenv("GIT_WORK_TREE", b.Dir)
	g := gitruntest.New(t, a)
	out, err := g.Exec(context.Background(), a.Dir, time.Minute, "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out); got != a.Dir {
		t.Fatalf("toplevel = %q, want %q", got, a.Dir)
	}
}

func TestExecCallShape(t *testing.T) {
	t.Setenv("GIT_INDEX_FILE", "/nowhere")
	t.Setenv("PG_GWM_KEEP", "1")
	fake := cmdruntest.New(t)
	fake.On(cmdruntest.Any).Return(cmdruntest.Out("ok"))
	g := gitrun.Git{Run: fake.Runner(), Bin: "gitbin", ExtraEnv: []string{"X=1"}}
	if _, err := g.Exec(context.Background(), "/d", 7*time.Second, "status"); err != nil {
		t.Fatal(err)
	}
	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	c := calls[0]
	if c.Name != "gitbin" || !slices.Equal(c.Args, []string{"-C", "/d", "status"}) {
		t.Fatalf("call = %+v", c)
	}
	if !c.OwnProcGroup || c.TermGrace != 30*time.Second || c.Timeout != 7*time.Second || c.EnvMode != cmdrun.EnvExact {
		t.Fatalf("call = %+v", c)
	}
	for _, kv := range c.Env {
		if strings.HasPrefix(kv, "GIT_") && !slices.Contains([]string{"GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true", "GIT_OPTIONAL_LOCKS=0"}, kv) {
			t.Fatalf("inherited %s", kv)
		}
	}
	if !slices.Contains(c.Env, "PG_GWM_KEEP=1") {
		t.Fatal("parent env not kept")
	}
	tail := c.Env[len(c.Env)-5:]
	want := []string{"GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C", "X=1"}
	if !slices.Equal(tail, want) {
		t.Fatalf("env tail = %v, want %v", tail, want)
	}
	_ = os.Environ
}

func TestExecTimeoutReturnsErrTimedOut(t *testing.T) {
	fake := cmdruntest.New(t)
	fake.On(cmdruntest.Any).Return(cmdrun.Result{Code: -1, Signal: "terminated", TimedOut: true, CtxErr: context.DeadlineExceeded})
	g := gitrun.Git{Run: fake.Runner()}
	_, err := g.Exec(context.Background(), "/d", time.Second, "fetch")
	if !errors.Is(err, gitrun.ErrTimedOut) {
		t.Fatalf("err = %v", err)
	}
}

func TestExecFailureShapes(t *testing.T) {
	fake := cmdruntest.New(t)
	fake.On(cmdruntest.Match("git", "-C", "/d", "a")).Return(cmdruntest.Fail(128, "fatal: nope"))
	fake.On(cmdruntest.Match("git", "-C", "/d", "b")).Return(cmdruntest.Fail(128, "fatal: Unable to create '/x/index.lock': File exists."))
	g := gitrun.Git{Run: fake.Runner()}
	_, err := g.Exec(context.Background(), "/d", time.Second, "a")
	var ge *gitrun.GitError
	if !errors.As(err, &ge) || ge.Code != 128 || ge.Stderr != "fatal: nope" || errors.Is(err, gitrun.ErrStaleLock) {
		t.Fatalf("err = %v", err)
	}
	_, err = g.Exec(context.Background(), "/d", time.Second, "b")
	if !errors.Is(err, gitrun.ErrStaleLock) || !errors.As(err, &ge) {
		t.Fatalf("err = %v", err)
	}
}
