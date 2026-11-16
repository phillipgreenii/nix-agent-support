package provision

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/phillipgreenii/pg-gwm/internal/gitrun"
)

// DefaultKeepList is the measured default for the monorepo: the flox
// environment, the direnv overrides, and the files the post-checkout hook
// writes (which a hook-skipping switch must not delete). Per-repository
// configuration replaces it wholesale.
var DefaultKeepList = []string{
	".flox",
	".envrc.local",
	".claude/settings.local.json",
	"CLAUDE.local.md",
	".vscode/settings.json",
}

// skipHooks is the per-command core.hooksPath override that disables every
// hook for one git invocation without writing configuration.
var skipHooks = []string{"-c", "core.hooksPath=/dev/null"}

// preserveSubject is the subject of every preservation commit (INV-GWM-24).
const preserveSubject = "adding dirty WIP"

// Worktree is the git-worktree Provisioner.
type Worktree struct {
	repo          string
	g             gitrun.Git
	gitTimeout    time.Duration
	createTimeout time.Duration
}

var _ Provisioner = (*Worktree)(nil)

// NewWorktree returns the provisioner for the repository whose main checkout
// is repoMain. Create is bounded by createTimeout, every other git call by
// gitTimeout.
func NewWorktree(repoMain string, g gitrun.Git, gitTimeout, createTimeout time.Duration) *Worktree {
	return &Worktree{repo: repoMain, g: g, gitTimeout: gitTimeout, createTimeout: createTimeout}
}

func (w *Worktree) git(ctx context.Context, dir string, args ...string) (string, error) {
	return w.g.Exec(ctx, dir, w.gitTimeout, args...)
}

func (w *Worktree) gitTrim(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := w.git(ctx, dir, args...)
	return strings.TrimSpace(out), err
}

// branchArgs renders b as the trailing arguments of git worktree add or git
// switch. add selects worktree add's shape (the directory goes before the
// commit).
func (b BranchSpec) args(dir string, add bool) []string {
	var flags, tail []string
	switch {
	case b.Detach:
		flags = []string{"--detach"}
		if add {
			tail = []string{dir}
		}
		if b.Commit != "" {
			tail = append(tail, b.Commit)
		}
		return append(flags, tail...)
	case b.Create, b.Reset:
		flag := "-b"
		if b.Reset {
			flag = "-B"
		}
		if add {
			flags = []string{flag, b.Branch}
			tail = []string{dir}
		} else {
			flag = map[string]string{"-b": "-c", "-B": "-C"}[flag]
			flags = []string{flag, b.Branch}
		}
		if b.Commit != "" {
			tail = append(tail, b.Commit)
		}
		return append(flags, tail...)
	default:
		if add {
			return []string{dir, b.Branch}
		}
		return []string{b.Branch}
	}
}

// Create adds a locked linked worktree at dir. git runs the repository's
// post-checkout hook as part of worktree add.
func (w *Worktree) Create(ctx context.Context, dir string, b BranchSpec, lockReason string) error {
	args := append([]string{"worktree", "add", "--lock", "--reason", lockReason}, b.args(dir, true)...)
	_, err := w.g.Exec(ctx, w.repo, w.createTimeout, args...)
	return err
}

// Switch moves dir to b. With runHook false the post-checkout hook is
// skipped for this command only.
func (w *Worktree) Switch(ctx context.Context, dir string, b BranchSpec, runHook bool) error {
	var args []string
	if !runHook {
		args = append(args, skipHooks...)
	}
	args = append(append(args, "switch"), b.args(dir, false)...)
	_, err := w.git(ctx, dir, args...)
	return err
}

// Clean resets tracked files, removes everything else outside keep, and
// proves nothing but keep paths remains.
func (w *Worktree) Clean(ctx context.Context, dir string, keep []string) error {
	keep, err := ValidateKeep(keep)
	if err != nil {
		return err
	}
	if _, err := w.git(ctx, dir, "reset", "--hard"); err != nil {
		return err
	}
	args := []string{"clean", "-ffdx"}
	for _, k := range keep {
		args = append(args, "-e", "/"+k)
	}
	if _, err := w.git(ctx, dir, args...); err != nil {
		return err
	}
	out, err := w.git(ctx, dir, "status", "--porcelain", "--ignored", "-z")
	if err != nil {
		return err
	}
	var left []string
	for _, rec := range strings.Split(out, "\x00") {
		if len(rec) < 4 {
			continue
		}
		path := rec[3:]
		if rec[:2] == "!!" && keepCovers(keep, strings.TrimSuffix(path, "/")) {
			continue
		}
		left = append(left, path)
	}
	if len(left) > 0 {
		return fmt.Errorf("%w: %s", ErrNotClean, strings.Join(left, "; "))
	}
	return nil
}

// keepCovers reports whether an ignored status path is a keep path, lies
// inside one, or is a directory that holds only keep paths.
func keepCovers(keep []string, path string) bool {
	for _, k := range keep {
		if path == k || strings.HasPrefix(path, k+"/") || strings.HasPrefix(k, path+"/") {
			return true
		}
	}
	return false
}

