// Package worktree assigns a fresh, isolated per-bead git worktree at dispatch so
// a pg-router worker never runs on whatever unrelated branch the monorepo happens
// to be on (pg2-yukh root cause #2). The worktree dir is <worktreeDir>/<beadID>
// on a dedicated branch pg-router/<beadID>; it is idempotent (an existing worktree
// for the bead is reused).
//
// Duplicated verbatim from packages/pg-router/internal/worktree (still needed
// there by its own orchestrator, which stays in-core): a leaf depending only
// on the external github.com/phillipgreenii/x/gitclient module, so a
// byte-for-byte copy costs nothing and neither side may import the other's
// internal/* (docket pg2-oju6w Task 5.2/5.3, folded per the operator's
// 2026-09-11 decision; docs/adr/0065's Addendum).
package worktree

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/x/gitclient"
)

// Opener anchors a gitclient at dir, returning the WorktreeManager role Ensure
// needs both to probe reuse and to create a fresh worktree. Production wires
// this to gitclient.New; tests substitute a fake so they never touch a real
// repo (design §4.6's app-local opener seam for multi-directory consumers —
// Ensure opens both the prospective worktree path and repoRoot).
type Opener func(ctx context.Context, dir string) (gitclient.WorktreeManager, error)

// ErrLowDisk is returned (wrapping the original failure) when Ensure's `git
// worktree add` failed because the filesystem had run out of room, rather
// than any other failure sharing that invocation's same generic exit code 128
// (bead pg2-8vn8t: the 2026-09-23 incident's repeated 'git worktree add ...
// exit 128: ... error: unable to write/create file' failures against a
// ~213k-file monorepo checkout, which pg-router surfaced as nothing more than
// an opaque handler-error). A caller checks errors.Is(err, ErrLowDisk) to
// treat this specific, transient, system-wide condition differently from a
// genuine per-bead defect — e.g. declining and retrying later rather than
// escalating the bead to a human (see pg-router-ccpool-handler's own
// ccpool.go, the one production caller of this duplicated package today).
var ErrLowDisk = errors.New("worktree: low disk space")

// diskFullMarkers are the case-insensitive stderr substrings known to
// accompany a disk-full git failure on the two platforms this pool actually
// runs on (Linux prod, Darwin dev): "no space left on device" is ENOSPC's own
// libc strerror text; "disk quota exceeded" is EDQUOT, which presents to a
// caller identically (the filesystem will not take more bytes either way).
//
// gitclient's own classify() deliberately never keys on stderr text (that
// package's errors.go doc: "text matching would be locale-fragile... MUST key
// on exit codes") — exit code 128 is shared by many unrelated fatal git
// conditions, so it alone cannot distinguish this case. This package accepts
// the locale caveat and scans stderr anyway, but ONLY for
// classification/logging, never for correctness-critical control flow: a
// false negative here (an unrecognized locale) simply returns the original,
// unlabeled error — exactly today's status quo — so this is a strict
// improvement with no downside risk.
var diskFullMarkers = []string{"no space left on device", "disk quota exceeded"}

// isDiskFull reports whether err's failed git invocation looks like it was
// caused by the filesystem running out of room, by scanning the wrapped
// *gitclient.GitError's own Stderr (never the generic ExitCode, per
// diskFullMarkers' own doc).
func isDiskFull(err error) bool {
	var ge *gitclient.GitError
	if !errors.As(err, &ge) {
		return false
	}
	lower := strings.ToLower(ge.Stderr)
	for _, marker := range diskFullMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// Ensure returns the path to a fresh per-bead worktree, creating it under
// worktreeDir off repoRoot's current HEAD on branch pg-router/<beadID>. If a
// worktree already exists at that path it is reused (idempotent) — detected via
// open(ctx, path)'s gitclient.ErrNotARepository sentinel rather than a
// `rev-parse --is-inside-work-tree` probe. The branch name and dir derive
// deterministically from beadID, so a redispatch reuses the same isolated
// workspace rather than the shared monorepo checkout.
//
// pg2-an65v: the probe must ALSO tolerate fs.ErrNotExist, not only
// gitclient.ErrNotARepository. gitclient.New's doc comment promises
// ErrNotARepository whenever dir is not inside a git repository, but its actual
// implementation resolves symlinks (filepath.EvalSymlinks) BEFORE ever running
// git, so a path that does not exist ON DISK yet — the normal case for a
// bead's worktree the very first time it is dispatched — fails at that
// resolution step with a plain wrapped "no such file or directory" error, never
// reaching the code that would wrap it as ErrNotARepository. Before this fix,
// Ensure treated that as a hard failure and never attempted `git worktree add`
// at all, so EVERY first-time dispatch failed here unconditionally (confirmed
// empirically against the real gitclient.New — the prior fix, a mocked Opener
// in this package's own tests, could not have caught it because it never
// exercised gitclient's real error shape). This was the actual cause of the
// 100%-failure-rate launches investigated in pg2-an65v; it reproduces
// regardless of whether pg-router itself runs from the canonical clone or a
// linked worktree, which refutes that bead's nested-worktree hypothesis as the
// mechanism (a fresh path fails identically either way).
func Ensure(ctx context.Context, open Opener, worktreeDir, repoRoot, beadID string) (string, error) {
	path := filepath.Join(worktreeDir, beadID)
	// Reuse: if the path is already a worktree root, keep it. A path that does
	// not exist yet is never an existing worktree — treat that the same as the
	// intended not-a-repository signal. Any OTHER error is a real failure (e.g.
	// a canceled context) and must propagate rather than be swallowed into an
	// attempted create.
	if _, err := open(ctx, path); err == nil {
		return path, nil
	} else if !errors.Is(err, gitclient.ErrNotARepository) && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("probe worktree %s: %w", path, err)
	}
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir worktree dir: %w", err)
	}
	branch := "pg-router/" + beadID
	wm, err := open(ctx, repoRoot)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", repoRoot, err)
	}
	// ResetBranch (-B) resets/creates the branch at HEAD; anchored at repoRoot so
	// the new worktree branches off the monorepo's current commit, then checks
	// out in isolation.
	if err := wm.CreateWorktree(ctx, path, branch, gitclient.CreateWorktreeOptions{ResetBranch: true}); err != nil {
		if isDiskFull(err) {
			return "", fmt.Errorf("worktree add %s: %w: %w", path, ErrLowDisk, err)
		}
		return "", fmt.Errorf("worktree add %s: %w", path, err)
	}
	return path, nil
}
