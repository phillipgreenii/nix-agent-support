package sync

import (
	"context"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
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

	// Closed/merged: close the existing bead and cascade-close its
	// descendants, then signal removal from the dashboard.
	if pr.State == "closed" || pr.State == "merged" || pr.Merged {
		if existing, ferr := e.findBeadByPR(ctx, bdc, repo, pr.Number); ferr == nil && existing != nil {
			reason := "upstream-" + pr.State
			if pr.Merged {
				reason = "upstream-merged"
			}
			if cerr := bdc.CloseMergeRequest(ctx, existing.ID, reason); cerr == nil {
				e.cascadeClose(ctx, bdc, existing.ID, "pr-closed", summary)
			}
		}
		return nil, nil
	}

	// Hidden team draft: keep the bead in sync (state=draft) so we can react
	// when it leaves draft, but don't surface it on the dashboard.
	if !e.isSelfAuthored(pr.Author) && pr.Draft {
		fields := beads.MergeRequestFields{
			Repo:         repo,
			PRNumber:     pr.Number,
			State:        "draft",
			Branch:       pr.Branch,
			Base:         pr.Base,
			Author:       pr.Author,
			URL:          pr.URL,
			LastSyncedAt: e.deps.Now().UTC().Format(time.RFC3339),
			Draft:        true,
		}
		_, _, _ = bdc.EnsureMergeRequest(ctx, pr.URL, fields)
		return nil, nil
	}

	// Active PR: fetch this PR's enrichment once and reuse it across the
	// feedback pipeline and the snapshot input, threading the upserted bead id
	// so buildPRInput skips a redundant FindByRepoAndNumber.
	enriched := e.enrichOnePR(ctx, rcfg, *pr)
	id, _, err := e.applyFetchedPR(ctx, bdc, rcfg, pr, enriched, summary)
	if err != nil {
		return nil, err
	}
	in := e.buildPRInput(ctx, *pr, enriched, bdc, nil, rcfg, id)
	flushOutbox(ctx, e.deps.Store, e.deps.Dispatch)
	return &in, nil
}
