package sync

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/enrich"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/ticketlink"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	jiraprovider "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/issues/jira"
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
//
// When cfg.Jira is non-nil the JiraLookupFunc is built from the in-repo Jira
// provider (pkg/provider/issues/jira) and injected into enrich.Input. When
// cfg.Jira is nil the signal stays disabled (nil JiraLookupFunc), preserving
// backward compatibility.
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
	// repo's config-driven ticket patterns.
	in.LinkedTicketKeys = ticketlink.Parse(pr.Branch, pr.Title, pr.Body, rcfg.TicketPatterns)

	// Wire JiraLookupFunc when Jira is configured (pg2-jpfw.4). When cfg.Jira
	// is nil (or cfg itself is nil — allowed for test engines built without Cfg)
	// the signal remains disabled (nil JiraLookupFunc) — no behaviour change for
	// callers that don't configure a Jira section.
	cfg := e.cfg()
	if cfg != nil && cfg.Jira != nil {
		// Use the injected provider (Deps.JiraProvider) when set (tests), or
		// construct the default subprocess-backed provider from PGPR_JIRA_BINARY.
		p := e.deps.JiraProvider
		if p == nil {
			p = jiraprovider.New()
		}
		in.JiraLookupFunc = jiraprovider.NewJiraLookupFunc(p, cfg.Jira.AdapterConfig)
	}
	// When cfg.Jira is nil, in.JiraLookupFunc remains nil (signal disabled).

	// Use ComputeWithContext so any injected ProjectHealthFunc or JiraLookupFunc
	// (wired above) can be cancelled via the calling context. When both are nil
	// (current default), this is identical to Compute(in).
	r := enrich.ComputeWithContext(ctx, in)
	if err := e.deps.Store.SetEnrichment(ctx, repo, pr.Number, store.Enrichment{
		Kind: r.Kind, Languages: r.Languages, Size: r.Size,
		Urgency: r.Urgency, UrgencyScore: r.UrgencyScore, UrgencyReasons: r.UrgencyReasons,
	}); err != nil {
		return fmt.Errorf("PR #%d enrich: %w", pr.Number, err)
	}
	return nil
}
