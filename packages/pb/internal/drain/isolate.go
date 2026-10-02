// Package drain implements /drain-beads isolation: one call that creates (or
// reuses) a bead's worktree on its drain/<id> branch and links the canonical
// clone's nix-generated pre-commit config into it (the config is a gitignored
// symlink — absent from fresh worktrees, so commits there would abort;
// phillipg-nix-repo-base ADR 0016).
package drain

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/pb/internal/run"
)

// ErrConflict: the isolation state on disk contradicts the request (the
// worktree path holds another branch, or drain/<id> is checked out elsewhere).
// Never resolved by force — the caller routes the bead to STUCK. CLI exit 3.
var ErrConflict = errors.New("conflicting isolation state")

type Params struct {
	RepoPath string // absolute canonical clone path
	BeadID   string
}

type Result struct {
	Worktree  string `json:"worktree"`
	Branch    string `json:"branch"`
	Reused    string `json:"reused"`    // none | worktree | branch
	Precommit string `json:"precommit"` // linked | present | none
	// Warning is a non-fatal, read-only diagnosis of the canonical clone
	// (core.worktree set in its .git/config, or git's toplevel disagreeing
	// with --repo). Empty when the canonical clone is healthy. The bead is
	// still isolated; the warning exists so the orchestrator is not first
	// told at LAND time by a phantom "dirty canonical" halt (pg2-4c4nv).
	Warning string `json:"warning,omitempty"`
}

func Isolate(ctx context.Context, r run.Runner, p Params) (Result, error) {
	// Resolve the repo root ourselves from the CALLER-supplied path rather
	// than trust `git -C p.RepoPath rev-parse --show-toplevel`'s stdout for
	// it. That command reads core.worktree off .git/config, and when the
	// CANONICAL clone's own config has a corrupted core.worktree pointing
	// at some OTHER existing worktree's path (an unrelated bug — a
	// git-fixture test-isolation escape elsewhere in this repo, tracked as
	// pg2-5ek6b/pg2-12795 — can leave it that way), --show-toplevel
	// silently reports that OTHER worktree's path instead of p.RepoPath's
	// own. Isolate would then join .worktrees/<bead> onto the WRONG
	// directory — exit 0, no error, a new worktree silently nested inside
	// an unrelated one (observed 2026-08-27, pg2-x4e06). --repo is
	// documented (cmd/pb's drain isolate) as "the canonical clone", i.e.
	// already the toplevel, so the caller-supplied path is the source of
	// truth; git is still asked below to CONFIRM it's a git repo, but its
	// opinion of the toplevel path is never used.
	repo, err := resolveRepo(p.RepoPath)
	if err != nil {
		return Result{}, fmt.Errorf("%s is not a git repo: %w", p.RepoPath, err)
	}
	top, err := r.Run(ctx, "git", []string{"-C", repo, "rev-parse", "--show-toplevel"}, run.Options{})
	if err != nil {
		return Result{}, fmt.Errorf("%s is not a git repo: %w", p.RepoPath, err)
	}
	branch := "drain/" + p.BeadID
	ref := "refs/heads/" + branch
	wt := filepath.Join(repo, ".worktrees", p.BeadID)
	res := Result{Worktree: wt, Branch: branch}
	res.Warning = diagnoseCanonical(ctx, r, repo, strings.TrimSpace(top.Stdout))

	checkouts, err := worktreeBranches(ctx, r, repo)
	if err != nil {
		return Result{}, err
	}

	if got, registered := checkouts[wt]; registered {
		if got != ref {
			return Result{}, fmt.Errorf("%w: %s has %s checked out, expected %s",
				ErrConflict, wt, got, ref)
		}
		if _, statErr := os.Stat(wt); statErr == nil {
			res.Reused = "worktree"
		} else {
			// Stale registration: the directory was deleted without
			// `git worktree remove`. Prune it and recreate below.
			if _, err := r.Run(ctx, "git", []string{"-C", repo, "worktree", "prune"}, run.Options{}); err != nil {
				return Result{}, err
			}
			delete(checkouts, wt)
		}
	}

	if res.Reused != "worktree" {
		// A path that exists but is not registered on our branch is occupied by
		// something else — a plain directory, or a detached-HEAD worktree (which
		// has no branch line in the porcelain output). Never force it.
		if _, lstatErr := os.Lstat(wt); lstatErr == nil {
			return Result{}, fmt.Errorf("%w: %s exists but does not hold %s", ErrConflict, wt, ref)
		}
		for path, b := range checkouts {
			if b == ref {
				return Result{}, fmt.Errorf("%w: branch %s is already checked out at %s",
					ErrConflict, branch, path)
			}
		}
		if branchExists(ctx, r, repo, ref) {
			if _, err := r.Run(ctx, "git", []string{"-C", repo, "worktree", "add", wt, branch}, run.Options{}); err != nil {
				return Result{}, err
			}
			res.Reused = "branch"
		} else {
			primary := primaryBranch(ctx, r, repo)
			if _, err := r.Run(ctx, "git", []string{"-C", repo, "worktree", "add", wt, "-b", branch, primary}, run.Options{}); err != nil {
				return Result{}, err
			}
			res.Reused = "none"
		}
	}

	pc, err := linkPrecommitConfig(repo, wt)
	if err != nil {
		return Result{}, err
	}
	res.Precommit = pc
	return res, nil
}

