// Package pr declares the pr capability's provider interface — a small,
// capability-scoped Go interface (never named after a backend/system)
// (INV-CAP-1) that a Tier-2 PR backend's concrete provider implements. It
// matches this repo's existing small-per-capability-interface convention
// (e.g. vcs.Provider in packages/pg-pr/pkg/provider/vcs) rather than one
// interface spanning multiple systems (INV-CAP-1).
//
// This package sits alongside pkg/schema and pkg/scriptout as part of the
// module's shared surface importable across backend boundaries — see
// cmd/pg-connector's layout-convention check.
package pr

import (
	"context"
	"encoding/json"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Provider is the pr capability's provider interface: a Show-style read
// plus the list/files/commits ops this docket names (naming_convention_test.go;
// interfaces.md's pr op catalog). The categorize/feedback_set write ops
// this interface used to also carry were retired by bead pg2-2j5ac.28.7:
// category/disposition are re-derived by pg-desk's interpreter rather than
// persisted by any backend (statelessness, D3). A concrete backend MAY
// additionally implement pkg/provider.AuthChecker, asserted via a
// type-check rather than folded into this interface (INV-AUTH-1) — see
// NewDispatchTable in dispatch.go.
type Provider interface {
	// Show returns id's current full state, including comments/
	// review-thread entries each with their own id (interfaces.md's pr op
	// catalog). The returned schema.PR MUST carry its
	// own AsOf/Stale pair (bead pg2-681xo): AsOf is this read's own as-of
	// time, and Stale is this Provider's own as-of/stale determination —
	// true only when this read served (rather than freshly fetched) a
	// cached copy of the underlying PR facts that has aged past this
	// Provider's own staleness bound. A Provider with no such cache (every
	// current implementation) always returns Stale false with AsOf set to
	// the read's own call time.
	Show(ctx context.Context, id string) (*schema.PR, error)

	// List runs query (already resolved from the request's own
	// config.queries block — dispatch.go's "list" handler does that
	// resolution centrally, reporting query_not_recognized itself before
	// ever calling List, so a concrete Provider need not repeat that check)
	// and returns the matching PRs. query MAY carry more than one
	// expression (design: "run each, union results deduplicated by
	// id, report truncated if any member truncated") — List, not the
	// dispatch table, does that fan-in. idsOnly, when true, means the
	// caller only wants PRListResult.PresentIDs populated; a Provider MAY
	// still choose to populate Entities anyway (harmless, just wasted
	// work) but need not. bead pg2-2j5ac.28.1.
	//
	// cursor is the incoming page/incremental-fetch token, opaque to
	// everything except the Provider that produced it (design: section 4.2
	// — a per-backend-opaque JSON blob, e.g. a Jira cursor carrying an
	// "updated >= " bound, never a bare string) — nil on a first/full
	// fetch. dispatch.go's "list" handler decodes it off the wire and
	// passes it through UNVALIDATED: a Provider that does not implement
	// incremental listing MUST simply ignore whatever it is handed and
	// MUST always answer with PRListResult.Cursor nil (design: "A backend
	// that does not support incremental listing MUST return null and MUST
	// ignore any cursor it is handed"). The human-facing CLI verb
	// `pg-connector pr list` MUST NEVER pass a non-nil cursor on this verb
	// (design: "It MUST NOT pass a cursor on this verb") — only the
	// umbrella's own internal changes/ledger refresh path does. bead
	// pg2-2j5ac.30.6.
	List(ctx context.Context, query schema.QueryExpr, idsOnly bool, cursor json.RawMessage) (*schema.PRListResult, error)

	// Files returns id's changed-file list. A targeted op (resolves to the
	// one backend that owns id), matching Show's existing convention — NOT
	// the fan-out scheme List uses. bead pg2-2j5ac.28.2.
	Files(ctx context.Context, id string) (*schema.PRFilesResult, error)

	// Commits returns id's commit list. Each entry MUST carry the
	// commit's own GitHub author login (schema.PRCommit.Author's doc
	// comment) — the co-owned-ownership classification's consumer. A
	// targeted op, same convention as Files above. bead pg2-2j5ac.28.2.
	Commits(ctx context.Context, id string) (*schema.PRCommitsResult, error)
}

// ReviewComment is one comment of a ReviewSubmitRequest (contract 9.1). It is
// either a NEW point anchored to Path/Line on Side ("LEFT" or "RIGHT"; "" means
// RIGHT), or a REPLY to an existing review thread: ThreadID is the real
// review-thread id (the review_thread_id that "pr show" reports) and Path and
// Line MUST then be unset. An item with no ThreadID is always a new point.
type ReviewComment struct {
	ThreadID string `json:"thread_id,omitempty"`
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
	Side     string `json:"side,omitempty"`
	Body     string `json:"body"`
}

// ReviewSubmitRequest is the review_submit op's input (contract 9.1). ID is
// the PR id, in the same style as every other pr op's args.
//
// SupersedePending is accepted and ignored: the op never deletes, replaces or
// submits a review, but a caller that still sends the field keeps working.
type ReviewSubmitRequest struct {
	ID               string          `json:"id"`
	HeadSHA          string          `json:"head_sha"`
	Body             string          `json:"body"`
	Comments         []ReviewComment `json:"comments"`
	SupersedePending bool            `json:"supersede_pending,omitempty"`
}

// review_submit statuses. Every one is a well-formed result and exits 0; the
// status says what happened.
const (
	// StatusPosted: no pending review existed, and one was created.
	StatusPosted = "posted"
	// StatusAppend: at least one comment, or a new per-head body section, was
	// written to an existing pending review.
	StatusAppend = "append"
	// StatusNoChange: nothing was written. When there was no pending review
	// none is created, and State is StateNone.
	StatusNoChange = "no_change"
)

// StateNone is the result State of a no_change run that found no pending
// review to use and created none.
const StateNone = "none"

// review_submit body dispositions (ReviewSubmitResult.Body).
const (
	// BodyWritten: a section for this head was added to the review body.
	BodyWritten = "written"
	// BodyKept: a section for this head already existed, so the supplied body
	// text was NOT applied.
	BodyKept = "kept"
	// BodyAbsent: no body was supplied, so no section was written.
	BodyAbsent = "absent"
	// BodyDismissed: the section for this head was written before and the
	// operator deleted it; it is not written again.
	BodyDismissed = "dismissed"
	// BodySkippedExtraPending: more than one pending review exists, so body
	// updates are refused.
	BodySkippedExtraPending = "skipped_extra_pending"
	// BodyTooLarge: the section would exceed the host's review body limit.
	BodyTooLarge = "too_large"
)

// ReviewSubmitResult is the review_submit op's output (contract 9.1).
//
// ReviewID/State/URL name the pending review that was used or created: State
// is "pending", or StateNone (with an empty ReviewID and no URL) when the run
// wrote nothing and found no pending review.
//
// Added, AlreadyPresent and Dismissed count the request's comments (after
// identical items are merged) by disposition: written by this run, already on
// the host, or deleted by the operator after an earlier run wrote them.
// ExtraPendingReviews is the number of pending reviews beyond the one used
// (normally 0). LastAppend is the backend's own record of the latest append to
// the PR, as of the end of this run.
type ReviewSubmitResult struct {
	ReviewID            string      `json:"review_id"`
	State               string      `json:"state"`
	HeadSHA             string      `json:"head_sha"`
	AsOf                string      `json:"as_of"`
	Status              string      `json:"status"`
	Added               int         `json:"added"`
	AlreadyPresent      int         `json:"already_present"`
	Dismissed           int         `json:"dismissed"`
	Body                string      `json:"body"`
	ExtraPendingReviews int         `json:"extra_pending_reviews"`
	LastAppend          *LastAppend `json:"last_append,omitempty"`
	URL                 string      `json:"url,omitempty"`
}

// ReviewSubmitter is an OPTIONAL capability of a pr backend: it puts content
// into the acting identity's PENDING review, creating the review when there is
// none and appending to it otherwise, never deleting, replacing or submitting
// anything. It is deliberately not part of
// Provider; NewDispatchTable registers the review_submit op only when the
// provider type-asserts to it, so other backends keep compiling. Errors
// MUST be wrapped with scriptout.Err* from INV-ERR-1's taxonomy.
type ReviewSubmitter interface {
	SubmitReview(ctx context.Context, req ReviewSubmitRequest) (ReviewSubmitResult, error)
}

// PendingReviewRequest is the review_pending op's input (contract 9.1a). ID is
// the PR id, in the same style as every other pr op's args.
type PendingReviewRequest struct {
	ID string `json:"id"`
}

// PendingReviewComment is one inline comment of the acting identity's pending
// review (contract 9.1a). Line is 0 when the host reports no line.
// OriginalCommit is the commit the comment was made on, the only value that
// says which head the comment belongs to.
type PendingReviewComment struct {
	ID             string `json:"id"`
	Path           string `json:"path"`
	Line           int    `json:"line"`
	Body           string `json:"body"`
	OriginalCommit string `json:"original_commit"`
}

// LastAppend is the most recent append to a PR's pending review, as the
// backend's own record of it (not read from the host). Added is the number of
// comments that append added; Head is the head it was made at.
type LastAppend struct {
	At    string `json:"at"`
	Added int    `json:"added"`
	Head  string `json:"head"`
}

// PendingReview is the structured record of the acting identity's PENDING
// review on a PR (contract 9.1a). When the identity has more than one pending
// review, the record describes the one with the lowest database id and
// ExtraPendingReviews counts the rest; every count below is over that review.
//
// CommitSHA is the REVIEW-level anchored commit: where the review was created,
// never a comment's commit. A pending review is reused across heads, so
// CommitSHA says nothing about which head the content is on.
//
//   - CommentsTotal is how many comments the review holds (paginated, none cut).
//   - CommentsAtHead is how many of them were made at the live head (their
//     original commit is the head).
//   - ReviewedHead is true when the review body holds a pg-section for the live
//     head, or any review of the identity, pending or submitted, has the live
//     head as its review-level commit.
//   - Stale is true only when CommentsAtHead is 0 AND ReviewedHead is false: a
//     run that wrote only a body section, only replies, or created the review
//     at the head is not stale; a review untouched since an older head is. The
//     connector owns this verdict.
//   - LastAppend comes from the backend's sidecar and is absent when nothing
//     was ever appended (or the sidecar cannot be read).
//
// The marker-based fields of the previous record (body_marked, per-comment
// marked, all_marked, digest_state) are gone; no consumer in this repo reads
// them from this record.
type PendingReview struct {
	ReviewID            string                 `json:"review_id"`
	DatabaseID          int64                  `json:"database_id"`
	URL                 string                 `json:"url,omitempty"`
	State               string                 `json:"state"`
	CommitSHA           string                 `json:"commit_sha"`
	Stale               bool                   `json:"stale"`
	Body                string                 `json:"body"`
	CommentsTotal       int                    `json:"comments_total"`
	CommentsAtHead      int                    `json:"comments_at_head"`
	ReviewedHead        bool                   `json:"reviewed_head"`
	ExtraPendingReviews int                    `json:"extra_pending_reviews"`
	LastAppend          *LastAppend            `json:"last_append,omitempty"`
	Comments            []PendingReviewComment `json:"comments"`
}

// PendingReviewResult is the review_pending op's output (contract 9.1a).
// Pending false with a nil Review is the explicit "none" answer: the lookup
// succeeded and the acting identity has no pending review. A failed lookup is
// never expressed here; it is an error from the closed INV-ERR-1 taxonomy.
type PendingReviewResult struct {
	Pending bool           `json:"pending"`
	HeadSHA string         `json:"head_sha"`
	AsOf    string         `json:"as_of"`
	Review  *PendingReview `json:"review,omitempty"`
}

// PendingReviewReader is an OPTIONAL capability of a pr backend: it resolves
// the acting identity's pending review on a PR. It is deliberately not part of
// Provider; NewDispatchTable registers the review_pending op only when the
// provider type-asserts to it. It MUST be read-only and fail-closed: any
// uncertainty is an error (wrapped with scriptout.Err* from INV-ERR-1's
// taxonomy), never a "none" result.
type PendingReviewReader interface {
	PendingReview(ctx context.Context, req PendingReviewRequest) (PendingReviewResult, error)
}
