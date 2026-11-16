package provision

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-gwm/internal/gitrun"
	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

// fakeWorktree is a Worktree whose git is the in-process fake.
func fakeWorktree(t *testing.T) (*Worktree, *cmdruntest.Fake) {
	t.Helper()
	f := cmdruntest.New(t)
	g := gitrun.Git{Run: f.Runner()}
	return NewWorktree("/repo", g, time.Minute, time.Minute), f
}

func gitArgs(c cmdrun.Call) string { return strings.Join(c.Args, " ") }

// The removal sequence stops the file-system monitor before the worktree is
// removed (INV-GWM-27); the manager owns the order, the provisioner's
// commands must have the shapes it relies on.
func TestRemovalSequenceCommandShapes(t *testing.T) {
	w, f := fakeWorktree(t)
	f.On(cmdruntest.MatchName("git")).Return(cmdruntest.Out(""))
	ctx := t.Context()
	for _, step := range []func() error{
		func() error { return w.StopFSMonitor(ctx, "/wt") },
		func() error { return w.Unlock(ctx, "/wt") },
		func() error { return w.Remove(ctx, "/wt") },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{
		"-C /wt fsmonitor--daemon stop",
		"-C /repo worktree unlock /wt",
		"-C /repo worktree remove --force /wt",
	}
	calls := f.Calls()
	if len(calls) != len(want) {
		t.Fatalf("calls = %d, want %d", len(calls), len(want))
	}
	for i, c := range calls {
		if gitArgs(c) != want[i] {
			t.Errorf("call %d = %q, want %q", i, gitArgs(c), want[i])
		}
	}
}

func TestStopFSMonitorNotRunningIsSuccess(t *testing.T) {
	w, f := fakeWorktree(t)
	f.On(cmdruntest.MatchName("git")).Return(cmdruntest.Fail(1, "fsmonitor--daemon is not running"))
	if err := w.StopFSMonitor(t.Context(), "/wt"); err != nil {
		t.Fatalf("not running must be success: %v", err)
	}
}

func TestStopFSMonitorOtherFailureIsReported(t *testing.T) {
	w, f := fakeWorktree(t)
	f.On(cmdruntest.MatchName("git")).Return(cmdruntest.Fail(128, "fatal: boom"))
	if err := w.StopFSMonitor(t.Context(), "/wt"); err == nil {
		t.Fatal("a real failure was swallowed")
	}
}

func TestBranchSpecCommandShapes(t *testing.T) {
	cases := []struct {
		name string
		b    BranchSpec
		add  string
		sw   string
	}{
		{"new", BranchSpec{Branch: "b", Create: true, Commit: "c"}, "-b b /d c", "-c b c"},
		{"reset", BranchSpec{Branch: "b", Reset: true, Commit: "c"}, "-B b /d c", "-C b c"},
		{"detach", BranchSpec{Detach: true, Commit: "c"}, "--detach /d c", "--detach c"},
		{"existing", BranchSpec{Branch: "b"}, "/d b", "b"},
	}
	for _, c := range cases {
		if got := strings.Join(c.b.args("/d", true), " "); got != c.add {
			t.Errorf("%s add = %q, want %q", c.name, got, c.add)
		}
		if got := strings.Join(c.b.args("/d", false), " "); got != c.sw {
			t.Errorf("%s switch = %q, want %q", c.name, got, c.sw)
		}
	}
}

// With no identity anywhere (no config, no environment, no hostname guess)
// Preserve falls back to the pg-gwm author.
func TestPreserveFallbackAuthor(t *testing.T) {
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "provision", InitialCommit: true})
	ctx := t.Context()
	for _, k := range []string{"user.name", "user.email"} {
		if _, err := repo.Client.Run(ctx, "config", "--local", "--unset", k); err != nil {
			t.Fatal(err)
		}
	}
	g := gitrun.Git{ExtraEnv: []string{
		"HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=user.useConfigOnly", "GIT_CONFIG_VALUE_0=true",
		"GIT_CEILING_DIRECTORIES=" + filepath.Dir(repo.Root()),
	}}
	w := NewWorktree(repo.Dir, g, time.Minute, time.Minute)
	dir, err := repo.RootPath("wt")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Create(ctx, dir, BranchSpec{Branch: "wt", Create: true}, "pg-gwm w-x"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := w.Preserve(ctx, dir, "pg-gwm/kept", "body"); err != nil {
		t.Fatal(err)
	}
	out, err := repo.Client.Run(ctx, "log", "-1", "--format=%an <%ae>", "pg-gwm/kept")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "pg-gwm <pg-gwm@localhost>" {
		t.Errorf("author = %q", got)
	}
}
