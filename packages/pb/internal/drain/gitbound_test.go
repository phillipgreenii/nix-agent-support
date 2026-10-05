package drain

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/pb/internal/run"
)

func TestFsmonitorOffEnv(t *testing.T) {
	got := fsmonitorOffEnv([]string{"A=1"})
	want := []string{"A=1", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.fsmonitor", "GIT_CONFIG_VALUE_0=false"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// An inherited GIT_CONFIG_COUNT is extended, never clobbered.
	got = fsmonitorOffEnv([]string{"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=a.b", "GIT_CONFIG_VALUE_0=x", "GIT_CONFIG_KEY_1=c.d", "GIT_CONFIG_VALUE_1=y"})
	want = []string{
		"GIT_CONFIG_KEY_0=a.b", "GIT_CONFIG_VALUE_0=x", "GIT_CONFIG_KEY_1=c.d", "GIT_CONFIG_VALUE_1=y",
		"GIT_CONFIG_COUNT=3", "GIT_CONFIG_KEY_2=core.fsmonitor", "GIT_CONFIG_VALUE_2=false",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestGitBound_onlyGitGetsEnvAndTimeout(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("git", []string{"status"}, run.Result{}, nil)
	f.AddResponse("pg-hooks", []string{"status"}, run.Result{}, nil)
	g := gitBound{inner: f, timeout: 7 * time.Second}
	if _, err := g.Run(context.Background(), "git", []string{"status"}, run.Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(context.Background(), "pg-hooks", []string{"status"}, run.Options{}); err != nil {
		t.Fatal(err)
	}
	calls := f.Calls()
	if calls[0].Opts.Timeout != 7*time.Second || !containsAll(calls[0].Opts.Env,
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.fsmonitor", "GIT_CONFIG_VALUE_0=false") {
		t.Errorf("git call opts = %+v", calls[0].Opts)
	}
	if calls[1].Opts.Timeout != 0 || calls[1].Opts.Env != nil {
		t.Errorf("non-git call must pass through untouched, opts = %+v", calls[1].Opts)
	}
}

func containsAll(env []string, want ...string) bool {
	for _, w := range want {
		found := false
		for _, e := range env {
			if e == w {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// fakeGit puts a `git` shim first on PATH. Every call is logged as
// "<GIT_CONFIG_COUNT>|<KEY_0>|<VALUE_0>|<argv>". A `worktree add` runs the REAL
// git (so the worktree, its registration, its lock-free admin dir and the -b
// branch really exist — the half-created state), then hangs with a background
// grandchild whose pid is written to the returned pidfile. Everything else is
// delegated to real git unchanged.
func fakeGit(t *testing.T) (log, pidfile string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	log = filepath.Join(dir, "git.log")
	pidfile = filepath.Join(dir, "grandchild.pid")
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s|%%s|%%s|%%s\n' "${GIT_CONFIG_COUNT:-}" "${GIT_CONFIG_KEY_0:-}" "${GIT_CONFIG_VALUE_0:-}" "$*" >>%q
case " $* " in
*" worktree add "*)
  %q "$@" || exit $?
  sleep 300 &
  echo $! >%q
  sleep 300
  ;;
*) exec %q "$@" ;;
esac
`, log, realGit, pidfile, realGit)
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log, pidfile
}

func pidAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func readPid(t *testing.T, pidfile string) int {
	t.Helper()
	b, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatalf("grandchild pidfile: %v (worktree add shim never reached its hang?)", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func branchListed(t *testing.T, repo, branch string) bool {
	t.Helper()
	return strings.TrimSpace(gitTest(t, repo, "branch", "--list", branch)) != ""
}

func TestIsolate_hungWorktreeAddIsBoundedAndCleansOnlyItsOwn(t *testing.T) {
	repo := newRepo(t)
	// Pre-existing isolation for ANOTHER bead must survive the cleanup.
	if _, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-other"}); err != nil {
		t.Fatal(err)
	}
	otherWT := filepath.Join(repo, ".worktrees", "pg2-other")
	log, pidfile := fakeGit(t)

	start := time.Now()
	_, err := Isolate(context.Background(), run.CLIRunner{}, Params{
		RepoPath: repo, BeadID: "pg2-hang", GitTimeout: 2 * time.Second,
	})
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("Isolate took %s; the bound did not hold", elapsed)
	}
	if !errors.Is(err, ErrGitTimeout) || !errors.Is(err, run.ErrTimeout) {
		t.Fatalf("err = %v, want ErrGitTimeout wrapping run.ErrTimeout", err)
	}
	if errors.Is(err, ErrConflict) {
		t.Errorf("a timeout must not look like a conflict (CLI exit 3): %v", err)
	}
	for _, want := range []string{"fsmonitor", "fseventsd", "worktree add"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	// The whole process group was killed, not just the direct child.
	if pid := readPid(t, pidfile); pidAlive(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("grandchild %d survived the timeout (only the direct child was killed)", pid)
	}
	// Only its own half-created worktree and branch are gone.
	mine := filepath.Join(repo, ".worktrees", "pg2-hang")
	if _, statErr := os.Lstat(mine); statErr == nil {
		t.Errorf("half-created worktree %s left behind", mine)
	}
	if branchListed(t, repo, "drain/pg2-hang") {
		t.Errorf("branch drain/pg2-hang created by the timed-out call left behind")
	}
	if list := gitTest(t, repo, "worktree", "list", "--porcelain"); strings.Contains(list, "pg2-hang") {
		t.Errorf("worktree registration left behind:\n%s", list)
	}
	if _, statErr := os.Stat(filepath.Join(otherWT, "f.txt")); statErr != nil {
		t.Errorf("pre-existing worktree for another bead was disturbed: %v", statErr)
	}
	if !branchListed(t, repo, "drain/pg2-other") {
		t.Errorf("pre-existing branch drain/pg2-other was removed")
	}
	// Every git call ran with fsmonitor forced off, per-call.
	logged, _ := os.ReadFile(log)
	for _, line := range strings.Split(strings.TrimSpace(string(logged)), "\n") {
		if strings.Contains(line, "user.email=test") {
			continue // the test's own gitTest verification calls, not pb's
		}
		if !strings.HasPrefix(line, "1|core.fsmonitor|false|") {
			t.Errorf("git call without the per-call fsmonitor-off env: %q", line)
		}
	}
	if !strings.Contains(string(logged), "worktree add") {
		t.Errorf("shim never saw worktree add; log:\n%s", logged)
	}
}

func TestIsolate_hungAddOfParkedBranchKeepsTheBranch(t *testing.T) {
	repo := newRepo(t)
	if _, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-parked"}); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "worktree", "remove", filepath.Join(repo, ".worktrees", "pg2-parked"))
	_, pidfile := fakeGit(t)

	_, err := Isolate(context.Background(), run.CLIRunner{}, Params{
		RepoPath: repo, BeadID: "pg2-parked", GitTimeout: 2 * time.Second,
	})
	if !errors.Is(err, ErrGitTimeout) {
		t.Fatalf("err = %v, want ErrGitTimeout", err)
	}
	if pid := readPid(t, pidfile); pidAlive(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("grandchild %d survived", pid)
	}
	// The branch pre-existed this call (parked work): it MUST NOT be deleted.
	if !branchListed(t, repo, "drain/pg2-parked") {
		t.Errorf("pre-existing parked branch was deleted by the cleanup")
	}
	if _, statErr := os.Lstat(filepath.Join(repo, ".worktrees", "pg2-parked")); statErr == nil {
		t.Errorf("half-created worktree left behind")
	}
}
