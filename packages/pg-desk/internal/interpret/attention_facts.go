package interpret

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
)

// PRAttentionFacts is the narrow set of facts the attention evaluator's own-PR
// rules need that the stored interpretation row does not carry: whether the
// PR is open and non-draft, whether it has a merge conflict or an unresolved
// review thread, and the CI rollup. They are derived by the SAME helpers
// Interpret uses (computeCIRollup, hasConflict, hasUnresolvedThread), so the
// attention feed, the dashboard panels and `pg-desk links` cannot disagree
// about what is failing or conflicting (docs/behavior/pg-desk/attention.md,
// "The evaluator").
type PRAttentionFacts struct {
	Open  bool
	Draft bool
	// CIState is the rollup display state (none, pending, success or
	// failure) after the check_interpreters exclusions. It is NEVER softened
	// by review_exempt_checks: that softening only decides the panels.
	CIState string
	// CIReviewState is the rollup state the owner-side panel decisions
	// consult (CIState with the review_exempt_checks softening applied).
	CIReviewState    string
	Conflict         bool
	UnresolvedThread bool
}

// DerivePRAttentionFacts decodes a stored entity's facts JSON (a gather.Facts
// document) and derives PRAttentionFacts under cfg. It is pure and reads no
// clock. A document with no pr_show, or one that does not decode, is an
// error: the caller treats such an entity as not evaluable.
func DerivePRAttentionFacts(factsJSON string, cfg *config.Config) (PRAttentionFacts, error) {
	var facts gather.Facts
	if err := json.Unmarshal([]byte(factsJSON), &facts); err != nil {
		return PRAttentionFacts{}, fmt.Errorf("interpret: decode stored facts: %w", err)
	}
	if len(facts.PRShow) == 0 {
		return PRAttentionFacts{}, errors.New("interpret: stored facts carry no pr_show")
	}
	pr, err := decodePRShow(facts.PRShow)
	if err != nil {
		return PRAttentionFacts{}, fmt.Errorf("interpret: decode pr show: %w", err)
	}
	var interps []config.CheckInterpreterConfig
	var exempt []string
	if cfg != nil {
		interps = cfg.CheckInterpreters
		exempt = cfg.ReviewExemptChecks
	}
	ci := computeCIRollup(facts.CI, interps, pr.HeadSHA, exempt)
	return PRAttentionFacts{
		Open:             pr.State == "open",
		Draft:            pr.Draft,
		CIState:          ci.State,
		CIReviewState:    ci.ReviewState(),
		Conflict:         pr.hasConflict(),
		UnresolvedThread: hasUnresolvedThread(pr.allComments()),
	}, nil
}
