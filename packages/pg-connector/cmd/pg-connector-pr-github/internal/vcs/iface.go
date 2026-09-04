// Package vcs is a local copy of pg-pr's pkg/provider/vcs interface —
// carried over so internal/github's ported Provider still type-checks
// against the same interface shape it always has [design: §9], without
// packages/pg-connector's go.mod depending on packages/pg-pr. Nothing
// outside cmd/pg-connector-pr-github ever sees this package: the backend's
// own pr.Provider glue (internal/provider.go) talks to internal/github's
// concrete *Provider type directly, not through this interface.
package vcs

import (
	"context"
	"errors"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/api"
)

// ErrNotImplemented is returned by stub methods during Phase 0.
var ErrNotImplemented = errors.New("pg-pr: not implemented in this phase")

// ErrAuthInvalid is the provider-agnostic auth-invalid sentinel. A provider's
// CheckAuth / poll methods wrap this (so errors.Is(err, ErrAuthInvalid) holds)
// when the underlying credentials are missing/invalid — distinguishing a real
// auth failure from a transient/network error. internal/sync references this
// sentinel directly (it only depends on the vcs interfaces, not the concrete
// provider) to drive the daemon's preflight + restart-to-refresh escalation.
var ErrAuthInvalid = errors.New("vcs: provider authentication failed")

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
	PostReview(ctx context.Context, repo string, number int, commitID, body string, comments []api.Comment) (*api.Review, error)
	// ListReviews returns the review summaries for a PR. State is one of
	// APPROVED, CHANGES_REQUESTED, COMMENTED. Body is the review-summary text
	// (used for agent approval-mining); Comments is left empty here — inline
	// comments are fetched via ListComments separately.
	ListReviews(ctx context.Context, repo string, number int) ([]api.Review, error)
}

// AuthChecker is an optional capability: a one-shot auth preflight the daemon
// runs before spinning up workers. Returns nil when authenticated; an error
// wrapping (via errors.Is) the provider's auth-invalid sentinel when the token
// is missing/invalid; any other error for transient/network failures.
type AuthChecker interface {
	CheckAuth(ctx context.Context) error
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
	// Files are the changed-file paths (for language detection). Empty when
	// not fetched (REST fallback) or truncated on a very large PR.
	Files []string
	// Commits are the PR's commit messages (for kind/urgency). Empty on the
	// REST fallback path.
	Commits []string
	// CommitAuthors are the PR's per-commit GitHub author logins
	// (author.user.login). Commits with no linked user contribute no entry.
	// Empty on the REST fallback path. Used to classify co-owned ownership.
	CommitAuthors []string

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

// PRFingerprint is the change-detection signature for one open PR, fetched
// by FingerprintProvider. It is intentionally small: just enough to decide
// "did this PR change since last tick?" without any node bodies.
type PRFingerprint struct {
	Repo              string
	Number            int
	Author            string // canonical login (bot suffix normalized)
	IsDraft           bool
	State             string // lowercased: open/closed/merged
	UpdatedAt         string
	HeadOID           string // last commit oid — catches pushes updated_at misses
	StatusRollup      string // statusCheckRollup.state, "" when none
	ReviewCount       int
	CommentCount      int
	ReviewThreadCount int
}

// FingerprintResult bundles one fingerprint query's PRs with pagination and
// rate-limit telemetry. Truncated is true when a hard page cap was hit before
// pagination completed — the caller MUST treat the roster as incomplete (do
// not infer "disappeared" from a truncated result).
type FingerprintResult struct {
	PRs       []PRFingerprint
	Truncated bool
	RateCost  int    // rateLimit.cost from the GraphQL envelope
	RateLeft  int    // rateLimit.remaining
	ResetAt   string // rateLimit.resetAt (RFC3339; window-reset timestamp)
}

// FingerprintProvider is an optional capability for VCS providers that can
// cheaply fetch per-PR change signatures via one (paginated) search. No repo
// arg: the search may span repos and each node carries its own repo. (The
// EnrichedPRsProvider keeps its repo arg for error context; this one does not
// — keep the asymmetry.)
type FingerprintProvider interface {
	FingerprintPRs(ctx context.Context, searchQuery string) (FingerprintResult, error)
}
