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
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

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

// ErrNoWorktreeDir is returned by Ensure when worktreeDir is empty (the role
// config / Config.WorktreeDir was never set). pg2-89dkcs: before this guard
// the empty dir surfaced as the cryptic "mkdir worktree dir: mkdir : no such
// file or directory" (os.MkdirAll("") always fails) and, being a genuine
// failed dispatch (no worktree, so the session never starts), repeated
// thousands of times with no hint at the cause.
var ErrNoWorktreeDir = errors.New("worktree: worktree dir is not configured (Config.WorktreeDir is empty)")

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
//
// pg2-mmgk2: when the probe says path is not a worktree but a directory is
// there anyway, and that directory is provably only a flox-executive husk
// (exactly .flox/log/<regular files>), it is removed before the create so the
// husk cannot make `git worktree add` fail "already exists" -- see reapFloxHusk
// for the strict shape check and why this lives here rather than only in
// cleanup.
func Ensure(ctx context.Context, open Opener, worktreeDir, repoRoot, beadID string) (string, error) {
	if worktreeDir == "" {
		return "", fmt.Errorf("isolation for %s: %w", beadID, ErrNoWorktreeDir)
	}
	path := filepath.Join(worktreeDir, beadID)
	// resumeBranch is set when an interrupted checkout held unique commits: the
	// recreated worktree then starts at the branch's own tip, not repoRoot's HEAD.
	resumeBranch := false
	// Reuse: if the path is already a worktree root, keep it. A path that does
	// not exist yet is never an existing worktree — treat that the same as the
	// intended not-a-repository signal. Any OTHER error is a real failure (e.g.
	// a canceled context) and must propagate rather than be swallowed into an
	// attempted create.
	if reuse, err := open(ctx, path); err == nil {
		// pg2-f6lxmp: a worktree whose admin dir holds a stale zero-byte index.lock
		// is a killed checkout; reusing it hands the session a half-written tree.
		admin, ok := adminDir(path)
		if !ok || !HasStaleIndexLock(admin, time.Now(), StaleIndexLockAge) {
			return path, nil
		}
		keep, err := discardInterrupted(ctx, open, reuse, path, worktreeDir, repoRoot, beadID)
		if err != nil {
			return "", err
		}
		resumeBranch = keep
		// fall through: create afresh
	} else if !errors.Is(err, gitclient.ErrNotARepository) && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("probe worktree %s: %w", path, err)
	}
	// pg2-mmgk2: a prior worktree removal can leave a flox-executive husk at
	// path (see reapFloxHusk); clear it so `git worktree add` does not fail
	// with "already exists". Strictly guarded; anything else is left alone and
	// git reports its usual error.
	if reaped, err := reapFloxHusk(path); err != nil {
		slog.Warn("worktree: flox husk found but not fully removed (git worktree add will report the conflict)",
			"path", path, "err", err)
	} else if reaped {
		slog.Info("worktree: removed flox-executive husk blocking worktree add", "path", path)
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
	// CreateWorktree streams (returns a *gitclient.Handle once git has started);
	// wait for it here, buffered, so a failure surfaces exactly as before. A nil
	// Handle (a test fake that does no real git work) has nothing to wait on.
	createOpts := gitclient.CreateWorktreeOptions{ResetBranch: true}
	if resumeBranch {
		// -B <branch> <path> <branch>: re-attach to the branch's current tip, so
		// its unique commits survive.
		createOpts.StartPoint = branch
	}
	h, err := wm.CreateWorktree(ctx, path, branch, createOpts)
	if err == nil && h != nil {
		err = h.Wait()
	}
	if err != nil {
		if isDiskFull(err) {
			return "", fmt.Errorf("worktree add %s: %w: %w", path, ErrLowDisk, err)
		}
		return "", fmt.Errorf("worktree add %s: %w", path, err)
	}
	return path, nil
}

// reapFloxHusk removes a leftover "husk" directory at path and reports whether
// it did (pg2-mmgk2). The husk is what a flox `executive` process leaves behind
// when it outlives its worktree: cleanupWorktree removes the worktree while the
// session's flox-activations executive is still running, and the executive
// then recreates path/.flox/log/executive.<pid>.log.<date> under the removed
// path. The resulting directory is not a git worktree, yet makes the next
// `git worktree add` for the same bead fail with "already exists", which the
// handler escalates to a human.
//
// Deletion is deliberately paranoid, because this runs on a path derived from a
// bead id under the operator's worktree directory. The directory is removed
// ONLY when its entire tree is exactly:
//
//	path/            (a real directory, not a symlink)
//	  .flox/         (a real directory)
//	    log/         (a real directory)
//	      <regular files only>
//
// Any other entry at any level (a .git file, a source file, a symlink anywhere,
// a nested directory under log, a non-regular file) makes it return (false,
// nil) and deletes NOTHING. Nothing is followed through symlinks (Lstat
// throughout). Removal is by non-recursive os.Remove, deepest first, so a file
// that appears after the check makes the parent removal fail instead of being
// deleted. A path that does not exist, or an empty directory (which git
// worktree add accepts), is left untouched.
//
// Callers invoke this only after the probe established path is not a git
// worktree, so a registered live worktree (which carries a .git entry and so
// also fails the shape check) is never a candidate.
func reapFloxHusk(path string) (bool, error) {
	if !isFloxHusk(path) {
		return false, nil
	}
	logDir := filepath.Join(path, ".flox", "log")
	entries, err := os.ReadDir(logDir)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if err := os.Remove(filepath.Join(logDir, e.Name())); err != nil {
			return false, err
		}
	}
	for _, d := range []string{logDir, filepath.Join(path, ".flox"), path} {
		if err := os.Remove(d); err != nil {
			return false, err
		}
	}
	return true, nil
}

