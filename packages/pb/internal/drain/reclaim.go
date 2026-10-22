package drain

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/pb/internal/run"
)

// Reclaim outcomes (Result.Outcome).
const (
	// OutcomeRemoved: the worktree held nothing (clean tree, zero commits ahead
	// of the primary branch), was removed with wtdone, and the bead got the
	// clean-up note.
	OutcomeRemoved = "removed"
	// OutcomeNone: no worktree has the work branch checked out, so there is
	// nothing to reclaim (idempotent re-run, or the bead was never isolated).
	OutcomeNone = "none"
	// OutcomeKeptDirty: `git status --porcelain` is non-empty; left untouched.
	OutcomeKeptDirty = "kept-dirty"
	// OutcomeKeptCommits: the branch has commits of its own; left untouched.
	OutcomeKeptCommits = "kept-commits"
)

// ErrNoteFailed: the worktree WAS removed but the bead note could not be
// written. The caller posts the note itself (Result.Note is the exact text).
var ErrNoteFailed = errors.New("worktree removed but the bead note failed")

// ReclaimParams selects the isolation to reclaim for ONE bead that a drain
// session is ending WITHOUT landing (park, defer, dependency wiring, release).
type ReclaimParams struct {
	RepoPath string // absolute canonical clone path
	BeadID   string
	// Branch is the work branch (default "drain/<BeadID>"). The worktree is
	// found by asking git which worktree has the branch checked out, so a
	// tracker section's own layout works too.
	Branch string
	// Actor is passed as --actor on the `bd comment` that records the note.
	Actor      string
	GitTimeout time.Duration // bounds EACH git call (0 = DefaultGitTimeout)
}

// ReclaimResult reports what Reclaim saw and did.
type ReclaimResult struct {
	Worktree string `json:"worktree,omitempty"`
	Branch   string `json:"branch"`
	Outcome  string `json:"outcome"`
	// Reason names the decisive fact for a kept outcome (the porcelain line
	// count, or the commit count ahead of the primary branch).
	Reason string `json:"reason,omitempty"`
	// Note is the text written to the bead; set only for OutcomeRemoved.
	Note string `json:"note,omitempty"`
}

// Reclaim removes the bead's isolation worktree when it holds nothing worth
// keeping, and records that on the bead. The worktree is reclaimed ONLY when
// BOTH legs hold at the instant they are read:
//
//  1. `git status --porcelain` in the worktree is empty (no modified, staged or
//     untracked file), and
//  2. the work branch has zero commits that the primary branch lacks
//     (`git rev-list --count <primary>..<branch>` is 0).
//
// Anything else leaves the worktree exactly as found: a dirty tree or a branch
// with commits is work a person or a later drain session resumes. Removal goes
// through `wtdone`, which refuses a worktree a live process is anchored in and
// never forces; any wtdone failure is returned as an error with the worktree
// left in place, and the bead gets NO note (the note states a removal that did
// not happen). A failed read (status or rev-list) is also an error, never a
// guess: the unattended caller cannot inherit a proof taken earlier (F-1).
//
// wtdone deletes the branch with a plain `git branch -d`, which is lossless for
// a zero-commit branch; `pb drain isolate` recreates it on resume.
func Reclaim(ctx context.Context, inner run.Runner, p ReclaimParams) (ReclaimResult, error) {
	timeout := p.GitTimeout
	if timeout <= 0 {
		timeout = DefaultGitTimeout
	}
	r := gitBound{inner: inner, timeout: timeout}
	repo, err := resolveRepo(p.RepoPath)
	if err != nil {
		return ReclaimResult{}, fmt.Errorf("%s is not a git repo: %w", p.RepoPath, err)
	}
	branch := p.Branch
	if branch == "" {
		branch = "drain/" + p.BeadID
	}
	res := ReclaimResult{Branch: branch}

	checkouts, err := worktreeBranches(ctx, r, repo)
	if err != nil {
		return res, err
	}
	for path, b := range checkouts {
		if b == "refs/heads/"+branch {
			res.Worktree = path
			break
		}
	}
	if res.Worktree == "" {
		res.Outcome = OutcomeNone
		return res, nil
	}
	if filepath.Clean(res.Worktree) == filepath.Clean(repo) {
		return res, fmt.Errorf("%s is the canonical clone itself, not an isolation worktree; refusing to remove it", repo)
	}

	status, err := r.Run(ctx, "git", []string{"-C", res.Worktree, "status", "--porcelain"}, run.Options{})
	if err != nil {
		return res, fmt.Errorf("cannot read the worktree state, leaving it in place: %w", err)
	}
	if dirty := strings.TrimSpace(status.Stdout); dirty != "" {
		res.Outcome = OutcomeKeptDirty
		res.Reason = fmt.Sprintf("%d changed path(s) in the worktree", len(strings.Split(dirty, "\n")))
		return res, nil
	}

	primary := primaryBranch(ctx, r, repo)
	count, err := r.Run(ctx, "git", []string{"-C", repo, "rev-list", "--count", "refs/heads/" + primary + ".." + "refs/heads/" + branch}, run.Options{})
	if err != nil {
		return res, fmt.Errorf("cannot count %s commits ahead of %s, leaving the worktree in place: %w", branch, primary, err)
	}
	ahead, err := strconv.Atoi(strings.TrimSpace(count.Stdout))
	if err != nil {
		return res, fmt.Errorf("unparsable rev-list count %q, leaving the worktree in place: %w", count.Stdout, err)
	}
	if ahead != 0 {
		res.Outcome = OutcomeKeptCommits
		res.Reason = fmt.Sprintf("%s has %d commit(s) not in %s", branch, ahead, primary)
		return res, nil
	}

	// wtdone, run from the canonical clone: its liveness guard cannot see the
	// calling shell, so a caller standing inside the worktree is refused here
	// rather than left with a deleted cwd. Not forced, ever.
	if _, err := r.Run(ctx, "wtdone", []string{branch, "--cc", repo}, run.Options{Dir: repo}); err != nil {
		return res, fmt.Errorf("wtdone did not remove %s (left in place): %w", res.Worktree, err)
	}

	res.Outcome = OutcomeRemoved
	res.Note = reclaimNote(res.Worktree, branch, primary, branchExists(ctx, r, repo, "refs/heads/"+branch))
	if _, err := r.Run(ctx, "bd", []string{"comment", p.BeadID, res.Note, "--actor", p.Actor}, run.Options{}); err != nil {
		return res, fmt.Errorf("%w: %v", ErrNoteFailed, err)
	}
	return res, nil
}

// reclaimNote is the bead comment recording the clean-up (operator ruling,
// Phillip, 2026-10-22: add a note that a clean-up pass ran and, since there
// were no changes, the worktree was removed).
func reclaimNote(wt, branch, primary string, branchKept bool) string {
	b := fmt.Sprintf("branch %s was deleted too (zero commits of its own, so nothing is lost; `pb drain isolate` recreates it off %s when the bead is resumed)", branch, primary)
	if branchKept {
		b = fmt.Sprintf("branch %s was kept; `pb drain isolate` reuses it when the bead is resumed", branch)
	}
	return fmt.Sprintf("drain clean-up pass: this bead ended without landing, and its isolation worktree %s had no changes (clean tree, zero commits ahead of %s), so the worktree was removed; %s.", wt, primary, b)
}
