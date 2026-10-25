package groups

import (
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// SummaryContent is the stored JSON of the summary group: what a listing
// returns, with the origin's updated text kept as the revalidation token. The
// comment summary (Issue.CommentCount, Issue.LatestCommentAt) is counted from
// the listing's comments.
type SummaryContent struct {
	Issue schema.Issue `json:"issue"`
	// Updated is the origin's own updated text, such as
	// 2026-10-05T10:11:12.000+0000. It is the revalidation token, never compared.
	Updated string `json:"updated,omitempty"`
	// Gone marks an issue the origin no longer shows.
	Gone bool `json:"gone,omitempty"`
}

// Comment is one comment as a single-issue read returns it.
type Comment struct {
	ID      string `json:"id"`
	Author  string `json:"author,omitempty"`
	Body    string `json:"body,omitempty"`
	Created string `json:"created,omitempty"`
}

// DetailContent is the stored JSON of the detail group: what a single-issue
// read adds to the summary.
type DetailContent struct {
	Description string                   `json:"description,omitempty"`
	Comments    []Comment                `json:"comments,omitempty"`
	Deps        []schema.IssueDependency `json:"deps,omitempty"`
	// StatusChangedAt and OperatorUpdatedAt are the two operator facts; they are
	// derived, not compared.
	StatusChangedAt   string `json:"status_changed_at,omitempty"`
	OperatorUpdatedAt string `json:"operator_updated_at,omitempty"`
}
