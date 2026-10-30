package executor

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
)

// Admission control (bead pg2-fx5x0l.5, INV-CCH-41, ADR 0117).
//
// With the handler's inlineSweeps switch off, a dispatch no longer runs the
// housekeeping passes before its capacity check. It pays only for what THIS call
// needs: when the pool is full and no usage limit is in force it asks the
// Reconciler to close the pool's closed-bead sessions once and re-checks capacity
// once, and before it prepares its own bead's worktree it asks the Reconciler to
// examine that one worktree. The passes themselves live in package main (the
// executor cannot import it), so they reach the executor through this seam; the
// closure is built where cfg.RepoRoot, the worktree directory, the git opener and
// the quiet check live (serveDispatch).

// ReconcileOutcome is what one capacity-pressure closed-bead reconcile did.
type ReconcileOutcome struct {
	// Closed is how many closed-bead sessions the reconcile closed.
	Closed int
	// Skipped is true when the reconcile did not run because an earlier one for the
	// same pool found nothing within the negative-cache interval.
	Skipped bool
}

// Reconciler is the executor's seam to the handler's housekeeping passes. A nil
// Reconciler in Deps, or an error from either method, never fails a dispatch.
type Reconciler interface {
	// ReconcileClosedBeads closes the sessions of the dispatching role's pool whose
	// bead has closed (INV-CCH-14, INV-CCH-20), rate-limited per pool by the
	// negative cache (INV-CCH-41).
	ReconcileClosedBeads(ctx context.Context) (ReconcileOutcome, error)
	// ReclaimLeakedWorktree applies the guarded worktree-keyed examination
	// (INV-CCH-19) to ONE bead's worktree, so a worktree its own bead leaked is
	// not reused as if it were fresh.
	ReclaimLeakedWorktree(ctx context.Context, beadID string) error
}

// admissionActive reports whether this dispatch does its housekeeping on
// demand (inlineSweeps off, a Reconciler wired) instead of inline.
func (r *ccpoolRun) admissionActive() bool {
	return !r.deps.Cfg.InlineSweeps && r.deps.Reconciler != nil
}

// reconcileForCapacity runs the closed-bead reconcile once for a dispatch that
// found its pool full, then reads capacity once more. note describes what was
// tried, for the decline's detail text ("" when nothing was). It never loops: the
// caller calls it at most once per dispatch.
func (r *ccpoolRun) reconcileForCapacity(ctx context.Context, d DispatchContext, before ccpool.Capacity) (after ccpool.Capacity, capErr error, note string) {
	out, err := r.deps.Reconciler.ReconcileClosedBeads(ctx)
	switch {
	case err != nil:
		slog.Warn("capacity-pressure reconcile failed; declining", "role", d.Role.Name, "bead", d.Item.ID, "err", err)
		return before, nil, "a closed-bead reconcile was tried and failed"
	case out.Skipped:
		slog.Info("capacity-pressure reconcile skipped: one found nothing recently", "role", d.Role.Name, "bead", d.Item.ID)
		return before, nil, "a closed-bead reconcile found nothing recently and was not repeated"
	}
	slog.Info("capacity-pressure reconcile ran", "role", d.Role.Name, "bead", d.Item.ID, "closed", out.Closed)
	after, capErr = r.deps.CC.Capacity(ctx)
	return after, capErr, fmt.Sprintf("a closed-bead reconcile was tried and closed %d", out.Closed)
}
