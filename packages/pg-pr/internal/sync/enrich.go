package sync

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/enrich"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/ticketlink"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// enrichAndStore computes enrichment for an observed open PR and persists it via
// the dedicated store.SetEnrichment write (decoupled from the lifecycle emit;
// the row already exists). No-op when Store is nil. Errors are non-fatal — they
// are not lifecycle events and self-heal on the next tick — so callers record
// them into summary.Errors. enriched may be nil (REST path): files/commits are
// then empty and enrichment degrades gracefully.
//
// rcfg supplies the repo's ticket patterns so linked ticket keys can be
// extracted from the PR's branch/title/body and populated into enrich.Input
// for the Jira priority/incident signal (pg2-4c5i.26). An empty TicketPatterns
// slice disables ticket-key extraction for this repo, which is the default
// until a consuming config supplies patterns.
func (e *Engine) enrichAndStore(ctx context.Context, repo string, pr api.PR, enriched *vcs.EnrichedPR, rcfg config.RepoConfig) error {
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
	// Populate linked ticket keys from the PR's branch/title/body using the
	// repo's config-driven ticket patterns. The Jira lookup function
	// (JiraLookupFunc) is intentionally left nil here — wiring the real Jira
	// client is deferred to a future bead (pg2-4c5i.26 follow-up). When nil,
	// the signal is a no-op and urgency is computed identically to before.
	in.LinkedTicketKeys = ticketlink.Parse(pr.Branch, pr.Title, pr.Body, rcfg.TicketPatterns)
	// in.JiraLookupFunc remains nil until the real provider is wired (deferred).

	// Use ComputeWithContext so any injected ProjectHealthFunc or JiraLookupFunc
	// (wired in a future live-verification bead) can be cancelled via the
	// calling context. When both are nil (current default), this is identical
	// to Compute(in).
	r := enrich.ComputeWithContext(ctx, in)
	if err := e.deps.Store.SetEnrichment(ctx, repo, pr.Number, store.Enrichment{
		Kind: r.Kind, Languages: r.Languages, Size: r.Size,
		Urgency: r.Urgency, UrgencyScore: r.UrgencyScore, UrgencyReasons: r.UrgencyReasons,
	}); err != nil {
		return fmt.Errorf("PR #%d enrich: %w", pr.Number, err)
	}
	return nil
}
