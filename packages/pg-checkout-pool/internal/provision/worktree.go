package provision

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// DefaultKeepList is the measured default for the ZR monorepo: the flox
// environment, the direnv overrides, and the three files the post-checkout
// hook writes (which a skipped-hook reset must not delete). Per-repo
// configuration replaces it wholesale.
var DefaultKeepList = []string{
	".flox",
	".envrc.local",
	".claude/settings.local.json",
	"CLAUDE.local.md",
	".vscode/settings.json",
}

// DefaultTimeout bounds each git command.
const DefaultTimeout = 5 * time.Minute

// skipHooksPath is the per-command core.hooksPath override that disables every
// hook for one git invocation without writing configuration.
const skipHooksPath = "core.hooksPath=/dev/null"

// WorktreeConfig configures a Worktree provisioner.
type WorktreeConfig struct {
	// Repo is the canonical clone the slots are linked worktrees of.
	Repo string
	// Keep is the per-repository list of ignored paths that survive a reset.
	// Nil means DefaultKeepList; an empty non-nil slice keeps nothing.
	Keep []string
	// Timeout bounds each git command; zero means DefaultTimeout.
	Timeout time.Duration
	// GitBin is the git executable; empty means "git" from PATH.
	GitBin string
	// Env is appended to the sanitised process environment (tests use it for
	// GIT_CONFIG_GLOBAL=/dev/null style hermeticity).
	Env []string
	// RunHookOnReset runs the post-checkout hook on reset-in-place. The
	// default (false) skips it, which is only safe with a keep list that
	// covers every path the hook writes.
	RunHookOnReset bool
}

// Worktree is the git-worktree Provisioner.
type Worktree struct {
	repo    string
	keep    []string
	timeout time.Duration
	gitBin  string
	env     []string
	hook    bool
}

var _ Provisioner = (*Worktree)(nil)

// NewWorktree validates cfg and returns the provisioner.
func NewWorktree(cfg WorktreeConfig) (*Worktree, error) {
	if cfg.Repo == "" || !filepath.IsAbs(cfg.Repo) {
		return nil, fmt.Errorf("repo %q must be an absolute path", cfg.Repo)
	}
	keep := cfg.Keep
	if keep == nil {
		keep = DefaultKeepList
	}
	keep, err := ValidateKeep(keep)
	if err != nil {
		return nil, err
	}
	w := &Worktree{repo: cfg.Repo, keep: keep, timeout: cfg.Timeout, gitBin: cfg.GitBin, env: cfg.Env, hook: cfg.RunHookOnReset}
	if w.timeout <= 0 {
		w.timeout = DefaultTimeout
	}
	if w.gitBin == "" {
		w.gitBin = "git"
	}
	return w, nil
}