// diagnoseCanonical returns a one-line, human-readable diagnosis when the
// canonical clone's git state is lying about where its working tree is, or ""
// when it is healthy. gitTop is `git -C repo rev-parse --show-toplevel`'s
// stdout (what git CLAIMS the toplevel is).
//
// The cause pg2-4c4nv / pg2-yj06c observed is a stray core.worktree in the
// canonical clone's own .git/config: git then reports ANOTHER worktree's path
// as the toplevel, so `git status` there lists that worktree's files as
// untracked and the lander halts at FF-0a on a phantom dirty tree. Naming
// core.worktree outright turns that into a one-step diagnosis.
//
// Strictly READ-ONLY (R-3): `git config --local --get` only reads; this never
// unsets the key — clearing it is the operator's call.
func diagnoseCanonical(ctx context.Context, r run.Runner, repo, gitTop string) string {
	// Exit 1 from `git config --get` means "key not set" — the healthy case.
	var coreWT string
	if out, err := r.Run(ctx, "git", []string{"-C", repo, "config", "--local", "--get", "core.worktree"}, run.Options{}); err == nil {
		coreWT = strings.TrimSpace(out.Stdout)
	}
	// Compare in symlink-free space, like resolveRepo (macOS /var → /private/var).
	topMismatch := false
	if gitTop != "" {
		if resolved, err := filepath.EvalSymlinks(gitTop); err == nil {
			gitTop = resolved
		}
		topMismatch = gitTop != repo
	}
	switch {
	case coreWT != "":
		return fmt.Sprintf("core.worktree set in canonical config (%s/.git/config): core.worktree=%s, "+
			"git rev-parse --show-toplevel reports %q, expected %q. git will lie about this clone "+
			"(phantom untracked files; the lander halts at FF-0a on a phantom dirty tree). "+
			"Read-only diagnosis, not cleared (R-3): the operator clears it with "+
			"`git config --file %s/.git/config --unset core.worktree`",
			repo, coreWT, gitTop, repo, repo)
	case topMismatch:
		return fmt.Sprintf("git rev-parse --show-toplevel reports %q, expected canonical root %q "+
			"(no core.worktree in the canonical .git/config; check GIT_WORK_TREE/GIT_DIR in the "+
			"environment, or whether --repo is a subdirectory)", gitTop, repo)
	}
	return ""
}

// resolveRepo resolves the caller-supplied repo path to an absolute,
// symlink-free form — matching how `git worktree list --porcelain` reports
// paths (macOS /var → /private/var) — WITHOUT asking git for its own
// opinion of the toplevel. See Isolate's comment for why: trusting
// `git rev-parse --show-toplevel` here is exactly the corruption vector
// this function exists to avoid.
func resolveRepo(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// primaryBranch resolves the integration branch exactly as the R-rules do:
// pgii-integrate-branch.primaryBranch → origin/HEAD → "main".
func primaryBranch(ctx context.Context, r run.Runner, repo string) string {
	if out, err := r.Run(ctx, "git", []string{"-C", repo, "config", "pgii-integrate-branch.primaryBranch"}, run.Options{}); err == nil {
		if b := strings.TrimSpace(out.Stdout); b != "" {
			return b
		}
	}
	if out, err := r.Run(ctx, "git", []string{"-C", repo, "symbolic-ref", "refs/remotes/origin/HEAD"}, run.Options{}); err == nil {
		if b := strings.TrimPrefix(strings.TrimSpace(out.Stdout), "refs/remotes/origin/"); b != "" {
			return b
		}
	}
	return "main"
}

func branchExists(ctx context.Context, r run.Runner, repo, ref string) bool {
	_, err := r.Run(ctx, "git", []string{"-C", repo, "rev-parse", "--verify", "--quiet", ref}, run.Options{})
	return err == nil
}

// worktreeBranches parses `git worktree list --porcelain` into path → branch
// ref (detached-HEAD worktrees are omitted; they have no branch line).
func worktreeBranches(ctx context.Context, r run.Runner, repo string) (map[string]string, error) {
	out, err := r.Run(ctx, "git", []string{"-C", repo, "worktree", "list", "--porcelain"}, run.Options{})
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	var current string
	for _, line := range strings.Split(out.Stdout, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			current = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch ") && current != "":
			m[current] = strings.TrimPrefix(line, "branch ")
		}
	}
	return m, nil
}

// linkPrecommitConfig links the CANONICAL clone's .pre-commit-config.yaml PATH
// (itself a gitignored symlink into the nix store) into the worktree — a
// symlink-to-symlink, deliberately NOT the resolved store target, so a later
// `nix run .#install-pre-commit-hooks` in the canonical clone propagates to
// long-lived worktrees instead of pinning a stale hook generation. Returns
// linked | present | none.
func linkPrecommitConfig(repo, wt string) (string, error) {
	dst := filepath.Join(wt, ".pre-commit-config.yaml")
	if _, err := os.Lstat(dst); err == nil {
		if _, err := os.Stat(dst); err == nil {
			return "present", nil
		}
		// A DANGLING link would read as "present" while prek fails on it —
		// exactly the failure this verb exists to prevent. Re-point it.
		if err := os.Remove(dst); err != nil {
			return "", fmt.Errorf("remove dangling pre-commit link: %w", err)
		}
	}
	src := filepath.Join(repo, ".pre-commit-config.yaml")
	if _, err := os.Lstat(src); err != nil {
		return "none", nil // canonical has no config; nothing to link
	}
	if err := os.Symlink(src, dst); err != nil {
		return "", fmt.Errorf("link pre-commit config into worktree: %w", err)
	}
	return "linked", nil
}
