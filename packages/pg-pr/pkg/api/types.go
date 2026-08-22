// Package api defines the JSON shapes used across the pg-pr CLI surface and
// the script-out provider protocol. Stubs only in Phase 0; fields are added
// in Phases 1–4 as the surface lands.
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
}

// Review is the JSON shape for a PR review summary.
type Review struct {
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

// CIRun is the JSON shape for a CI workflow run.
type CIRun struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
	Provider   string `json:"provider"`
	// HeadSHA is the commit SHA the run was triggered against. Used as
	// subject_sha in the feedback store so ci-failure rows are per-revision.
	HeadSHA string `json:"head_sha,omitempty"`
}

// Issue is the JSON shape for an external issue (jira ticket, github issue, etc.).
type Issue struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
	URL   string `json:"url"`

	// Priority is the issue's priority string as returned by the tracker (e.g.
	// "High", "Medium", "Low"). Empty when the provider does not supply it.
	// Added in pg2-jpfw.4 (additive; existing callers are unaffected).
	Priority string `json:"priority,omitempty"`

	// Labels is the list of label strings attached to the issue. Empty when the
	// provider does not supply them or when the issue has no labels.
	// Added in pg2-jpfw.4 (additive; existing callers are unaffected).
	Labels []string `json:"labels,omitempty"`

	// IssueType is the issue-type string as returned by the tracker (e.g.
	// "Bug", "Story", "Incident"). Empty when the provider does not supply it.
	// Added in pg2-jpfw.4 (additive; existing callers are unaffected).
	IssueType string `json:"issue_type,omitempty"`
}
