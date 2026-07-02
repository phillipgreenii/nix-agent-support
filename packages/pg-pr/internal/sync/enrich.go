package sync

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/enrich"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// enrichAndStore computes enrichment for an observed open PR and persists it via
// the dedicated store.SetEnrichment write (decoupled from the lifecycle emit;
// the row already exists). No-op when Store is nil. Errors are non-fatal — they
// are not lifecycle events and self-heal on the next tick — so callers record
// them into summary.Errors. enriched may be nil (REST path): files/commits are
// then empty and enrichment degrades gracefully.
func (e *Engine) enrichAndStore(ctx context.Context, repo string, pr api.PR, enriched *vcs.EnrichedPR) error {
	if e.deps.Store == nil {
		return nil
	}
	in := enrich.Input{PR: pr, Labels: pr.Labels}
	if enriched != nil {
		in.Files = enriched.Files
		in.Commits = enriched.Commits
		in.CIRuns = enriched.CIRuns
		if len(pr.Labels) == 0 {
			in.Labels = enriched.PR.Labels
		}
	}
	// Use ComputeWithContext so any injected ProjectHealthFunc (wired in a
	// future live-verification bead) can be cancelled via the calling context.
	// When ProjectHealthFunc is nil (current default), this is identical to
	// Compute(in).
	r := enrich.ComputeWithContext(ctx, in)
	if err := e.deps.Store.SetEnrichment(ctx, repo, pr.Number, store.Enrichment{
		Kind: r.Kind, Languages: r.Languages, Size: r.Size,
		Urgency: r.Urgency, UrgencyScore: r.UrgencyScore, UrgencyReasons: r.UrgencyReasons,
	}); err != nil {
		return fmt.Errorf("PR #%d enrich: %w", pr.Number, err)
	}
	return nil
}
