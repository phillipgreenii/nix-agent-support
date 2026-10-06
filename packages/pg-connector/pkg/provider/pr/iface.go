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

// ReviewComment is one inline comment of a ReviewSubmitRequest (contract
// 9.1): anchored to Path/Line on Side ("LEFT" or "RIGHT").
type ReviewComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side"`
	Body string `json:"body"`
}

// ReviewSubmitRequest is the review_submit op's input (contract 9.1). ID is
// the PR id, in the same style as every other pr op's args.
type ReviewSubmitRequest struct {
	ID               string          `json:"id"`
	HeadSHA          string          `json:"head_sha"`
	Body             string          `json:"body"`
	Comments         []ReviewComment `json:"comments"`
	SupersedePending bool            `json:"supersede_pending"`
}

// SupersedeOutcome reports the delete half of a supersede_pending request,
// kept in its original shape alongside the status fields below (contract
// 9.1). Attempted is true when a delete was issued; Deleted when it
// succeeded; Error carries a failed delete's message. It is a mirror of what
// status already says, so a caller reads status, not this.
type SupersedeOutcome struct {
	Attempted bool   `json:"attempted"`
	Deleted   bool   `json:"deleted"`
	Error     string `json:"error,omitempty"`
}

// review_submit statuses. With supersede_pending set, the op emits exactly one
// of the four (contract 9.1); without it a successful post is StatusPosted.
const (
	// StatusPosted: no pending review existed; a new PENDING review was posted.
	StatusPosted = "posted"
	// StatusSkipped: a pending review already exists at the head being
	// reviewed; nothing was posted. Callers treat it as success.
	StatusSkipped = "skipped"
	// StatusReplaced: a stale, provably unedited, fully marked pending review
	// was archived and deleted, and a new one was posted.
	StatusReplaced = "replaced"
	// StatusBlockedHumanPending: a stale pending review could not be removed;
	// the review is untouched, nothing was posted, and a human is needed.
	StatusBlockedHumanPending = "blocked_human_pending"
)

// review_submit reasons, carried in ReviewSubmitResult.Reason.
const (
	// ReasonSameHead accompanies StatusSkipped.
	ReasonSameHead = "pending_review_exists_same_head"
	// The following accompany StatusBlockedHumanPending.
	ReasonDetectionFailed = "detection_failed"
	ReasonHumanEdited     = "human_edited"
	ReasonArchiveFailed   = "archive_failed"
	ReasonDeleteRefused   = "delete_refused"
)

// PendingReviewRef identifies a pending review: the one a skip points at, the
// one a block left untouched, or the one a replace removed.
type PendingReviewRef struct {
	ReviewID   string `json:"review_id"`
	DatabaseID int64  `json:"database_id"`
	URL        string `json:"url,omitempty"`
	CommitSHA  string `json:"commit_sha"`
}

// SupersededReview is the full content of a pending review that a replace
// removed, returned so the caller holds it even if the archive file is lost.
// ArchivePath is where the same content was persisted BEFORE the delete.
type SupersededReview struct {
	PendingReviewRef
	ArchivePath string              `json:"archive_path"`
	Body        string              `json:"body"`
	Comments    []SupersededComment `json:"comments"`
}

// SupersededComment is one inline comment of a pending review that a replace
// removed. It keeps the per-comment bot marker the replace guard read (Marked),
// which the review_pending record no longer carries; it goes away with the
// replace path itself.
type SupersededComment struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Body   string `json:"body"`
	Marked bool   `json:"marked"`
}

// ReviewSubmitResult is the review_submit op's output (contract 9.1).
//
// Status is one of the Status* constants. ReviewID/State name the pending
// review that exists for the PR at HeadSHA after the call: the new one for
// posted and replaced, the existing one for skipped, empty for
// blocked_human_pending (nothing was posted). Reason and Message are set for
// skipped and blocked_human_pending. PendingReview names the existing review
// a skip points at or a block left untouched; Superseded is set only for
// replaced. Supersede is nil (omitted) when supersede_pending was not set.
type ReviewSubmitResult struct {
	ReviewID      string            `json:"review_id"`
	State         string            `json:"state"`
	HeadSHA       string            `json:"head_sha"`
	AsOf          string            `json:"as_of"`
	Status        string            `json:"status,omitempty"`
	Reason        string            `json:"reason,omitempty"`
	Message       string            `json:"message,omitempty"`
	PendingReview *PendingReviewRef `json:"pending_review,omitempty"`
	Superseded    *SupersededReview `json:"superseded,omitempty"`
	Supersede     *SupersedeOutcome `json:"supersede,omitempty"`
}

// ReviewSubmitter is an OPTIONAL capability of a pr backend: it posts a
// PENDING review anchored to req.HeadSHA. It is deliberately not part of
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
