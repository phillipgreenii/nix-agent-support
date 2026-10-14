package conversation

import (
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// contentOf assembles the group's content from the two Provider reads. It is the
// comment and review loops of the old backend's toSchemaPR plus the connection
// reports of its Show, copied (the new binary cannot import that package), and
// it keeps their behavior exactly: top-level (issue) comments have no owning
// review and land in Comments; inline/review-thread comments nest under their
// owning review via api.Comment.ReviewID, and one whose ReviewID is empty falls
// back to Comments. (As in the old code, an inline comment whose ReviewID names a
// review that was not returned, because the reviews cap cut it, is in no list.)
func contentOf(comments *api.CommentsResult, reviews *api.ReviewsResult) Content {
	var out Content

	byReview := make(map[string][]schema.PRComment, len(reviews.Reviews))
	for _, c := range comments.Comments {
		pc := toSchemaComment(c)
		if c.ReviewID != "" {
			byReview[c.ReviewID] = append(byReview[c.ReviewID], pc)
			continue
		}
		out.Comments = append(out.Comments, pc)
	}

	for _, r := range reviews.Reviews {
		// The reviews read always selects commit { oid }, so the oid is always
		// reported; an empty one is GitHub's null commit (the reviewed commit was
		// since deleted), carried as a pointer to "" and not as an absent field.
		commitOID := r.CommitOID
		out.Reviews = append(out.Reviews, schema.PRReview{
			ID:          r.ID,
			Author:      r.Author,
			State:       r.State,
			Body:        r.Body,
			Comments:    byReview[r.ID],
			SubmittedAt: r.SubmittedAt,
			CommitOID:   &commitOID,
		})
	}

	out.Connections = schema.PRConnections{
		Reviews:  toSchemaConnectionReport(reviews.Report),
		Threads:  toSchemaConnectionReport(comments.Threads),
		Comments: toSchemaConnectionReport(comments.CommentsReport),
	}
	return out
}

// toSchemaConnectionReport maps one api.ConnectionReport onto its wire shape.
func toSchemaConnectionReport(r api.ConnectionReport) schema.PRConnectionReport {
	return schema.PRConnectionReport{Truncated: r.Truncated, Total: r.Total, Returned: r.Returned}
}

// toSchemaComment maps one api.Comment onto its schema.PRComment shape.
func toSchemaComment(c api.Comment) schema.PRComment {
	return schema.PRComment{
		ID:             c.ID,
		Author:         c.Author,
		Body:           c.Body,
		Path:           c.Path,
		Line:           c.Line,
		ThreadID:       c.ThreadID,
		ReviewThreadID: c.ReviewThreadID,
		Resolved:       c.Resolved,
		ThreadOutdated: c.ThreadIsOutdated,
	}
}
