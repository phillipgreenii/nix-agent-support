package sync

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/ownership"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// overlayMergeState copies GitHub merge-state from the enriched PR onto the
// observed PR (the REST GetPR/enumerate paths leave it empty). Idempotent.
//
// The REST provider.GetPR field list (used by refreshPR) and the plain
// enumerate path omit mergeable/mergeStateStatus entirely, so pr.HasConflict()
// would otherwise ALWAYS be false on those paths — silently defeating the
// attention-dampening predicate (snapshot.NeedsAttention's hasConflict guard)
// in production. GraphQL enrichment (enrichOnePR/bulk EnrichedPRs) is the only
// source of these fields; overlay them onto the observed pr so every
// emit/attention site downstream sees the real merge state. No-op when
// enriched is nil (single-PR enrich failure). (pg2-tsgkj)
func overlayMergeState(pr *api.PR, enriched *vcs.EnrichedPR) {
	if enriched == nil {
		return
	}
	pr.Mergeable = enriched.PR.Mergeable
	pr.MergeStateStatus = enriched.PR.MergeStateStatus
	pr.AutoMergeEnabled = enriched.PR.AutoMergeEnabled
}

// commitAuthorsOf returns enriched.CommitAuthors, tolerating a nil enriched
// (single-PR enrich failure) — nil degrades ownership to authorship-only.
func commitAuthorsOf(enriched *vcs.EnrichedPR) []string {
	if enriched == nil {
		return nil
	}
	return enriched.CommitAuthors
}

// reviewRequestedOfSelf reports whether self is among the PR's requested
// reviewers (exact GitHub-login match). Empty self => false. Kept in the sync
// layer because the VCS provider is self-agnostic (it returns the raw requested
// reviewers; only the engine knows the configured self login).
func reviewRequestedOfSelf(self string, requested []string) bool {
	if self == "" {
		return false
	}
	for _, r := range requested {
		if r == self {
			return true
		}
	}
	return false
}