// environ is the process environment minus the variables that make git act on
// a different repository than the -C directory, plus non-interactive settings.
func (w *Worktree) environ() []string {
	drop := []string{"GIT_DIR=", "GIT_WORK_TREE=", "GIT_INDEX_FILE=", "GIT_COMMON_DIR=", "GIT_OBJECT_DIRECTORY=", "GIT_NAMESPACE="}
	var env []string
outer:
	for _, kv := range os.Environ() {
		for _, d := range drop {
			if strings.HasPrefix(kv, d) {
				continue outer
			}
		}
		env = append(env, kv)
	}
	env = append(env, "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	return append(env, w.env...)
}

// git runs one bounded git command in dir (the repo itself when dir is empty).
// A timeout kills the whole process group and returns ErrWedged.
func (w *Worktree) git(ctx context.Context, dir string, args ...string) (string, error) {
	if dir == "" {
		dir = w.repo
	}
	ctx, cancel := context.WithTimeout(ctx, w.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, w.gitBin, append([]string{"-C", dir}, args...)...)
	cmd.Env = w.environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", fmt.Errorf("%w: git %s after %s", ErrWedged, strings.Join(args, " "), w.timeout)
	}
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		if strings.Contains(msg, ".lock': File exists") {
			return "", fmt.Errorf("%w: git %s: %s", ErrStaleLock, strings.Join(args, " "), msg)
		}
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// Create adds a detached linked worktree at dir. git runs the repository's
// post-checkout hook as part of worktree add: this is the one full hook run
// of the slot's life.
func (w *Worktree) Create(ctx context.Context, dir, commit string) error {
	if _, err := w.git(ctx, "", "worktree", "add", "--detach", dir, commit); err != nil {
		return err
	}
	return nil
}

// gitDir returns the absolute per-worktree git directory.
func (w *Worktree) gitDir(ctx context.Context, dir string) (string, error) {
	return w.git(ctx, dir, "rev-parse", "--absolute-git-dir")
}

// lockPresent reports a leftover lock file in the slot's git directory or the
// shared one. The pool holds the slot exclusively, so no live git of ours owns
// it.
func (w *Worktree) lockPresent(ctx context.Context, dir string) error {
	gd, err := w.gitDir(ctx, dir)
	if err != nil {
		return err
	}
	common := filepath.Join(w.repo, ".git")
	for _, lock := range []string{
		filepath.Join(gd, "index.lock"), filepath.Join(gd, "HEAD.lock"),
		filepath.Join(common, "index.lock"), filepath.Join(common, "shallow.lock"),
	} {
		if _, err := os.Lstat(lock); err == nil {
			return fmt.Errorf("%w: %s", ErrStaleLock, lock)
		}
	}
	return nil
}

// Verify checks dir is a worktree of the configured repository with no stale
// lock.
func (w *Worktree) Verify(ctx context.Context, dir string) error {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("%w: %s missing", ErrUnhealthy, dir)
	}
	top, err := w.git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		if errors.Is(err, ErrWedged) {
			return err
		}
		return fmt.Errorf("%w: %v", ErrUnhealthy, err)
	}
	if !samePath(top, dir) {
		return fmt.Errorf("%w: toplevel %s is not %s", ErrUnhealthy, top, dir)
	}
	common, err := w.git(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	if !samePath(common, filepath.Join(w.repo, ".git")) {
		return fmt.Errorf("%w: belongs to %s, not %s", ErrUnhealthy, common, w.repo)
	}
	return w.lockPresent(ctx, dir)
}

func samePath(a, b string) bool {
	ra, ea := filepath.EvalSymlinks(a)
	rb, eb := filepath.EvalSymlinks(b)
	return ea == nil && eb == nil && ra == rb
}

// skip returns the per-command hook override unless hooks are wanted on reset.
// It is passed as a leading -c option and so is never written to any config.
func (w *Worktree) skip() []string {
	if w.hook {
		return nil
	}
	return []string{"-c", skipHooksPath}
}

func (w *Worktree) cleanArgs(dry bool) []string {
	a := append(w.skip(), "clean", "-ffdxq")
	if dry {
		a = append(w.skip(), "clean", "-ffdxn")
	}
	for _, k := range w.keep {
		a = append(a, "-e", "/"+k)
	}
	return a
}

// Reset checks out commit (as branch when given, forced, else detached),
// removes every untracked and ignored file outside the keep list, and proves
// the result: HEAD is commit and a dry-run clean finds nothing left.
func (w *Worktree) Reset(ctx context.Context, dir, commit, branch string) (string, error) {
	if err := w.lockPresent(ctx, dir); err != nil {
		return "", err
	}
	want, err := w.git(ctx, dir, "rev-parse", "--verify", "--end-of-options", commit+"^{commit}")
	if err != nil {
		return "", err
	}
	co := append(w.skip(), "checkout", "-q", "-f")
	if branch != "" {
		co = append(co, "-B", branch, want)
	} else {
		co = append(co, "--detach", want)
	}
	if _, err := w.git(ctx, dir, co...); err != nil {
		return "", err
	}
	if _, err := w.git(ctx, dir, w.cleanArgs(false)...); err != nil {
		return "", err
	}
	left, err := w.git(ctx, dir, w.cleanArgs(true)...)
	if err != nil {
		return "", err
	}
	if left != "" {
		return "", fmt.Errorf("%w: %s", ErrNotClean, strings.ReplaceAll(left, "\n", "; "))
	}
	head, err := w.git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if head != want {
		return "", fmt.Errorf("%w: HEAD %s, wanted %s", ErrUnhealthy, head, want)
	}
	return head, nil
}

// Detach detaches HEAD at its current commit, leaving the lease branch intact.
func (w *Worktree) Detach(ctx context.Context, dir string) error {
	_, err := w.git(ctx, dir, append(w.skip(), "checkout", "-q", "--detach")...)
	return err
}
