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
}

// CIRun is the JSON shape for a CI workflow run.
type CIRun struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
	Provider   string `json:"provider"`
}

// Issue is the JSON shape for an external issue (jira ticket, github issue, etc.).
type Issue struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
	URL   string `json:"url"`
}
