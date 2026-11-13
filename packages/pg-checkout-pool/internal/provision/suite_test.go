package provision

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-checkout-pool/internal/pool"
)

// hermeticEnv isolates git from the operator's configuration and identity.
var hermeticEnv = []string{
	"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
	"GIT_AUTHOR_NAME=Pool Test", "GIT_AUTHOR_EMAIL=pool-test@example.invalid",
	"GIT_COMMITTER_NAME=Pool Test", "GIT_COMMITTER_EMAIL=pool-test@example.invalid",
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), hermeticEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

type fixture struct {
	repo, slot, hookLog, c1, c2 string
}

// newFixture builds a real repository with two commits and a post-checkout
// hook that logs each run and writes an ignored hook-made file.
func newFixture(t *testing.T) fixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(base, "repo")
	run(t, base, "init", "-q", "-b", "main", repo)
	write(t, filepath.Join(repo, ".gitignore"), "*.o\nbuild/\n.flox/\n.envrc.local\n.claude/settings.local.json\nCLAUDE.local.md\n.vscode/settings.json\n.hookmade\n")
	write(t, filepath.Join(repo, "a.txt"), "one\n")
	run(t, repo, "add", ".")
	run(t, repo, "commit", "-q", "-m", "c1")
	c1 := run(t, repo, "rev-parse", "HEAD")
	write(t, filepath.Join(repo, "a.txt"), "two\n")
	write(t, filepath.Join(repo, "sub", "b.txt"), "b\n")
	run(t, repo, "add", ".")
	run(t, repo, "commit", "-q", "-m", "c2")
	c2 := run(t, repo, "rev-parse", "HEAD")

	hookLog := filepath.Join(base, "hook.log")
	hook := "#!/bin/sh\necho ran >> '" + hookLog + "'\necho made > .hookmade\n"
	write(t, filepath.Join(repo, ".git", "hooks", "post-checkout"), hook)
	if err := os.Chmod(filepath.Join(repo, ".git", "hooks", "post-checkout"), 0o755); err != nil {
		t.Fatal(err)
	}
	return fixture{repo: repo, slot: filepath.Join(base, "slot"), hookLog: hookLog, c1: c1, c2: c2}
}

// factory builds the provisioner under test from a config.
type factory func(t *testing.T, cfg WorktreeConfig) Provisioner

// suite is one implementation under the shared provisioner test suite.
type suite struct {
	t  *testing.T
	mk factory
}

func (s suite) prov(f fixture, mut func(*WorktreeConfig)) Provisioner {
	s.t.Helper()
	cfg := WorktreeConfig{Repo: f.repo, Env: hermeticEnv, Timeout: 20 * time.Second}
	if mut != nil {
		mut(&cfg)
	}
	return s.mk(s.t, cfg)
}

func (f fixture) hookRuns(t *testing.T) int {
	t.Helper()
	b, err := os.ReadFile(f.hookLog)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "ran")
}

