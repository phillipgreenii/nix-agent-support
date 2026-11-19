package hygiene

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/phillipgreenii/pg-router/internal/handlers/ccpool"
	"github.com/phillipgreenii/pg-router/internal/handlers/gwm"
	"github.com/phillipgreenii/pg-router/internal/handlers/worktree"
	"github.com/phillipgreenii/x/gitclient"
)

// Teardown of a session whose worktree is a pg-gwm lease.
//
// A session dispatched with a pg-gwm lease carries pgrouter.gwm_lease. Its
// worktree belongs to pg-gwm: the handler ends the lease by name
// (<role>/<bead>), never with git worktree remove, and expires it when pg-gwm
// reports the tree dirty. Each role holds its own worktree, so there is no
// peer-sharing rule and no live-process guard. A session without the key (one
// in flight when the lease code was applied) tears down the old way.

// teardownGWM is the pg-gwm client the closed-bead teardown uses; nil means the
// production CLI client at the repo root. Tests substitute a fake.
var teardownGWM gwm.Client

// gwmClientFor returns c, or the production client for repoRoot when c is nil.
func gwmClientFor(c gwm.Client, repoRoot string) gwm.Client {
	if c != nil {
		return c
	}
	return &gwm.CLI{RepoRoot: repoRoot}
}

// gwmLeased reports whether s's worktree is a pg-gwm lease.
func gwmLeased(s ccpool.Session) bool { return s.Meta[ccpool.MetaKeyGWMLease] != "" }

// gwmKey names s's lease: by <role>/<bead>, falling back to the lease id when
// the row carries no role or bead.
func gwmKey(s ccpool.Session) gwm.Key {
	role, bead := s.Meta[ccpool.MetaKeyRole], s.Meta[ccpool.MetaKeyBead]
	if role != "" && bead != "" {
		return gwm.Key{Name: role + "/" + bead}
	}
	return gwm.Key{LeaseID: s.Meta[ccpool.MetaKeyGWMLease]}
}

// gwmLeaseOver reports whether err says the lease no longer exists or is
// already finished, which counts as a completed release step.
func gwmLeaseOver(err error) bool {
	return errors.Is(err, gwm.ErrNotFound) || errors.Is(err, gwm.ErrLeaseLost) || errors.Is(err, gwm.ErrLeaseReleased)
}

// releaseGWM ends s's lease: release, or expire when the tree is dirty. It
// returns nil when the lease is over (including already over) and the error
// otherwise.
func releaseGWM(ctx context.Context, c gwm.Client, s ccpool.Session) error {
	key := gwmKey(s)
	err := c.Release(ctx, key)
	if errors.Is(err, gwm.ErrDirty) {
		err = c.Expire(ctx, key)
	}
	if gwmLeaseOver(err) {
		return nil
	}
	return err
}

// endGWMLease releases s's lease and then deletes its anchor branch when that is
// safe. It reports whether the release step is complete.
func endGWMLease(ctx context.Context, c gwm.Client, open worktree.Opener, repoRoot string, s ccpool.Session) bool {
	if err := releaseGWM(ctx, c, s); err != nil {
		slog.Warn("teardown: could not release the worktree lease; leaving it for the next reconcile",
			"session", s.ExternalID, "lease", s.Meta[ccpool.MetaKeyGWMLease], "err", err)
		return false
	}
	slog.Info("teardown: worktree lease released", "session", s.ExternalID, "lease", s.Meta[ccpool.MetaKeyGWMLease])
	deleteAnchorBranchIfMerged(ctx, open, repoRoot, s.ExternalID, s.Meta[ccpool.MetaKeyBead])
	return true
}

// deleteAnchorBranchIfMerged deletes pg-router/<bead> only when the canonical
// clone's HEAD already has every commit of it; git itself refuses while a
// worktree still holds the branch. Fails soft.
func deleteAnchorBranchIfMerged(ctx context.Context, open worktree.Opener, repoRoot, externalID, beadID string) {
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
	refs, okR := root.(gitclient.RefReader)
	bm, okB := root.(gitclient.BranchManager)
	if !okR || !okB {
		return
	}
	exists, err := refs.RefExists(ctx, "refs/heads/"+branch)
	if err != nil || !exists {
		return
	}
	ahead, err := refs.CommitsAhead(ctx, "HEAD", branch)
	if err != nil {
		slog.Warn("teardown: branch kept -- commit count unreadable", "session", externalID, "branch", branch, "err", err)
		return
	}
	if ahead > 0 {
		slog.Info("teardown: branch kept -- it has commits the canonical clone's HEAD lacks",
			"session", externalID, "branch", branch, "commits", ahead)
		return
	}
	if err := bm.DeleteBranch(ctx, branch, true); err != nil {
		slog.Info("teardown: branch kept (a worktree may hold it, or the delete failed)",
			"session", externalID, "branch", branch, "err", err)
		return
	}
	slog.Info("teardown: branch deleted", "session", externalID, "branch", branch)
}

// renewGWM extends s's lease by ttl; an over lease is logged, not an error.
func renewGWM(ctx context.Context, c gwm.Client, s ccpool.Session, ttl time.Duration) {
	id := s.Meta[ccpool.MetaKeyGWMLease]
	if err := c.Renew(ctx, id, ttl); err != nil && !gwmLeaseOver(err) {
		slog.Warn("orphan reconcile: could not renew the worktree lease", "session", s.ExternalID, "lease", id, "err", err)
	}
}
