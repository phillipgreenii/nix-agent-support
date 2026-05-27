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
