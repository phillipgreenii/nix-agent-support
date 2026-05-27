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
