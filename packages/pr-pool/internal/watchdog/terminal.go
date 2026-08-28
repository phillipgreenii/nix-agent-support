package watchdog

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/gitenv"
	"github.com/phillipgreenii/x/gitclient"
)

// gitCallTimeout bounds each git probe/mutation this file issues through
// x/gitclient so a wedged git can neither hang the hard-stop sequence nor
// defeat ctx cancellation (pg2-yy42). gitclient's own Client.run already
// wraps ctx cancellation/deadlines explicitly and sets a WaitDelay bounding
// how long a killed child's inherited I/O pipes are waited on (design
// §4.4's Context contract); this per-call context.WithTimeout is this
// file's OWN outer bound layered on top of that. It replaces the retired
// execGit helper's identical bound for the read-only toplevel probe
// (safeToReset), and is newly applied to the mutating reset/clean calls in
// terminal, which previously had no bound of their own and relied solely
// on the caller's ambient ctx (pg2-ljyaj).
const gitCallTimeout = 10 * time.Second

// gitLocatorCleaner is the composite role this file needs from x/gitclient
// at a given anchor directory: Locator.Toplevel backs safeToReset's
// worktree-root backstop, and Cleaner.ResetHard/CleanUntracked back
// terminal's guarded hard-stop reset (design §4.5's consumer mapping: "pr-
// pool watchdog -> Cleaner + Locator (Toplevel inside safeToReset)").
type gitLocatorCleaner interface {
	gitclient.Locator
	gitclient.Cleaner
}

// gitOpener anchors an x/gitclient client at dir, sized to the widest
// role(s) this file needs (design §4.6's app-local opener seam for multi-
// directory consumers -- this watchdog probes arbitrary session paths, one
// client per hard-stop rather than a cached long-lived one).
type gitOpener func(ctx context.Context, dir string) (gitLocatorCleaner, error)

// openGit is a package-level var, not a plain function, so tests can
// substitute a client anchored via gitclient.WithGit at a script that
// deliberately blocks -- proving ctx cancellation/timeout is honored end to
// end -- without threading a new testing seam through Watchdog itself.
var openGit gitOpener = func(ctx context.Context, dir string) (gitLocatorCleaner, error) {
	return gitclient.New(ctx, dir)
}

// terminal runs the 100% hard-stop sequence: 2nd cancel, guarded worktree reset,
// budget note, unclaim, eventlog. (Session close is done by the orchestrator's
// pass-level teardownAll, as in A.) Each step is best-effort.
func (w *Watchdog) terminal(ctx context.Context, sessionName, beadID string) {
	_ = w.CC.Cancel(ctx, sessionName) // 2nd cancel (idempotent/safe)

	wt := w.sessionCWD(ctx, sessionName)
	didReset := false
	if safeToReset(ctx, wt, w.RepoRoot, w.WorktreeDir) {
		octx, ocancel := context.WithTimeout(ctx, gitCallTimeout)
		defer ocancel()
		if client, err := openGit(octx, wt); err == nil {
			rctx, rcancel := context.WithTimeout(ctx, gitCallTimeout)
			defer rcancel()
			if err := client.ResetHard(rctx); err == nil {
				cctx, ccancel := context.WithTimeout(ctx, gitCallTimeout)
				defer ccancel()
				_ = client.CleanUntracked(cctx)
				didReset = true
			}
		}
	}

	_ = beads.Comment(ctx, w.BD, beadID, "interrupted — budget")
	_ = beads.Unclaim(ctx, w.BD, beadID)
	w.emit("error", "hard_stop", "budget hard stop reached", map[string]any{
		"session": sessionName, "bead": beadID, "worktree_reset": didReset, "worktree": wt,
	})
}

func (w *Watchdog) sessionCWD(ctx context.Context, externalID string) string {
	sessions, err := w.CC.List(ctx)
	if err != nil {
		return ""
	}
	for _, s := range sessions {
		if s.ExternalID == externalID {
			return s.CWD
		}
	}
	return ""
}

// safeToReset returns true only when path is a real git worktree root, distinct
// from repoRoot, inside worktreeDir. Symlink-resolved, boundary-checked (never a
// prefix-string match). On ANY uncertainty it returns false (no-op = safe).
func safeToReset(ctx context.Context, path, repoRoot, worktreeDir string) bool {
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
	// x/gitclient's Locator role (Toplevel = `rev-parse --show-toplevel`)
	// backs this probe -- it replaces the retired execGit/gitToplevel
	// helpers (pg2-ljyaj) -- bounded by gitCallTimeout so a wedged git
	// can't hang this guard (pg2-yy42).
	cctx, cancel := context.WithTimeout(ctx, gitCallTimeout)
	defer cancel()
	client, err := openGit(cctx, rp)
	if err != nil {
		return false
	}
	tl, err := client.Toplevel(cctx)
	if err != nil {
		return false
	}
	if resolved, evalErr := filepath.EvalSymlinks(tl); evalErr == nil {
		tl = resolved
	}
	return tl == rp
}

// OSGit is the production GitRunner — runs `git -C <dir> <args...>`. This
// file's own hard-stop sequence (terminal/safeToReset above) no longer uses
// it: they migrated onto x/gitclient's Cleaner+Locator roles (bead pg2-
// ljyaj). OSGit remains here because internal/executor still injects it as
// the shared worktree-CREATION git seam (executor.Deps.git's nil fallback,
// which worktreeIsolation passes to internal/worktree.Ensure) — a separate,
// not-yet-landed migration (pg2-mj9n0, x/gitclient's WorktreeManager role).
// Retire this type only once that bead lands.
type OSGit struct{}

// Run runs `git -C <dir> <args...>` with a hermetic child environment (see
// this package's gitenv import) — retained for internal/executor's
// worktree-creation seam; see the OSGit doc comment above for why this
// file still defines it despite no longer calling it itself.
func (OSGit) Run(ctx context.Context, dir string, args ...string) error {
	return gitenv.Command(ctx, dir, args...).Run()
}
