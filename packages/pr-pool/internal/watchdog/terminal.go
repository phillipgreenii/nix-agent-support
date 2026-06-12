package watchdog

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/pr-pool/internal/beads"
)

// terminal runs the 100% hard-stop sequence: 2nd cancel, guarded worktree reset,
// budget note, unclaim, eventlog. (Session close is done by the orchestrator's
// pass-level teardownAll, as in A.) Each step is best-effort.
func (w *Watchdog) terminal(ctx context.Context, sessionName, beadID string) {
	_ = w.CC.Cancel(ctx, sessionName) // 2nd cancel (idempotent/safe)

	wt := w.sessionCWD(ctx, sessionName)
	didReset := false
	if safeToReset(wt, w.RepoRoot, w.WorktreeDir) {
		if err := w.Git.Run(ctx, wt, "reset", "--hard"); err == nil {
			_ = w.Git.Run(ctx, wt, "clean", "-fd")
			didReset = true
		}
	}

	_ = beads.Comment(ctx, w.BD, beadID, "interrupted — budget")
	_ = beads.Unclaim(ctx, w.BD, beadID)
	w.emit("hard_stop", map[string]any{"session": sessionName, "bead": beadID, "worktree_reset": didReset, "worktree": wt})
}

func (w *Watchdog) sessionCWD(ctx context.Context, name string) string {
	sessions, err := w.CC.List(ctx)
	if err != nil {
		return ""
	}
	for _, s := range sessions {
		if s.Name == name {
			return s.CWD
		}
	}
	return ""
}

// safeToReset returns true only when path is a real git worktree root, distinct
// from repoRoot, inside worktreeDir. Symlink-resolved, boundary-checked (never a
// prefix-string match). On ANY uncertainty it returns false (no-op = safe).
func safeToReset(path, repoRoot, worktreeDir string) bool {
	if path == "" {
		return false
	}
	rp, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false // path doesn't exist -> safe no-op
	}
	rr, err := filepath.EvalSymlinks(repoRoot)
	if err == nil && rp == rr {
		return false // never the monorepo
	}
	wd, err := filepath.EvalSymlinks(worktreeDir)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(wd, rp)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false // outside worktreeDir
	}
	// backstop: must be a worktree ROOT (toplevel == path), not REPO_ROOT.
	tl, err := gitToplevel(rp)
	if err != nil || tl != rp {
		return false
	}
	return true
}

// gitToplevel returns `git -C path rev-parse --show-toplevel` (EvalSymlinks'd).
func gitToplevel(path string) (string, error) {
	out, err := execGit(path, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	tl := strings.TrimSpace(out)
	if resolved, err := filepath.EvalSymlinks(tl); err == nil {
		return resolved, nil
	}
	return tl, nil
}

func execGit(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return string(out), err
}

// OSGit is the production GitRunner — runs `git -C <dir> <args...>`.
type OSGit struct{}

func (OSGit) Run(ctx context.Context, dir string, args ...string) error {
	return exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Run()
}
