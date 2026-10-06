// Package api is a trimmed, local copy of pg-pr's pkg/api types — just the
// shapes internal/github's ported GitHub logic needs (Comment, Review; PR
// lives in pr.go). Copied rather than imported because packages/pg-connector's
// go.mod MUST NOT depend on packages/pg-pr (layout_convention_test.go). This is an
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

	// ReviewThreadID is the GraphQL node id (PRRT_...) of the review thread
	// the comment belongs to, taken from reviewThreads.id; empty for a
	// top-level (issue) comment. It is the value addPullRequestReviewThreadReply
	// accepts. ThreadID above is a different value (the comment's own node id)
	// that the thread-reply mutation rejects, and it keeps that meaning for its
	// existing consumers.
	ReviewThreadID string `json:"review_thread_id,omitempty"`

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
	// PR.Comments (interfaces.md's pr op catalog).
	//
	// MUST be in the SAME id space as Review.ID below: both are GitHub's
	// GraphQL node-id string (e.g. "PRR_kwDOKtdWE88AAAABL3blsA"), never the
	// REST decimal id. internal/github's ListComments reads it straight from
	// the comment's pullRequestReview.id, so the two always match; a mismatch
	// would make provider.go's join drop every inline review comment
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

// ConnectionReport states how much of one GraphQL connection a read returned.
// Truncated is true only when the connection held more items than the read's
// cap let it fetch (Total > Returned); a connection of exactly the cap is
// complete and reports Truncated false.
type ConnectionReport struct {
	Truncated bool `json:"truncated"`
	Total     int  `json:"total"`
	Returned  int  `json:"returned"`
}

// CommentsResult is the full comment context of a PR: its issue comments and
// the comments of every review thread, newest first, plus a report for the
// thread connection and for the comment set.
type CommentsResult struct {
	Comments []Comment
	// Threads reports the reviewThreads connection.
	Threads ConnectionReport
	// CommentsReport covers every returned comment (issue comments and thread
	// comments together); its Total counts only the threads that were fetched,
	// so a truncated Threads report means the real total is higher.
	CommentsReport ConnectionReport
}

// ReviewsResult is a PR's submitted and pending reviews, newest first, plus a
// report for the reviews connection.
type ReviewsResult struct {
	Reviews []Review
	Report  ConnectionReport
}
