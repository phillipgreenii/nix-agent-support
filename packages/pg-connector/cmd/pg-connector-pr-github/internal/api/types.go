// Package api is a trimmed, local copy of pg-pr's pkg/api types — just the
// shapes internal/github's ported GitHub logic needs (Comment, Review; PR
// lives in pr.go). Copied rather than imported because packages/pg-connector's
// go.mod MUST NOT depend on packages/pg-pr [design: §9, §5.2]. This is an
// internal representation only: internal (the backend's pr.Provider glue)
// maps it to pkg/schema.PR at the Show boundary — pg-pr's own
// api.Issue/api.BranchInfo are out of scope here since nothing in this
// backend's ported logic uses them. CIRun (pg-pr's CI-run shape, used only
// by this backend's own now-deleted EnrichedPR bulk-fetch optimization) was
// removed as dead surface alongside it [bead pg2-lh3c4] — this backend has
// no CI-run consumer; pg-connector's `ci` capability has its own
// pkg/schema.CIRun wire shape, unrelated to this one.
package api

// Comment is the JSON shape for a PR comment.
type Comment struct {
	ID         string `json:"id"`
	Author     string `json:"author"`
	AuthorRole string `json:"author_role"`
	Body       string `json:"body"`
	Path       string `json:"path,omitempty"`
	Line       int    `json:"line,omitempty"`
	ThreadID   string `json:"thread_id,omitempty"`
	Resolved   bool   `json:"resolved"`

	// StartLine is the FIRST line of a multi-line anchor, of which Line is then
	// the LAST — GitHub's review-comment `start_line`/`line` pair (pg2-3c8mo).
	//
	// Zero means single-line, which is the only value a single-line finding can
	// carry, so `omitempty` keeps a single-line comment's wire payload
	// byte-identical to the pre-multi-line one. Write path only: GitHub's read
	// paths (ListComments, the enrich GraphQL query) do not report it, so a
	// comment read back from upstream always has StartLine == 0.
	StartLine int `json:"start_line,omitempty"`

	// CreatedAt is the comment's creation timestamp (GraphQL createdAt,
	// RFC3339). Empty when the provider does not supply one. Flows through
	// ingestion into code_comment_message.posted_at for message ordering.
	CreatedAt string `json:"created_at,omitempty"`

	// UpdatedAt is the comment's last-edit timestamp (GraphQL updatedAt,
	// RFC3339). Empty when the provider does not supply one (e.g. an
	// older-shaped cached payload recorded before this field was fetched).
	UpdatedAt string `json:"updated_at,omitempty"`

	// Review-thread staleness fields (populated for inline thread comments only).
	//
	// ThreadIsOutdated mirrors PullRequestReviewThread.isOutdated: true when
	// the thread's diff context has been pushed past (the thread is "stale").
	//
	// IsMinimized / MinimizedReason reflect the per-comment collapse state
	// GitHub exposes as "marked as outdated". MinimizedReason is an uppercase
	// string (e.g. "OUTDATED", "RESOLVED", "OFF_TOPIC").
	//
	// OriginalCommitOID is the OID of the commit the comment was originally
	// posted against — used as subject_sha when writing feedback-store entries.
	ThreadIsOutdated  bool   `json:"thread_is_outdated,omitempty"`
	IsMinimized       bool   `json:"is_minimized,omitempty"`
	MinimizedReason   string `json:"minimized_reason,omitempty"`
	OriginalCommitOID string `json:"original_commit_oid,omitempty"`

	// ReviewID is the owning review's id for an inline/review-thread comment;
	// empty for a top-level (issue) comment. Added here (internal/github's
	// own call-site adaptation, not present on pg-pr's upstream api.Comment)
	// so this backend's Show can nest a review-thread comment under its
	// owning PRReview.Comments rather than only ever flattening it into
	// PR.Comments [design: §2, §6.1].
	//
	// MUST be in the SAME id space as Review.ID below: both are GitHub's
	// GraphQL node-id string (e.g. "PRR_kwDOKtdWE88AAAABL3blsA"), never the
	// REST decimal id. GitHub's pulls-comments endpoint (this field's
	// upstream source) only ever exposes the REST decimal
	// pull_request_review_id, so internal/github's ListComments translates
	// it to the matching review's GraphQL node id (via
	// reviewNodeIDsByDatabaseID) before populating this field. Earlier, this
	// field carried the untranslated decimal id while Review.ID carried the
	// GraphQL node id (post the 2b93d895/pg2-6hkl5 crash fix) — the two
	// never matched, so provider.go's join silently dropped every inline
	// review comment instead of nesting it or falling back to PR.Comments
	// [bug pg2-flaes].
	ReviewID string `json:"review_id,omitempty"`
}

// Review is the JSON shape for a PR review summary.
type Review struct {
	// ID is GitHub's GraphQL node-id string (e.g. "PRR_..."), matching
	// Comment.ReviewID's id space above [bug pg2-flaes] — not the REST
	// decimal id GitHub also exposes for reviews.
	ID       string    `json:"id"`
	Author   string    `json:"author"`
	State    string    `json:"state"`
	Body     string    `json:"body"`
	Comments []Comment `json:"comments,omitempty"`
	// CommitOID is the SHA of the commit the review was submitted against.
	// Populated by the GitHub GraphQL path; empty otherwise.
	CommitOID string `json:"commit_oid,omitempty"`
	// SubmittedAt is the RFC3339 timestamp when the review was submitted.
	SubmittedAt string `json:"submitted_at,omitempty"`
}
