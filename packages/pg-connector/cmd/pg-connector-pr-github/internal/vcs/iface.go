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

// EnrichedPR/EnrichedPRsProvider and PRFingerprint/FingerprintResult/
// FingerprintProvider (pg-pr's bulk-fetch optimizations for its own sync
// snapshot loop) were carried over into this file but never implemented by
// anything but internal/github's now-deleted enrich.go/fingerprint.go, and
// never consumed by anything in pg-connector — this backend's Show always
// performs a live, uncached, per-PR read (provider.go's own doc comment).
// Design §9.1's verb→destination table retires pg-pr's `sync` command group
// "without a rewrite target" (pr-pool polls the beads connector directly
// instead), and no design section names either optional capability as a
// future pg-connector need, so both were removed as dead surface rather
// than carried forward speculatively [bead pg2-lh3c4]. If a future
// `pg-connector pr list` (design Appendix A: still unresolved — live call
// vs. backend-local store) turns out to need bulk fan-out, re-derive the
// shape fresh against that verb's actual requirements rather than
// resurrecting this file: the original was tuned for pg-pr's polling
// daemon, a different consumer shape than a one-shot CLI verb.