// Unsaved reports tracked changes, untracked files, unreachable commits and
// any operation in progress.
func (w *Worktree) Unsaved(ctx context.Context, dir string) (Unsaved, error) {
	var u Unsaved
	out, err := w.git(ctx, dir, "status", "--porcelain=v2", "-z")
	if err != nil {
		return u, err
	}
	recs := strings.Split(out, "\x00")
	files := map[string]bool{}
	var blockers []string
	for i := 0; i < len(recs); i++ {
		r := recs[i]
		if len(r) < 3 {
			continue
		}
		switch r[0] {
		case '?':
			files[r[2:]] = true
		case '1', '2', 'u':
			nf := map[byte]int{'1': 9, '2': 10, 'u': 11}[r[0]]
			f := strings.SplitN(r, " ", nf)
			if len(f) < nf {
				continue
			}
			files[f[nf-1]] = true
			if r[0] == '2' {
				i++ // the rename's original path
				if i < len(recs) {
					files[recs[i]] = true
				}
			}
			if sub := f[2]; len(sub) == 4 && sub[0] == 'S' && sub[1:] != "..." {
				blockers = append(blockers, "submodule change in "+f[nf-1])
			}
		}
	}
	for f := range files {
		u.Files = append(u.Files, f)
	}
	sort.Strings(u.Files)
	gd, err := w.gitTrim(ctx, dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return u, err
	}
	for _, c := range []struct{ name, why string }{
		{"MERGE_HEAD", "merge in progress"},
		{"rebase-merge", "rebase in progress"},
		{"rebase-apply", "rebase in progress"},
		{"CHERRY_PICK_HEAD", "cherry-pick in progress"},
	} {
		if _, err := os.Lstat(filepath.Join(gd, c.name)); err == nil {
			blockers = append(blockers, c.why)
		}
	}
	u.Blocker = strings.Join(dedupe(blockers), "; ")
	rl, err := w.gitTrim(ctx, dir, "rev-list", "HEAD", "--not", "--branches", "--tags")
	if err != nil {
		return u, err
	}
	if rl != "" {
		u.Unreachable = strings.Split(rl, "\n")
	}
	return u, nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// Preserve creates branch at the current head, adds every change (never
// ignored files) and commits it with the fixed subject and message as body.
// Hooks and signing are off and a missing identity gets a fallback author.
// A merge, rebase, cherry-pick or submodule change returns ErrPreserveBlocked
// before anything is touched.
func (w *Worktree) Preserve(ctx context.Context, dir, branch, message string) error {
	u, err := w.Unsaved(ctx, dir)
	if err != nil {
		return err
	}
	if u.Blocker != "" {
		return fmt.Errorf("%w: %s", ErrPreserveBlocked, u.Blocker)
	}
	if _, err := w.git(ctx, dir, "switch", "-c", branch); err != nil {
		return err
	}
	if _, err := w.git(ctx, dir, "add", "-A"); err != nil {
		return err
	}
	pending, err := w.gitTrim(ctx, dir, "status", "--porcelain")
	if err != nil {
		return err
	}
	if pending == "" {
		return nil
	}
	args := append([]string{}, skipHooks...)
	if _, ierr := w.git(ctx, dir, "var", "GIT_AUTHOR_IDENT"); ierr != nil {
		args = append(args, "-c", "user.name=pg-gwm", "-c", "user.email=pg-gwm@localhost")
	}
	args = append(args, "-c", "commit.gpgsign=false", "commit", "-m", preserveSubject, "-m", message)
	_, err = w.git(ctx, dir, args...)
	return err
}

// Unlock removes the lock git worktree add --lock took.
func (w *Worktree) Unlock(ctx context.Context, dir string) error {
	_, err := w.git(ctx, w.repo, "worktree", "unlock", dir)
	return err
}

// Remove deletes the worktree. A single --force still refuses a locked
// worktree, so the caller unlocks first.
func (w *Worktree) Remove(ctx context.Context, dir string) error {
	_, err := w.git(ctx, w.repo, "worktree", "remove", "--force", dir)
	return err
}

// Prune forgets worktrees whose directory no longer exists.
func (w *Worktree) Prune(ctx context.Context) error {
	_, err := w.git(ctx, w.repo, "worktree", "prune")
	return err
}

// StopFSMonitor stops the file-system monitor daemon of dir; one that is not
// running is success.
func (w *Worktree) StopFSMonitor(ctx context.Context, dir string) error {
	_, err := w.git(ctx, dir, "fsmonitor--daemon", "stop")
	var ge *gitrun.GitError
	if errors.As(err, &ge) && strings.Contains(ge.Stderr, "not running") {
		return nil
	}
	return err
}

// RemoveStaleIndexLock deletes dir's index.lock when it is zero bytes and
// older than olderThan, and reports whether it did. A non-empty or younger
// lock may belong to a live git and is kept.
func (w *Worktree) RemoveStaleIndexLock(dir string, olderThan time.Duration) (bool, error) {
	gd, err := worktreeGitDir(dir)
	if err != nil {
		return false, err
	}
	lock := filepath.Join(gd, "index.lock")
	fi, err := os.Lstat(lock)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if fi.Size() != 0 || time.Since(fi.ModTime()) < olderThan {
		return false, nil
	}
	if err := os.Remove(lock); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return true, nil
}

// worktreeGitDir resolves dir's own git directory from its .git entry: the
// directory itself, or the target of a "gitdir:" file.
func worktreeGitDir(dir string) (string, error) {
	dot := filepath.Join(dir, ".git")
	fi, err := os.Stat(dot)
	if err != nil {
		return "", err
	}
	if fi.IsDir() {
		return dot, nil
	}
	b, err := os.ReadFile(dot)
	if err != nil {
		return "", err
	}
	line, ok := strings.CutPrefix(strings.TrimSpace(string(bytes.SplitN(b, []byte("\n"), 2)[0])), "gitdir:")
	if !ok {
		return "", fmt.Errorf("%s: not a gitdir file", dot)
	}
	gd := strings.TrimSpace(line)
	if !filepath.IsAbs(gd) {
		gd = filepath.Join(dir, gd)
	}
	return gd, nil
}

// FreeBytes reports the bytes available to an unprivileged writer on the
// volume holding path.
func FreeBytes(path string) (uint64, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return 0, err
	}
	return uint64(s.Bavail) * uint64(s.Bsize), nil //nolint:gosec // block counts are non-negative
}