func (s suite) TestResetRemovesIgnoredOutsideKeepAndKeepsKeepList() {
	t := s.t
	f := newFixture(t)
	w := s.prov(f, nil)
	ctx := context.Background()
	if err := w.Create(ctx, f.slot, f.c1); err != nil {
		t.Fatal(err)
	}
	kept := []string{".flox/env.json", ".envrc.local", ".claude/settings.local.json", "CLAUDE.local.md", ".vscode/settings.json"}
	for _, k := range kept {
		write(t, filepath.Join(f.slot, k), "keep")
	}
	junk := []string{"x.o", "build/out.bin", "untracked.txt", "sub/deep/y.o", ".hookmade", ".claude/other.json", "nested/.git/HEAD"}
	for _, j := range junk {
		write(t, filepath.Join(f.slot, j), "junk")
	}
	write(t, filepath.Join(f.slot, "a.txt"), "dirty\n")

	head, err := w.Reset(ctx, f.slot, f.c2, "lease/s1")
	if err != nil {
		t.Fatal(err)
	}
	if head != f.c2 {
		t.Errorf("head %s want %s", head, f.c2)
	}
	for _, j := range junk {
		if exists(filepath.Join(f.slot, j)) {
			t.Errorf("junk survived reset: %s", j)
		}
	}
	for _, k := range kept {
		if !exists(filepath.Join(f.slot, k)) {
			t.Errorf("keep-list path lost: %s", k)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(f.slot, "a.txt")); string(b) != "two\n" {
		t.Errorf("tracked file not restored: %q", b)
	}
	if got := run(t, f.slot, "branch", "--show-current"); got != "lease/s1" {
		t.Errorf("branch %q", got)
	}
	if st := run(t, f.slot, "status", "--porcelain"); st != "" {
		t.Errorf("tree dirty: %q", st)
	}
	if err := w.Verify(ctx, f.slot); err != nil {
		t.Errorf("verify: %v", err)
	}
}

func (s suite) TestPerRepoKeepListReplacesDefault() {
	t := s.t
	f := newFixture(t)
	w := s.prov(f, func(c *WorktreeConfig) { c.Keep = []string{".flox"} })
	ctx := context.Background()
	if err := w.Create(ctx, f.slot, f.c1); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.slot, ".flox/e"), "k")
	write(t, filepath.Join(f.slot, ".envrc.local"), "gone")
	if _, err := w.Reset(ctx, f.slot, f.c1, ""); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(f.slot, ".flox/e")) || exists(filepath.Join(f.slot, ".envrc.local")) {
		t.Error("custom keep list not honoured")
	}
	if got := run(t, f.slot, "branch", "--show-current"); got != "" {
		t.Errorf("detached reset left branch %q", got)
	}
}

