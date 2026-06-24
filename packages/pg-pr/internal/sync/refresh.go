package sync

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
)

// refreshPR fetches one PR and reconciles its bead + snapshot from real
// state. It is the daemon worker's per-PR entry point and the single place
// beads are closed or marked dormant.
//
// Returns (nil, nil) when the PR should be removed from the dashboard:
//   - the upstream PR is closed or merged (bead closed + cascaded), or
//   - it is a hidden team draft (bead marked draft, but not surfaced).
//
// Returns (input, nil) to upsert the PR onto the dashboard: an open, active
// PR (self-authored, or a non-draft team PR) runs the full active pipeline
// via applyFetchedPR and yields a snapshot input.
func (e *Engine) refreshPR(ctx context.Context, repo string, number int) (*snapshot.PRInput, error) {
	rcfg, err := e.repoConfig(repo)
	if err != nil {
		return nil, err
	}
	provider, err := e.providerFor(rcfg)
	if err != nil {
		return nil, err
	}
	bdc := e.bdClientFor(rcfg)
	pr, err := provider.GetPR(ctx, repo, number)
	if err != nil {
		return nil, fmt.Errorf("refreshPR %s#%d: %w", repo, number, err)
	}
	summary := &Summary{}

	// Closed/merged: emit pr.closed/pr.merged so the beadsbridge cascade-closes
	// the existing bead + descendants at outbox flush, then signal removal from
	// the dashboard. The engine no longer closes the bead inline.
	if pr.State == "closed" || pr.State == "merged" || pr.Merged {
		merged := pr.Merged || pr.State == "merged"
		ownership := "team"
		if e.isSelfAuthored(pr.Author) {
			ownership = "mine"
		}
		row := e.prToStoreRow(repo, *pr, ownership) // state resolves to closed/merged via stateForPR
		if e.deps.Store != nil {
			_, _ = e.deps.Store.UpsertPR(ctx, row) // keep store authoritative (best-effort)
		}
		// The emit is critical: a dropped pr.closed/pr.merged event also drops
		// the bridge cascade. Propagate (matching emitPREvent in the draft
		// branch below) rather than silencing it.
		if err := e.emitPRClosed(ctx, row, merged); err != nil {
			return nil, err
		}
		flushOutbox(ctx, e.deps.Store, e.deps.Dispatch)
		return nil, nil
	}

	// Hidden team draft: emit pr.updated (state=draft) so the bridge keeps the
	// bead in sync — we can react when it leaves draft — but don't surface it on
	// the dashboard. The engine no longer EnsureMergeRequests inline.
	if !e.isSelfAuthored(pr.Author) && pr.Draft {
		if err := e.emitPREvent(ctx, store.EventPRUpdated, repo, *pr, "team"); err != nil {
			return nil, err
		}
		if e.deps.Store != nil {
			_, _ = e.deps.Store.UpsertPR(ctx, e.prToStoreRow(repo, *pr, "team"))
		}
		flushOutbox(ctx, e.deps.Store, e.deps.Dispatch)
		return nil, nil
	}

	// Active PR: fetch this PR's enrichment once and reuse it across the
	// feedback pipeline and the snapshot input.
	//
	// applyFetchedPR now EMITS pr.opened/updated rather than creating the bead
	// inline (the beadsbridge projects it at outbox flush). So the outbox MUST
	// be flushed BEFORE buildPRInput, whose dep path locates the bead via
	// FindByRepoAndNumber — the bead does not exist until the flush projects it.
	// Passing "" as the known id lets buildPRInput run that lookup post-flush.
	enriched := e.enrichOnePR(ctx, rcfg, *pr)
	if err := e.applyFetchedPR(ctx, rcfg, pr, enriched, summary); err != nil {
		return nil, err
	}
	flushOutbox(ctx, e.deps.Store, e.deps.Dispatch)
	in := e.buildPRInput(ctx, *pr, enriched, bdc, nil, rcfg, "")
	return &in, nil
}
