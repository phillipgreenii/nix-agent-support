// Package vcs declares the version-control-system provider interface used by
// pg-pr. Builtin impl is `github`; external impls are loaded via the
// `exec:<name>` syntax in config and the scriptout protocol.
package vcs

import (
	"context"
	"errors"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// ErrNotImplemented is returned by stub methods during Phase 0.
var ErrNotImplemented = errors.New("pg-pr: not implemented in this phase")

// Provider abstracts a VCS (GitHub, Forgejo, …) for PR-related operations.
type Provider interface {
	GetPR(ctx context.Context, repo string, number int) (*api.PR, error)
	ListMyPRs(ctx context.Context, repo string) ([]api.PR, error)
	ListTeamPRs(ctx context.Context, repo string, members []string) ([]api.PR, error)
	CreatePR(ctx context.Context, repo string, draft bool, title, body, branch, base string, reviewers, labels []string) (*api.PR, error)
	UpdatePR(ctx context.Context, repo string, number int, body string) error
	SetDraft(ctx context.Context, repo string, number int, draft bool) error
	SetAutomerge(ctx context.Context, repo string, number int, enabled bool) error
	Merge(ctx context.Context, repo string, number int) error
	Close(ctx context.Context, repo string, number int) error
	ListComments(ctx context.Context, repo string, number int) ([]api.Comment, error)
	AddComment(ctx context.Context, repo string, number int, body string) (*api.Comment, error)
	ReplyToThread(ctx context.Context, repo string, threadID, body string) (*api.Comment, error)
	ResolveThread(ctx context.Context, repo string, threadID string) error
	PostReview(ctx context.Context, repo string, number int, body string, comments []api.Comment) (*api.Review, error)
	// ListReviews returns the review summaries for a PR. State is one of
	// APPROVED, CHANGES_REQUESTED, COMMENTED. Body is the review-summary text
	// (used for agent approval-mining); Comments is left empty here — inline
	// comments are fetched via ListComments separately.
	ListReviews(ctx context.Context, repo string, number int) ([]api.Review, error)
}

// EnrichedPR bundles a PR with everything the sync snapshot loop reads
// per-PR. Providers that can fetch this data in one round-trip (e.g.
// GitHub via a single GraphQL search) implement EnrichedPRsProvider;
// the sync engine uses it to collapse per-PR REST fan-out into one
// per-repo call.
type EnrichedPR struct {
	PR       api.PR
	Reviews  []api.Review
	Comments []api.Comment
	CIRuns   []api.CIRun

	// Truncated reports the embedded connections whose pagination cap was
	// hit during the bulk fetch (so the caller can decide whether to fall
	// back to per-PR REST methods for full data). Empty when nothing was
	// truncated.
	Truncated []string
}

// EnrichedPRsProvider is an optional capability for VCS providers that
// can bulk-fetch enriched PR data in one round-trip. Sync uses this when
// available to replace ListMyPRs+ListTeamPRs+per-PR ListReviews/
// ListComments/ListRuns with a single per-repo query.
//
// searchQuery is a provider-native query string (for GitHub: the search
// syntax, e.g. `is:pr is:open repo:owner/name author:a author:b`).
type EnrichedPRsProvider interface {
	EnrichedPRs(ctx context.Context, repo string, searchQuery string) ([]EnrichedPR, error)
}
