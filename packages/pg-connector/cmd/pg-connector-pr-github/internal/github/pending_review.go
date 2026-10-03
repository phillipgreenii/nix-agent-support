package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// This file holds the review_pending read path (entity-change-flow contract
// 9.1a): resolve the acting identity's PENDING review on a PR to a structured
// record in ONE GraphQL round trip. It lives apart from github.go on purpose:
// github.go is hash-pinned against pg-pr's copy (testdata/pg-pr-drift), and
// none of this exists in pg-pr.
//
// Why GraphQL and not the REST list: REST returns pending-review comments
// only through a second call (/reviews/{id}/comments) and reports line=null
// for them, while GraphQL returns id, review-level commit, body and per-comment
// bodies together (prerequisites P1 and design correction 5,
// docs/superpowers/specs/2026-09-30-pending-review-prerequisites-results.md).
// The PR head is read from headRefOid in the SAME query because REST
// head.sha lagged a push by seconds in P4.

// pendingReviewQuery reads the PR head, the viewer, and the PENDING reviews.
// GitHub only ever returns a PENDING review to its own author (P1 part b,
// confirmed by the operator), but the viewer login is read as well so the
// caller can refuse to attribute a review to the wrong identity.
const pendingReviewQuery = `
query($owner: String!, $name: String!, $number: Int!) {
  viewer { login }
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      headRefOid
      reviews(first: 50, states: [PENDING]) {
        totalCount
        nodes {
          id
          databaseId
          url
          state
          author { login }
          commit { oid }
          body
          comments(first: 100) {
            totalCount
            nodes {
              id
              databaseId
              path
              line
              originalLine
              body
            }
          }
        }
      }
    }
  }
}
`

// PendingReviewComment is one inline comment of a pending review as read from
// the host. Line is 0 when the host reports neither line nor originalLine.
type PendingReviewComment struct {
	ID         string
	DatabaseID int64
	Path       string
	Line       int
	Body       string
}

// PendingReviewNode is the acting identity's pending review as read from the
// host. CommitOID is the REVIEW-level commit (never a comment's commit, which
// is not stable, P4); it is empty when the host reports a null commit.
type PendingReviewNode struct {
	ID         string
	DatabaseID int64
	URL        string
	CommitOID  string
	Body       string
	Comments   []PendingReviewComment
}

// PendingReviewData is the result of GetPendingReview. Review is nil when the
// acting identity has no pending review on the PR. HeadSHA is the PR head from
// GraphQL headRefOid.
type PendingReviewData struct {
	HeadSHA string
	Review  *PendingReviewNode
}

// GetPendingReview resolves the acting identity's pending review on
// repo#number. It is strictly read-only and FAIL-CLOSED: any condition that
// leaves the answer uncertain (a host failure, a response that does not parse,
// a PR that did not resolve, a missing head, a pending review that is not the
// viewer's, more than one pending review, or a review whose comment list was
// truncated) is an error, never a "no pending review" answer. A nil Review is
// returned only when the host affirmatively reported zero pending reviews.
func (p *Provider) GetPendingReview(ctx context.Context, repo string, number int) (*PendingReviewData, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	if number <= 0 {
		return nil, fmt.Errorf("github: invalid PR number %d", number)
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("github: repo %q is not in owner/name form", repo)
	}
	raw, err := p.gh.Run(
		ctx,
		"api", "graphql",
		"-F", "query="+pendingReviewQuery,
		"-f", "owner="+owner,
		"-f", "name="+name,
		"-F", fmt.Sprintf("number=%d", number),
	)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data struct {
			Viewer struct {
				Login string `json:"login"`
			} `json:"viewer"`
			Repository struct {
				PullRequest *struct {
					HeadRefOid string `json:"headRefOid"`
					Reviews    struct {
						TotalCount int `json:"totalCount"`
						Nodes      []struct {
							ID         string `json:"id"`
							DatabaseID int64  `json:"databaseId"`
							URL        string `json:"url"`
							State      string `json:"state"`
							Author     struct {
								Login string `json:"login"`
							} `json:"author"`
							Commit *struct {
								OID string `json:"oid"`
							} `json:"commit"`
							Body     string `json:"body"`
							Comments struct {
								TotalCount int `json:"totalCount"`
								Nodes      []struct {
									ID           string `json:"id"`
									DatabaseID   int64  `json:"databaseId"`
									Path         string `json:"path"`
									Line         *int   `json:"line"`
									OriginalLine *int   `json:"originalLine"`
									Body         string `json:"body"`
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
					} `json:"reviews"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("github: parse pending-review graphql response: %w", err)
	}
	pr := resp.Data.Repository.PullRequest
	if pr == nil {
		return nil, errors.New("github: pending-review graphql response did not resolve the pull request")
	}
	if pr.HeadRefOid == "" {
		return nil, errors.New("github: pending-review graphql response carried no headRefOid")
	}
	out := &PendingReviewData{HeadSHA: pr.HeadRefOid}

	if pr.Reviews.TotalCount == 0 && len(pr.Reviews.Nodes) == 0 {
		return out, nil
	}
	if pr.Reviews.TotalCount != 1 || len(pr.Reviews.Nodes) != 1 {
		return nil, fmt.Errorf("github: expected at most one pending review for the viewer, host reported %d (%d returned)",
			pr.Reviews.TotalCount, len(pr.Reviews.Nodes))
	}
	n := pr.Reviews.Nodes[0]
	if !strings.EqualFold(n.State, "PENDING") {
		return nil, fmt.Errorf("github: pending-review query returned a review in state %q", n.State)
	}
	viewer := resp.Data.Viewer.Login
	if viewer == "" {
		return nil, errors.New("github: pending-review graphql response carried no viewer login")
	}
	if !strings.EqualFold(n.Author.Login, viewer) {
		return nil, errors.New("github: the pending review is not authored by the acting identity")
	}
	if n.Comments.TotalCount != len(n.Comments.Nodes) {
		return nil, fmt.Errorf("github: pending review comment list truncated (%d of %d returned); marker presence cannot be established",
			len(n.Comments.Nodes), n.Comments.TotalCount)
	}
	rev := &PendingReviewNode{ID: n.ID, DatabaseID: n.DatabaseID, URL: n.URL, Body: n.Body}
	if n.Commit != nil {
		rev.CommitOID = n.Commit.OID
	}
	for _, c := range n.Comments.Nodes {
		line := 0
		switch {
		case c.Line != nil:
			line = *c.Line
		case c.OriginalLine != nil:
			line = *c.OriginalLine
		}
		rev.Comments = append(rev.Comments, PendingReviewComment{
			ID: c.ID, DatabaseID: c.DatabaseID, Path: c.Path, Line: line, Body: c.Body,
		})
	}
	out.Review = rev
	return out, nil
}
