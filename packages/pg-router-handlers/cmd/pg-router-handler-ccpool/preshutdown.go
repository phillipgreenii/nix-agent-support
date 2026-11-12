package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log/slog"
	"os"

	"github.com/phillipgreenii/pg-router-handlers/internal/beads"
	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/rolefile"
	"github.com/phillipgreenii/pg-router-handlers/internal/wire"
	"github.com/phillipgreenii/pg-router-handlers/internal/worktree"
	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/schemas"
	"github.com/phillipgreenii/x/gitclient"
)

// runPreShutdown implements the `preShutdown` INTF-HANDLER subcommand. It
// answers at once and touches nothing: no ccpool call, no bd call, no git
// call, no launch config read, no spawned process.
//
// Core calls it at daemon shutdown, once per distinct handler. It used to run
// a global sweep that listed every session, looked up beads and closed
// sessions and worktrees, which was the largest fixed cost of a stop with
// nothing in flight and ate the launchd ExitTimeOut budget. Nothing is lost by
// dropping it: the same passes (the purge_pending retry, the closed-bead
// reconcile, the default-pool stale pass, the orphan reconcile) run on the
// scheduled janitor (INV-CCH-41) and on dispatch, and a session spared at
// shutdown is already outlived by the daemon. A detached janitor run from here
// was rejected: the janitor is single-flight and scheduled, so a spawn would
// only race the next scheduled run, and a child that outlives the daemon is
// the shape the shutdown budget exists to avoid.
//
// The request still carries the role object like every other request and is
// rejected without a usable one; the role is not decoded.
func runPreShutdown(args []string) int {
	fs := flag.NewFlagSet("preShutdown", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	// --config is still accepted (the core passes the handler's launch flags to
	// every subcommand) but is not read.
	_ = fs.String("config", os.Getenv(rolefile.EnvConfig), "unused: preShutdown reads no launch config")
	if exit, done := parseFlags("preShutdown", fs, args); done {
		return exit
	}
	return servePreShutdown(os.Stdin, os.Stdout)
}

// servePreShutdown is runPreShutdown's testable core. A request that fails the
// schema or has no usable role gets an error reply; any other request gets an
// ok reply with no side effect.
func servePreShutdown(stdin io.Reader, stdout io.Writer) int {
	raw, _, exit, ok := readRequest("preShutdown", "handler.preShutdown", stdin, stdout)
	if !ok {
		return exit
	}
	var req wire.LifecycleRequest
	_ = json.Unmarshal(raw, &req)
	slog.Info("preShutdown: nothing to do; session cleanup runs on the janitor")
	wire.WriteReply(stdout, map[string]any{
		"schemaVersion": schemas.SchemaVersion,
		"id":            req.ID,
		"outcome":       "ok",
	})
	return conformance.ExitOK
}

// closeSessionAndWorktree tears s down: for a per-bead linked worktree it runs
// the two-phase teardown (closeSession's doc, INV-CCH-20: non-purge close,
// worktree removal, THEN purge); for any other CWD it purges via
// cc.Close(purge=true) and then
// best-effort removes s's own working directory (ccpool.Session.CWD) as a
// linked git worktree via open/gitclient.WorktreeManager.RemoveWorktree —
// and, once that removal actually succeeds, deletes s's own
// pg-router/<beadID> anchor branch (deleteAnchorBranch below). This must
// fail SOFT (log + continue), never abort the caller's sweep or be promoted
// to a nonzero overall exit: both callers — the removed shutdown sweep's
// once-per-shutdown sweep and reconcile.go's periodic
// reconcileClosedBeadSessions — sweep sessions regardless of which
// IsolationConfig.Type (internal/executor/isolation.go) launched them, so
// s.CWD is not always a registered linked worktree of any repository — for
// "none" isolation it's RepoRoot itself (git refuses to remove a repo's main
// working tree), and for "path"/"workforest" isolation it's an unrelated
// directory that may not even be inside a git repository at all. Either
// failure shape (open erroring because cwd isn't inside any repo, or
// RemoveWorktree itself erroring because it isn't a registered linked
// worktree of the repo it IS inside) is expected and equally harmless — same
// fail-soft posture cc.Close's own error handling below already has. Since
// the branch delete only ever runs after a successful RemoveWorktree, it
// naturally never fires for either of those isolation types either — only a
// genuine "worktree" isolation session ever has a pg-router/<beadID> anchor
// branch to delete in the first place.
// Single-phase: returns true iff cc.Close succeeded (the session was actually
// purged); a worktree-removal or branch-delete failure never changes that.
// Two-phase: returns true iff the row was purged, which requires the worktree to
// have been removed or confirmed gone; a failed removal returns false and leaves
// the row, marked, for the next reconcile (the one deliberate difference).
//
// The branch-delete step is bead pg2-ci75j's own fix, applied here at this
// THIRD call site by pg2-tpa18: ci75j's own acceptance criterion named only
// internal/executor.cleanupWorktree and the nix mkWorktreeSweepScript,
// leaving this preShutdown/reconcile call site explicitly out of scope —
// x/gitclient's RemoveWorktree only ever ran `git worktree remove`, never
// touching the branch it was created on, so every session purged through
// this function orphaned its pg-router/<beadID> anchor branch forever until
// now.
func closeSessionAndWorktree(ctx context.Context, cc ccpool.Runner, open worktree.Opener, repoRoot, worktreeDir string, s ccpool.Session) bool {
	return closeSession(ctx, cc, open, repoRoot, worktreeDir, s, false)
}

// closeSession is closeSessionAndWorktree with an explicit keepWorktree
// switch (pg2-u3t04): when true the session is still purged but its
// worktree and anchor branch are left in place because another live session
// still uses the same directory.
//
// A per-bead linked worktree (s.CWD under worktreeDir and not the repo root,
// twoPhaseEligible) is torn down in TWO phases (bead pg2-kqegi, INV-CCH-20):
// purge_pending marker -> non-purge close -> worktree removal (from the repo
// root) -> anchor-branch delete -> purge. The purge is last so an interruption
// (daemon-restart drain timeout, a crash, a slow removal of a huge checkout)
// leaves a row the next reconcile can find (retryPurgePending); purging first
// stranded a half-removed worktree no row led to (the original pg2-me0t1
// leak). Every other CWD ("none"/"path"/"workforest" isolation, an empty
// worktreeDir) and keepWorktree=true keep the single-phase purge with no
// marker.
func closeSession(ctx context.Context, cc ccpool.Runner, open worktree.Opener, repoRoot, worktreeDir string, s ccpool.Session, keepWorktree bool) bool {
	// A "bead-worktree" session's directory is a drain worker's worktree this
	// handler never created (bead pg2-b49ydt, R3): purge the row, remove nothing.
	// The type travels on the row (ccpool.MetaKeyKeepWorktree) because this
	// teardown runs for sessions of every role. closeSinglePhase repeats the
	// check so no caller can reach the force-remove around it.
	keepWorktree = keepWorktree || s.KeepsWorktree()
	// A pooled checkout is always torn down in two phases, whatever its path: the
	// lease release stands where the worktree removal stands, and the row is
	// purged only once the pool has the checkout back (finishPurge).
	if keepWorktree || (!twoPhaseEligible(s, repoRoot, worktreeDir) && s.CheckoutLease() == "") {
		return closeSinglePhase(ctx, cc, open, repoRoot, s, keepWorktree)
	}
	// The marker goes on BEFORE the non-purge close: a handler-closed idle row
	// WITHOUT it is a settled duplicate that a redelivered dispatch absorbs
	// (INV-CCH-17), which would be wrong for a row about to be discarded. If the
	// marker cannot be written, fall back to today's ordering.
	if err := cc.SetMeta(ctx, s.ExternalID, ccpool.MetaKeyPurgePending, purgeMarkerValue); err != nil {
		slog.Warn("teardown: could not mark the row purge_pending; falling back to single-phase purge",
			"session", s.ExternalID, "cwd", s.CWD, "err", err)
		return closeSinglePhase(ctx, cc, open, repoRoot, s, keepWorktree)
	}
	if err := cc.Close(ctx, s.ExternalID, false); err != nil {
		// The row stays open and marked; the next reconcile retries it.
		slog.Warn("teardown: close failed", "session", s.ExternalID, "err", err)
		return false
	}
	slog.Info("teardown: purge deferred until worktree removed (purge_pending)",
		"session", s.ExternalID, "cwd", s.CWD)
	return finishPurge(ctx, cc, open, repoRoot, worktreeDir, s, false)
}

// closeSinglePhase is the pre-pg2-kqegi teardown: purge the row first, then
// best-effort remove the worktree (unless keepWorktree) and its anchor branch.
func closeSinglePhase(ctx context.Context, cc ccpool.Runner, open worktree.Opener, repoRoot string, s ccpool.Session, keepWorktree bool) bool {
	if err := cc.Close(ctx, s.ExternalID, true); err != nil {
		slog.Warn("teardown: close failed", "session", s.ExternalID, "err", err)
		return false
	}
	if s.CheckoutLease() != "" {
		// Reached only when the two-phase marker could not be written: the row is
		// already purged, so the lease is released best effort; the pool's expiry
		// reaper covers a failed release.
		releaseSessionLease(ctx, teardownPoolClient(s.Meta[ccpool.MetaKeyCheckoutRoot]), s)
		return true
	}
	if s.KeepsWorktree() {
		slog.Info("teardown: worktree kept -- the session's isolation does not own it (bead-worktree)",
			"session", s.ExternalID, "cwd", s.CWD)
		return true
	}
	if keepWorktree {
		slog.Info("teardown: worktree kept -- another live session still uses it",
			"session", s.ExternalID, "cwd", s.CWD)
		return true
	}
	if worktreeHeldByStranger(ctx, s) {
		return true
	}
	if wm, err := open(ctx, s.CWD); errors.Is(err, gitclient.ErrNotARepository) {
		// cwd is not inside any git repository (pg2-tp8rx: the non-git
		// workspace root a drain session runs in), so there is no worktree to
		// remove -- expected, not a failure. Warning here drowned out genuine
		// teardown failures; only a non-sentinel open error still warns below.
		slog.Info("teardown: no worktree to remove (cwd is not inside a git repository)",
			"session", s.ExternalID, "cwd", s.CWD)
	} else if err != nil {
		slog.Warn("teardown: worktree remove failed (cannot open the cwd's git repository)",
			"session", s.ExternalID, "cwd", s.CWD, "err", err)
	} else if err := worktree.RemoveWithin(ctx, wm, s.CWD, true, teardownRemoveTimeout, worktree.PruneVia(open, repoRoot)); err != nil {
		slog.Warn("teardown: worktree remove failed (cwd may not be a linked worktree)",
			"session", s.ExternalID, "cwd", s.CWD, "err", err)
	} else {
		slog.Info("teardown: worktree removed", "session", s.ExternalID, "cwd", s.CWD)
		deleteAnchorBranch(ctx, open, repoRoot, s.ExternalID, s.Meta[ccpool.MetaKeyBead])
	}
	return true
}

// deleteAnchorBranch removes s's own throwaway pg-router/<beadID> anchor
// branch (worktree.Ensure's own doc comment: "a dedicated branch
// pg-router/<beadID>") now that closeSessionAndWorktree has confirmed its
// worktree is gone. x/gitclient's RemoveWorktree runs only `git worktree
// remove` and never touches the branch (bead pg2-ci75j's root cause) --
// without this call, the branch is orphaned in repoRoot forever, exactly the
// ~100+-stray-branch state pg2-ci75j found in the ZR monorepo at its other
// two call sites.
//
// Deliberately reopens the gitclient at repoRoot rather than reusing the
// worktree.Opener result the caller already holds: that manager was opened
// at s.CWD, which RemoveWorktree just removed, so it can no longer spawn git
// at all -- a branch is a repo-level ref that must be deleted from a still-
// existing checkout, the same anchor worktree.Ensure itself used to create
// the branch in the first place (internal/worktree/worktree.go's Ensure
// opens repoRoot, not the not-yet-created worktree path, to create it).
//
// beadID comes from the session's own pgrouter.bead metadata
// (ccpool.MetaKeyBead) -- the SAME source beadAlreadyClosed above already
// trusts for this session's bead identity, rather than re-deriving one from
// s.CWD's basename. An empty beadID (an older session dispatched before
// pg2-5sirm's meta round-trip, or a non-"worktree"-isolation stray this
// sweep still matched by prefix alone) is a no-op: "pg-router/" alone is not
// a real branch, and there is nothing safe to delete.
//
// These are pg-router's own internal per-dispatch anchor branches, reset at
// HEAD on every dispatch (worktree.Ensure's own ResetBranch/-B) -- never a
// user branch with independent value -- so once the worktree is confirmed
// removed the branch has fully served its purpose. Most are never merged
// into anything, so deletion uses force (`git branch -D`) rather than plain
// `-d`, which would refuse constantly for exactly that reason.
//
// Fails soft exactly like RemoveWorktree above: any error here (open
// failure, the branch already gone, a client that isn't a BranchManager, a
// transient git error) is logged and left for the next sweep, never
// escalated -- an orphaned branch costs disk, not correctness.
func deleteAnchorBranch(ctx context.Context, open worktree.Opener, repoRoot, externalID, beadID string) {
	if beadID == "" {
		return
	}
	branch := "pg-router/" + beadID
	root, err := open(ctx, repoRoot)
	if err != nil {
		slog.Warn("teardown: branch delete: open repo root failed (left for next sweep)",
			"session", externalID, "branch", branch, "err", err)
		return
	}
	bm, ok := root.(gitclient.BranchManager)
	if !ok {
		return
	}
	if err := bm.DeleteBranch(ctx, branch, true); err != nil {
		slog.Warn("teardown: branch delete failed (left for next sweep)",
			"session", externalID, "branch", branch, "err", err)
		return
	}
	slog.Info("teardown: branch deleted", "session", externalID, "branch", branch)
}

// beadAlreadyClosed reports whether s's own bead — read from its
// pgrouter.bead session metadata, ccpool.MetaKeyBead, stamped at dispatch by
// ccpool.DispatchMeta and round-tripped back through `ccpool list --json`'s
// own meta object (internal/ccpool.Session.Meta) — is already closed. This
// reads the LIVE session metadata rather than re-deriving a bead id by
// parsing ExternalID/Name (roles.Role.ExternalID/DisplayName's own
// "<prefix><name>-<beadid>[-<stamp>]" shape is not safely reversible here:
// this sweep runs across every enabled role sharing SessionPrefix, so it
// never has a single role.Name to strip, and both the prefix and a role name
// can themselves contain "-").
//
// Fails CLOSED (false — preserve, the existing/safe default) on every
// ambiguous case: no bead metadata on the session at all (an older session
// dispatched before pg2-5sirm, or a non-"worktree"-isolation stray this sweep
// still matches by prefix alone), or a `bd show` error (bd unreachable) —
// never risk purging a session an operator still needs to attach to just
// because bd could not be reached.
//
// Shared verbatim by both callers of this file's own sweeps: the removed shutdown sweep
// (needs_input only) and reconcile.go's reconcileClosedBeadSessions
// (idle or needs_input, pg2-hrppg) — the log line below is deliberately
// state-agnostic ("preserving session"), not "preserving needs_input
// session", since an idle session can hit this same ambiguous-lookup path.
func beadAlreadyClosed(ctx context.Context, br beads.Runner, s ccpool.Session) bool {
	beadID := s.Meta[ccpool.MetaKeyBead]
	if beadID == "" {
		return false
	}
	status, err := beads.Status(ctx, br, beadID)
	if err != nil {
		slog.Warn("teardown: bead-status lookup failed; preserving session",
			"session", s.ExternalID, "bead", beadID, "err", err)
		return false
	}
	return status == "closed"
}