// refreshPR fetches one PR and reconciles its bead + snapshot from real
// state. It is the daemon worker's per-PR entry point and the single place
// beads are closed or marked dormant.
//
// Ownership is classified once (Mine/CoOwned/Team) right after enrichment, and
// that single verdict drives both the draft-hide decision and the attention
// gate below.
//
// Returns (nil, nil) when the PR should be removed from the dashboard:
//   - the upstream PR is closed or merged (bead closed + cascaded), or
//   - it is a hidden TEAM draft (bead marked draft, but not surfaced). A
//     co-owned draft (a teammate's PR I've pushed commits onto) is NOT
//     hidden — it falls through and is surfaced like an active PR.
//
// Returns (input, nil) to upsert the PR onto the dashboard: an open, active
// PR (mine, co-owned, or a non-draft team PR) runs the full active pipeline
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
	// "Requested of me" is derived downstream in buildPRInput (the single point
	// BOTH the daemon refresh AND the one-shot full-sync snapshot paths converge
	// on), so it is consistent across both — not re-derived here (pg2-ynhr.13
	// B2/B5 review #2).
	summary := &Summary{}

	// Closed/merged: emit pr.closed/pr.merged so the beadsbridge cascade-closes
	// the existing bead + descendants at outbox flush, then signal removal from
	// the dashboard. The engine no longer closes the bead inline.
	if pr.State == "closed" || pr.State == "merged" || pr.Merged {
		merged := pr.Merged || pr.State == "merged"
		ownershipStr := ownership.Classify(ownership.Engagement{
			Self: e.cfg().SelfLogin, PRAuthor: pr.Author,
		}).String()
		row := e.prToStoreRow(repo, *pr, ownershipStr) // state resolves to closed/merged via stateForPR
		// emitPRClosed atomically upserts the closed/merged row AND enqueues the
		// event in one tx (keeping the store authoritative). The emit is
		// critical: a dropped pr.closed/pr.merged event also drops the bridge
		// cascade. Propagate (matching emitPREvent in the draft branch below)
		// rather than silencing it.
		if err := e.emitPRClosed(ctx, row, merged); err != nil {
			return nil, err
		}
		flushOutbox(ctx, e.deps.Store, e.deps.Dispatch)
		return nil, nil
	}

	// Enrich BEFORE the draft-hide decision so a co-owned draft (teammate PR I
	// pushed commits onto) is detected and surfaced rather than hidden as a team
	// draft. (Closed/merged PRs returned above never reach here.)
	enriched := e.enrichOnePR(ctx, rcfg, *pr)
	// The REST GetPR path leaves Mergeable/MergeStateStatus/AutoMergeEnabled
	// empty; overlay GraphQL's merge-state onto pr so pr.HasConflict() (used
	// below by applyFetchedPR's emit AND the attention block) reflects reality
	// on the daemon path (pg2-tsgkj).
	overlayMergeState(pr, enriched)
	own := ownership.Classify(ownership.Engagement{
		Self: e.cfg().SelfLogin, PRAuthor: pr.Author, CommitAuthors: commitAuthorsOf(enriched),
	})

	// Hidden team draft: emit pr.updated (state=draft) so the bridge keeps the
	// bead in sync — we can react when it leaves draft — but don't surface it on
	// the dashboard. The engine no longer EnsureMergeRequests inline. Only a
	// genuine TEAM draft is hidden; a co-owned/mine draft falls through to the
	// active pipeline below and is surfaced.
	if own == ownership.Team && pr.Draft {
		// emitPREvent atomically upserts the row (state=draft) AND enqueues
		// pr.updated in one tx so the bridge keeps the bead in sync.
		if err := e.emitPREvent(ctx, store.EventPRUpdated, repo, *pr, own.String()); err != nil {
			return nil, err
		}
		flushOutbox(ctx, e.deps.Store, e.deps.Dispatch)
		return nil, nil
	}

	// Active PR: reuse the enrichment already fetched above across the feedback
	// pipeline and the snapshot input.
	//
	// applyFetchedPR now EMITS pr.opened/updated rather than creating the bead
	// inline (the beadsbridge projects it at outbox flush). So the outbox MUST
	// be flushed BEFORE buildPRInput, whose dep path locates the bead via
	// FindByRepoAndNumber — the bead does not exist until the flush projects it.
	// Passing "" as the known id lets buildPRInput run that lookup post-flush.
	if err := e.applyFetchedPR(ctx, rcfg, pr, enriched, summary); err != nil {
		return nil, err
	}
	flushOutbox(ctx, e.deps.Store, e.deps.Dispatch)

	// Teammate-attention projection (pg2-4c5i.13): emit for any PR that is NOT
	// mine-authored. Team PRs carry the real predicate, re-derived from the
	// just-persisted facts and emitted every tick (self-healing under
	// fire-once delivery, R1). Co-owned PRs force Need=false so a team->co-owned
	// transition idempotently CLOSES a previously-opened attention bead. Then
	// flush so the bridge ensures/closes the attention bead. Mine PRs carry no
	// attention signal.
	if e.deps.Store != nil && own != ownership.Mine {
		if stored, gerr := e.deps.Store.GetPR(ctx, rcfg.Remote, pr.Number); gerr == nil && stored != nil {
			if aerr := e.emitAttention(ctx, bdc, rcfg.Remote, pr.Number, stored.ID, own, pr.HasConflict()); aerr != nil {
				// Non-fatal: a failed attention emit self-heals next tick.
				summary.Errors = append(summary.Errors, SummaryError{Repo: rcfg.Remote, Message: aerr.Error()})
			} else {
				flushOutbox(ctx, e.deps.Store, e.deps.Dispatch)
			}
		}
	}

	in := e.buildPRInput(ctx, *pr, enriched, bdc, nil, rcfg, "")
	return &in, nil
}