// isFloxHusk reports whether path matches the exact husk shape documented on
// reapFloxHusk. Read-only.
func isFloxHusk(path string) bool {
	if !isRealDir(path) {
		return false
	}
	top, err := os.ReadDir(path)
	if err != nil || len(top) != 1 || top[0].Name() != ".flox" {
		return false
	}
	floxDir := filepath.Join(path, ".flox")
	if !isRealDir(floxDir) {
		return false
	}
	mid, err := os.ReadDir(floxDir)
	if err != nil || len(mid) != 1 || mid[0].Name() != "log" {
		return false
	}
	logDir := filepath.Join(floxDir, "log")
	if !isRealDir(logDir) {
		return false
	}
	files, err := os.ReadDir(logDir)
	if err != nil {
		return false
	}
	for _, f := range files {
		info, err := os.Lstat(filepath.Join(logDir, f.Name()))
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}

// isRealDir reports whether p exists and is itself a directory (Lstat: a
// symlink to a directory is NOT one).
func isRealDir(p string) bool {
	info, err := os.Lstat(p)
	return err == nil && info.IsDir()
}

// StaleIndexLockAge is how old a zero-byte index.lock must be before it counts as
// the residue of a killed checkout rather than a git command still running.
const StaleIndexLockAge = 10 * time.Minute

// ErrInterruptedCheckout is returned by Ensure when an existing bead worktree is
// an interrupted checkout and the repo cannot say whether its branch holds
// unique commits (no ref reader, or the commit count failed), so it is neither
// reused nor discarded. A branch KNOWN to hold unique commits is recovered, not
// refused (see discardInterrupted).
var ErrInterruptedCheckout = errors.New("worktree: interrupted checkout (stale index.lock) with unique commits")

// HasStaleIndexLock reports whether admin (a worktree's git admin directory)
// holds a zero-byte index.lock last modified at least minAge before now. A
// non-empty lock, or one too young, is treated as possibly live.
func HasStaleIndexLock(admin string, now time.Time, minAge time.Duration) bool {
	info, err := os.Lstat(filepath.Join(admin, "index.lock"))
	if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
		return false
	}
	return now.Sub(info.ModTime()) >= minAge
}

// adminDir resolves a linked worktree's admin directory from its .git file.
func adminDir(path string) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(path, ".git"))
	if err != nil {
		return "", false
	}
	line := strings.TrimSpace(string(raw))
	const prefix = "gitdir:"
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	admin := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if !filepath.IsAbs(admin) {
		admin = filepath.Join(path, admin)
	}
	return filepath.Clean(admin), true
}

// discardInterrupted clears an interrupted-checkout worktree so Ensure can
// recreate it. When repoRoot's HEAD lacks nothing the branch holds, the worktree
// is force-removed and the caller recreates it at HEAD (returns false). When the
// branch holds unique commits they live in the branch ref, so the directory is
// NOT deleted: it is renamed into a sibling "<worktreeDir>-interrupted" holding
// area (keeping any uncommitted edits recoverable), its stale registration is
// pruned, and the caller re-attaches the worktree to the branch's own tip
// (returns true). Refusing outright, as before, made every later dispatch of the
// bead fail the same way forever. ErrInterruptedCheckout is now returned only
// when uniqueness cannot be determined at all.
func discardInterrupted(ctx context.Context, open Opener, wt gitclient.WorktreeManager, path, worktreeDir, repoRoot, beadID string) (bool, error) {
	branch := "pg-router/" + beadID
	root, err := open(ctx, repoRoot)
	if err != nil {
		return false, fmt.Errorf("open %s: %w", repoRoot, err)
	}
	refs, ok := root.(gitclient.RefReader)
	if !ok {
		return false, fmt.Errorf("%s: %w", path, ErrInterruptedCheckout)
	}
	ahead, err := refs.CommitsAhead(ctx, "HEAD", branch)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, ErrInterruptedCheckout)
	}
	if ahead > 0 {
		aside := filepath.Join(worktreeDir+"-interrupted", fmt.Sprintf("%s-%d", beadID, time.Now().UnixNano()))
		if err := os.MkdirAll(filepath.Dir(aside), 0o755); err != nil {
			return false, fmt.Errorf("mkdir holding dir for %s: %w", path, err)
		}
		slog.Warn("worktree: moving interrupted checkout with unique commits aside and re-attaching its branch",
			"path", path, "aside", aside, "branch", branch, "unique_commits", ahead)
		if err := os.Rename(path, aside); err != nil {
			return false, fmt.Errorf("move interrupted worktree %s aside: %w", path, err)
		}
		if err := root.PruneWorktrees(ctx); err != nil {
			return false, fmt.Errorf("prune after moving %s aside: %w", path, err)
		}
		return true, nil
	}
	slog.Warn("worktree: discarding interrupted checkout (stale index.lock)", "path", path)
	if err := wt.RemoveWorktree(ctx, path, true); err != nil {
		return false, fmt.Errorf("remove interrupted worktree %s: %w", path, err)
	}
	return false, nil
}