func (s suite) TestHookRunsAtCreationNotAtResetAndHooksPathNeverPersisted() {
	t := s.t
	f := newFixture(t)
	w := s.prov(f, nil)
	ctx := context.Background()
	if err := w.Create(ctx, f.slot, f.c1); err != nil {
		t.Fatal(err)
	}
	if n := f.hookRuns(t); n != 1 {
		t.Fatalf("hook runs after create = %d, want 1", n)
	}
	if !exists(filepath.Join(f.slot, ".hookmade")) {
		t.Fatal("hook did not write its file at creation")
	}
	for _, c := range []string{f.c2, f.c1, f.c2} {
		if _, err := w.Reset(ctx, f.slot, c, "lease/s1"); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Detach(ctx, f.slot); err != nil {
		t.Fatal(err)
	}
	if n := f.hookRuns(t); n != 1 {
		t.Errorf("hook runs after resets and detach = %d, want still 1", n)
	}
	for _, d := range []string{f.repo, f.slot} {
		cmd := exec.CommandContext(t.Context(), "git", "-C", d, "config", "--get-regexp", "(?i)^core\\.hookspath$")
		cmd.Env = append(os.Environ(), hermeticEnv...)
		if out, err := cmd.Output(); err == nil || len(out) != 0 {
			t.Errorf("core.hooksPath set in config of %s: %q", d, out)
		}
	}
	for _, p := range []string{filepath.Join(f.repo, ".git", "config"), filepath.Join(f.repo, ".git", "config.worktree")} {
		if b, err := os.ReadFile(p); err == nil && strings.Contains(strings.ToLower(string(b)), "hookspath") {
			t.Errorf("hooksPath written to %s", p)
		}
	}
}

func (s suite) TestRunHookOnResetOptIn() {
	t := s.t
	f := newFixture(t)
	w := s.prov(f, func(c *WorktreeConfig) { c.RunHookOnReset = true })
	ctx := context.Background()
	if err := w.Create(ctx, f.slot, f.c1); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Reset(ctx, f.slot, f.c2, "lease/s1"); err != nil {
		t.Fatal(err)
	}
	if n := f.hookRuns(t); n < 2 {
		t.Errorf("hook runs = %d, want >= 2 when opted in", n)
	}
}

func (s suite) TestDetachFreesBranch() {
	t := s.t
	f := newFixture(t)
	w := s.prov(f, nil)
	ctx := context.Background()
	if err := w.Create(ctx, f.slot, f.c1); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Reset(ctx, f.slot, f.c2, "lease/s1"); err != nil {
		t.Fatal(err)
	}
	if err := w.Detach(ctx, f.slot); err != nil {
		t.Fatal(err)
	}
	if got := run(t, f.slot, "branch", "--show-current"); got != "" {
		t.Errorf("still on branch %q", got)
	}
	run(t, f.slot, "rev-parse", "--verify", "lease/s1")
}

func (s suite) TestStaleLockIsQuarantined() {
	t := s.t
	f := newFixture(t)
	w := s.prov(f, nil)
	ctx := context.Background()
	if err := w.Create(ctx, f.slot, f.c1); err != nil {
		t.Fatal(err)
	}
	gd := run(t, f.slot, "rev-parse", "--absolute-git-dir")
	write(t, filepath.Join(gd, "index.lock"), "")

	if err := w.Verify(ctx, f.slot); !errors.Is(err, ErrStaleLock) {
		t.Errorf("verify err = %v, want ErrStaleLock", err)
	}
	h := leasedHandle(t, "s1")
	_, err := w.Reset(ctx, f.slot, f.c2, "lease/s1")
	if !errors.Is(err, ErrStaleLock) {
		t.Fatalf("reset err = %v, want ErrStaleLock", err)
	}
	if err := QuarantineIfWedged(h, err); !errors.Is(err, ErrStaleLock) {
		t.Errorf("returned %v", err)
	}
	if got := h.Record().State; got != pool.Quarantined {
		t.Errorf("state %s, want quarantined", got)
	}
	if !strings.Contains(h.Record().Reason, "index.lock") {
		t.Errorf("reason %q", h.Record().Reason)
	}
	if !exists(filepath.Join(gd, "index.lock")) {
		t.Error("lock file was deleted; slot must stay as evidence")
	}
}

func (s suite) TestWedgedGitIsKilledAndQuarantined() {
	t := s.t
	f := newFixture(t)
	if err := s.prov(f, nil).Create(context.Background(), f.slot, f.c1); err != nil {
		t.Fatal(err)
	}
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	// A git wrapper that hangs on checkout and defers to the real git otherwise.
	wrapper := filepath.Join(t.TempDir(), "git")
	write(t, wrapper, "#!/bin/sh\nfor a in \"$@\"; do if [ \"$a\" = checkout ]; then sleep 60; fi; done\nexec '"+real+"' \"$@\"\n")
	if err := os.Chmod(wrapper, 0o755); err != nil {
		t.Fatal(err)
	}
	w := s.prov(f, func(c *WorktreeConfig) { c.GitBin = wrapper; c.Timeout = 500 * time.Millisecond })

	h := leasedHandle(t, "s2")
	start := time.Now()
	_, err = w.Reset(context.Background(), f.slot, f.c2, "lease/s2")
	if !errors.Is(err, ErrWedged) {
		t.Fatalf("err = %v, want ErrWedged", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("reset took %s; the timeout did not bound it", d)
	}
	_ = QuarantineIfWedged(h, err)
	if got := h.Record().State; got != pool.Quarantined {
		t.Errorf("state %s, want quarantined", got)
	}
}

func (s suite) TestOrdinaryErrorDoesNotQuarantine() {
	t := s.t
	f := newFixture(t)
	w := s.prov(f, nil)
	ctx := context.Background()
	if err := w.Create(ctx, f.slot, f.c1); err != nil {
		t.Fatal(err)
	}
	h := leasedHandle(t, "s3")
	_, err := w.Reset(ctx, f.slot, "no-such-commit", "lease/s3")
	if err == nil {
		t.Fatal("expected error for unknown commit")
	}
	if got := QuarantineIfWedged(h, err); got != err || h.Record().State != pool.Leased {
		t.Errorf("ordinary error quarantined slot: state %s", h.Record().State)
	}
}

func (s suite) TestVerifyRejectsForeignAndMissing() {
	t := s.t
	f := newFixture(t)
	w := s.prov(f, nil)
	ctx := context.Background()
	if err := w.Verify(ctx, filepath.Join(filepath.Dir(f.slot), "nope")); !errors.Is(err, ErrUnhealthy) {
		t.Errorf("missing: %v", err)
	}
	other := filepath.Join(filepath.Dir(f.slot), "other")
	run(t, filepath.Dir(f.slot), "init", "-q", other)
	if err := w.Verify(ctx, other); !errors.Is(err, ErrUnhealthy) {
		t.Errorf("foreign repo: %v", err)
	}
}

func TestValidateKeep(t *testing.T) {
	got, err := ValidateKeep([]string{"./a/b/", "/c"})
	if err != nil || got[0] != "a/b" || got[1] != "c" {
		t.Errorf("got %v, %v", got, err)
	}
	for _, bad := range []string{"", "..", "a/../b", "-x", ":(top)a", "*.o", "a//b", "!a"} {
		if _, err := ValidateKeep([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := NewWorktree(WorktreeConfig{Repo: "relative"}); err == nil {
		t.Error("relative repo accepted")
	}
}

// leasedHandle returns a handle on a real store slot moved to leased.
func leasedHandle(t *testing.T, owner string) *pool.Handle {
	t.Helper()
	s := pool.NewStore(t.TempDir(), pool.SystemClock{})
	if err := s.CreateSlot("slot-1"); err != nil {
		t.Fatal(err)
	}
	h, err := s.Acquire(pool.AcquireRequest{OwnerSessionID: owner, OwnerPID: os.Getpid(), Repo: "r", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Unlock)
	if err := h.MarkLeased(); err != nil {
		t.Fatal(err)
	}
	return h
}

func (s suite) TestResolveAndRescue() {
	t := s.t
	f := newFixture(t)
	w := s.prov(f, nil)
	ctx := context.Background()
	if got, err := w.Resolve(ctx, f.c1); err != nil || got != f.c1 {
		t.Errorf("resolve = %q, %v", got, err)
	}
	if _, err := w.Resolve(ctx, "no-such-ref"); !errors.Is(err, ErrRefUnresolvable) {
		t.Errorf("unresolvable: %v", err)
	}
	if err := w.Create(ctx, f.slot, f.c1); err != nil {
		t.Fatal(err)
	}
	if c, err := w.Rescue(ctx, f.slot, "refs/pool-rescue/x"); err != nil || c != "" {
		t.Errorf("clean tree rescue = %q, %v", c, err)
	}
	write(t, filepath.Join(f.slot, "a.txt"), "dirty\n")
	c, err := w.Rescue(ctx, f.slot, "refs/pool-rescue/x")
	if err != nil || c == "" {
		t.Fatalf("dirty rescue = %q, %v", c, err)
	}
	if got := run(t, f.slot, "rev-parse", "refs/pool-rescue/x"); got != c {
		t.Errorf("rescue ref %s, want %s", got, c)
	}
}

// runProvisionerSuite runs the shared provisioner test suite against mk, with
// real temporary directories and real git.
func runProvisionerSuite(t *testing.T, mk factory) {
	t.Run("TestResetRemovesIgnoredOutsideKeepAndKeepsKeepList", func(t *testing.T) { suite{t, mk}.TestResetRemovesIgnoredOutsideKeepAndKeepsKeepList() })
	t.Run("TestPerRepoKeepListReplacesDefault", func(t *testing.T) { suite{t, mk}.TestPerRepoKeepListReplacesDefault() })
	t.Run("TestHookRunsAtCreationNotAtResetAndHooksPathNeverPersisted", func(t *testing.T) { suite{t, mk}.TestHookRunsAtCreationNotAtResetAndHooksPathNeverPersisted() })
	t.Run("TestRunHookOnResetOptIn", func(t *testing.T) { suite{t, mk}.TestRunHookOnResetOptIn() })
	t.Run("TestResolveAndRescue", func(t *testing.T) { suite{t, mk}.TestResolveAndRescue() })
	t.Run("TestDetachFreesBranch", func(t *testing.T) { suite{t, mk}.TestDetachFreesBranch() })
	t.Run("TestStaleLockIsQuarantined", func(t *testing.T) { suite{t, mk}.TestStaleLockIsQuarantined() })
	t.Run("TestWedgedGitIsKilledAndQuarantined", func(t *testing.T) { suite{t, mk}.TestWedgedGitIsKilledAndQuarantined() })
	t.Run("TestOrdinaryErrorDoesNotQuarantine", func(t *testing.T) { suite{t, mk}.TestOrdinaryErrorDoesNotQuarantine() })
	t.Run("TestVerifyRejectsForeignAndMissing", func(t *testing.T) { suite{t, mk}.TestVerifyRejectsForeignAndMissing() })
}
