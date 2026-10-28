package changes

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/dependency"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// rehydrateDependants is the detector for a dependant left stale by a
// blocker's closure (loss path (d) of the no-reconcile risk). A blocker closing
// changes a dependant's stored recursive issue_deps without the dependant's own
// list fingerprint moving, so nothing else would notice until the dependant's
// next change. For each id in closedIDs (the issues whose hydration in THIS
// poll appended a closed row) it reads the DIRECT, blocks-typed dependants
// through the reverse-edge index (dependency.IssueDependents, which already
// leaves out inactive and terminal ones) and re-hydrates each one that is
// still active and watched, as a normal hydration (no forced reconcile, no list
// fingerprint). The normal classifier decides what the re-read appends: a row
// only when the dependant's normalised snapshot differs, normally deps_changed.
//
// Every re-hydration draws one unit of the poll's hydration.max_per_poll
// budget, after the poll's own hydrations; once it is spent the rest are not
// read. pg-desk keeps no retry counter or deferral queue, so a dependant the
// budget cuts is NOT retried: it is seen at its next change. That loss path is
// accepted; the cut is reported as the budget reason on every watched query,
// like the sweep's.
//
// A dependant this poll already hydrated without error is skipped (its read is
// fresh) and a dependant is re-hydrated once however many of its blockers
// closed. A blocker whose stored facts carry no issue_deps is unavailable: when
// the index also holds no edge for it the blocker is skipped with a warning
// (nothing proves it has no dependants); edges the dependants themselves store
// are still used, since they are the edge source.
func (e *Engine) rehydrateDependants(ctx context.Context, opts Options, st *pollState, bud *budget, closedIDs []string) error {
	if opts.EntityType != issueEntityType || len(closedIDs) == 0 {
		return nil
	}
	window := e.Cfg.ThreadActiveWindow()
	now := e.now()
	index, err := dependency.NewIssueDependents(e.Store, func(row store.Entity) bool {
		return sourceTerminalRow(row, now, window)
	})
	if err != nil {
		return fmt.Errorf("changes: dependants index: %w", err)
	}
	seen := map[string]bool{}
	for _, blocker := range closedIDs {
		deps := index.DependentsOf(blocker)
		if len(deps) == 0 && !index.Available(blocker) {
			e.warnf("changes: dependants of %s unavailable: no stored issue_deps\n", blocker)
			continue
		}
		for _, id := range deps {
			if seen[id] {
				continue
			}
			seen[id] = true
			if herr, done := st.hydrated[id]; done && herr == nil {
				continue // read fresh in this poll
			}
			row, found, gerr := e.Store.GetEntity(e.repo(), issueEntityType, id)
			if gerr != nil {
				return gerr
			}
			if !found || row.Inactive || row.Watched == nil || !*row.Watched {
				continue
			}
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("changes: %w", err)
			}
			if why := bud.blocked(); why != "" {
				st.noteQueries(nil, why)
				return nil
			}
			herr := e.hydrate(ctx, issueEntityType, id, gather.ChangeChanged, OriginPGConnector, false, nil)
			st.hydrated[id] = herr
			bud.note(herr)
			if herr != nil {
				st.side = append(st.side, fmt.Sprintf("dependant %s of %s: %s", id, blocker, firstErrLine(herr.Error())))
			}
		}
	}
	return nil
}
