package hygiene

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/phillipgreenii/pg-router/internal/handlers/config"
)

// janitorDryRun lists what each pass WOULD reclaim, read-only (INV-CCH-41's
// dry run): no lock, no event row, no mutation. It reuses the passes' own
// read-only filters (isOrphanOf, beadAlreadyClosed, reconcilableState), so it is an upper bound: the guards a
// real pass applies under a lock (transcript quiet window, clean and pushed
// worktree) are not evaluated here. The lock-file collection is
// not previewed.
func janitorDryRun(ctx context.Context, w io.Writer, jroles []janitorRole, cfg config.Config) int {
	fmt.Fprintf(w, "janitor dry run: %d role(s); candidates are an upper bound, nothing is reclaimed\n", len(jroles))
	poolDone := map[string]bool{}
	for _, jr := range jroles {
		if !poolDone[jr.pool] {
			poolDone[jr.pool] = true
			dryClosedBead(ctx, w, jr)
		}
		dryOrphans(ctx, w, jr)
	}
	fmt.Fprintln(w, "lock_gc: not previewed")
	return 0
}

func dryClosedBead(ctx context.Context, w io.Writer, jr janitorRole) {
	sessions, err := jr.deps.CC.List(ctx)
	if err != nil {
		fmt.Fprintf(w, "%s (pool %q): list failed: %v\n", passClosedBead, jr.pool, err)
		return
	}
	for _, s := range sessions {
		if !strings.HasPrefix(s.ExternalID, jr.cfg.SessionPrefix) || s.PurgePending() || !reconcilableState(s.State) {
			continue
		}
		if beadAlreadyClosed(ctx, jr.deps.BD, s) {
			fmt.Fprintf(w, "%s (pool %q): would close session %s\n", passClosedBead, jr.pool, s.ExternalID)
		}
	}
}

func dryOrphans(ctx context.Context, w io.Writer, jr janitorRole) {
	sessions, err := jr.deps.CC.List(ctx)
	if err != nil {
		fmt.Fprintf(w, "%s (role %s): list failed: %v\n", passOrphan, jr.role.Name, err)
		return
	}
	now := orphanNow(jr.deps)
	for _, s := range sessions {
		if isOrphanOf(s, jr.role, jr.cfg.SessionPrefix, now) {
			fmt.Fprintf(w, "%s (role %s): would reclaim orphan %s\n", passOrphan, jr.role.Name, s.ExternalID)
		}
	}
}
