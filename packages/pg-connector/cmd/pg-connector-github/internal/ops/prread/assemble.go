package prread

import (
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/conversation"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/detail"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// ToSchemaPR assembles the show answer's schema.PR from the stored summary,
// detail and conversation groups, with the field semantics of the old backend's
// toSchemaPR (cmd/pg-connector-pr-github/internal/provider.go):
//
//   - summary supplies the identity, title, body, branches, author, URL, draft
//     flag, labels, head and base commits, mergeable, review decision, updatedAt
//     and node id; state is lower case, as the old toAPI writes it;
//   - detail (when not nil) supplies merged, additions, deletions, changed_files,
//     merge_state_status and review_requests;
//   - conversation (when not nil) supplies comments, reviews (each with its own
//     inline comments nested, commit_oid a pointer) and the connections report.
//
// comment_count is the number of top-level issue comments of the conversation (an
// entry with neither a thread nor a path, so an inline comment that could not be
// joined to its review is not counted), the summary's count when the
// conversation is absent; review_count is len(reviews) when the conversation is
// present, the summary's otherwise; review_thread_count stays zero, as the old
// show leaves it (the threads are in the reviews); label_count is len(labels);
// base_sha is the summary's base commit; checks_rollup is the summary's checks
// state folded by the same function the list uses.
//
// as_of and stale are NOT set here: answer.ApplyPR sets them from the oldest
// fetched_at of the groups served and the answer's staleness. The final
// parameter is the time of assembly; the assembler stamps nothing with it, so it
// is accepted (the signature is the one the assembly packet binds) and unused.
func ToSchemaPR(id string, sum summary.Summary, d *detail.Detail, c *conversation.Content, _ time.Time) *schema.PR {
	out := &schema.PR{
		ID:     id,
		Repo:   sum.Repo,
		Number: sum.Number,
		Title:  sum.Title,
		State:  strings.ToLower(sum.State),
		Branch: sum.HeadRefName,
		Base:   sum.BaseRefName,
		Author: sum.Author,
		URL:    sum.URL,
		Draft:  sum.IsDraft,
		Body:   sum.Body,

		HeadSHA:      sum.HeadRefOid,
		BaseSHA:      sum.BaseRefOid,
		Mergeable:    sum.Mergeable,
		ChecksRollup: github.ChecksRollupFor(sum.ChecksState),

		NodeID:         sum.NodeID,
		UpdatedAt:      sum.UpdatedAt,
		ReviewDecision: sum.ReviewDecision,
		CommentCount:   sum.CommentCount,
		ReviewCount:    sum.ReviewCount,
		// The show path leaves the thread count zero: it carries the threads.
		ReviewThreadCount: 0,
	}
	if len(sum.Labels) > 0 {
		out.Labels = append([]string(nil), sum.Labels...)
	}
	out.LabelCount = len(sum.Labels)

	if d != nil {
		out.Merged = d.Merged
		out.Additions = d.Additions
		out.Deletions = d.Deletions
		out.ChangedFiles = d.ChangedFiles
		out.MergeStateStatus = d.MergeStateStatus
		if len(d.ReviewRequests) > 0 {
			out.ReviewRequests = append([]string(nil), d.ReviewRequests...)
		}
	}

	if c != nil {
		if len(c.Comments) > 0 {
			out.Comments = append([]schema.PRComment(nil), c.Comments...)
		}
		if len(c.Reviews) > 0 {
			out.Reviews = make([]schema.PRReview, len(c.Reviews))
			for i, r := range c.Reviews {
				out.Reviews[i] = r
				if r.CommitOID != nil {
					oid := *r.CommitOID
					out.Reviews[i].CommitOID = &oid
				}
				if len(r.Comments) > 0 {
					out.Reviews[i].Comments = append([]schema.PRComment(nil), r.Comments...)
				}
			}
		}
		conns := c.Connections
		out.Connections = &conns
		out.CommentCount = topLevelCommentCount(c.Comments)
		out.ReviewCount = len(c.Reviews)
	}
	return out
}

// topLevelCommentCount is schema.PR.CommentCount for a stored conversation: the
// entries with no thread and no path, which are exactly the issue
// (conversation) comments. len(comments) is NOT that count: an inline review
// comment whose review could not be joined also sits in the list. It is the
// old backend's topLevelCommentCount for a non-nil comment list.
func topLevelCommentCount(comments []schema.PRComment) int {
	n := 0
	for _, c := range comments {
		if c.ThreadID == "" && c.Path == "" {
			n++
		}
	}
	return n
}
