package github

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
//
// The lookup tolerates any number of pending reviews of the viewer (the lowest
// databaseId is first; the caller reports the rest as extra) and pages every
// connection it reads. A comment's head is its originalCommit.oid: commit.oid,
// REST commit_id and the review-level commit are not stable once the head
// advances and are never used to decide which head a comment is on.
//
// Read shape. ONE GraphQL document returns the viewer, the live head, every
// pending review of the viewer with the first page of its comments, a light
// listing of the submitted reviews (id, author, review-level commit, comment
// count) and the first page of the PR's review threads (ids only). Follow-up
// documents page whatever was cut off: further reviews, further comments of a
// review, further threads. A review thread is not reachable from a comment in
// the schema, so each comment's review-thread id is resolved by reading the
// threads' comment ids; a comment whose thread cannot be found leaves
// ReviewThreadID empty. Comments of submitted reviews are read only for the
// viewer's own submitted reviews.

const pendingCommentSel = `
              id
              databaseId
              path
              line
              originalLine
              body
              originalCommit { oid }
`

const pendingReviewSel = `
          id
          databaseId
          url
          state
          author { login }
          commit { oid }
          body
          comments(first: 100) {
            totalCount
            pageInfo { hasNextPage endCursor }
            nodes {` + pendingCommentSel + `            }
          }
`

const submittedReviewSel = `
          id
          databaseId
          state
          author { login }
          commit { oid }
          comments(first: 1) { totalCount }
`

// submittedStates are the review states a viewer's review can have once it
// has been submitted.
const submittedStates = "COMMENTED, APPROVED, CHANGES_REQUESTED, DISMISSED"

// pendingReviewQuery is the main read: the viewer, the head, the viewer's
// PENDING reviews, the submitted reviews (light) and the first page of the
// review threads' comment ids. GitHub only ever returns a PENDING review to
// its own author, but the viewer login is read as well so the caller refuses
// to attribute a review to the wrong identity.
const pendingReviewQuery = `
query($owner: String!, $name: String!, $number: Int!) {
  viewer { login }
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      headRefOid
      pending: reviews(first: 100, states: [PENDING]) {
        totalCount
        pageInfo { hasNextPage endCursor }
        nodes {` + pendingReviewSel + `        }
      }
      submitted: reviews(first: 100, states: [` + submittedStates + `]) {
        totalCount
        pageInfo { hasNextPage endCursor }
        nodes {` + submittedReviewSel + `        }
      }
      reviewThreads(first: 100) {
        totalCount
        pageInfo { hasNextPage endCursor }
        nodes {
          id
          comments(first: 100) {
            pageInfo { hasNextPage endCursor }
            nodes { id }
          }
        }
      }
    }
  }
}
`

// pendingReviewsPageQuery and submittedReviewsPageQuery read the pages of the
// two review connections after the first.
const pendingReviewsPageQuery = `
query($owner: String!, $name: String!, $number: Int!, $after: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviews(first: 100, after: $after, states: [PENDING]) {
        totalCount
        pageInfo { hasNextPage endCursor }
        nodes {` + pendingReviewSel + `        }
      }
    }
  }
}
`

const submittedReviewsPageQuery = `
query($owner: String!, $name: String!, $number: Int!, $after: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviews(first: 100, after: $after, states: [` + submittedStates + `]) {
        totalCount
        pageInfo { hasNextPage endCursor }
        nodes {` + submittedReviewSel + `        }
      }
    }
  }
}
`

// reviewCommentsPageQuery reads a page of ONE review's comments; with no
// cursor it is the first page.
const reviewCommentsPageQuery = `
query($reviewId: ID!, $after: String) {
  node(id: $reviewId) {
    ... on PullRequestReview {
      comments(first: 100, after: $after) {
        totalCount
        pageInfo { hasNextPage endCursor }
        nodes {` + pendingCommentSel + `        }
      }
    }
  }
}
`

// threadIDsPageQuery and threadCommentIDsPageQuery page the review threads and
// the comment ids inside one thread.
const threadIDsPageQuery = `
query($owner: String!, $name: String!, $number: Int!, $after: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviewThreads(first: 100, after: $after) {
        totalCount
        pageInfo { hasNextPage endCursor }
        nodes {
          id
          comments(first: 100) {
            pageInfo { hasNextPage endCursor }
            nodes { id }
          }
        }
      }
    }
  }
}
`

const threadCommentIDsPageQuery = `
query($threadId: ID!, $after: String) {
  node(id: $threadId) {
    ... on PullRequestReviewThread {
      comments(first: 100, after: $after) {
        pageInfo { hasNextPage endCursor }
        nodes { id }
      }
    }
  }
}
`

// maxLookupNodes bounds each connection the lookup reads. The lookup is
// fail-closed, so a connection that holds more than this is an error rather
// than a silently shortened list.
const maxLookupNodes = 10000

// PendingReviewComment is one inline comment of a review as read from the
// host. Line is 0 when the host reports neither line nor originalLine.
// OriginalCommitOID is the commit the comment was made on (originalCommit.oid),
// the only value that says which head a comment belongs to. ReviewThreadID is
// the review thread's node id, empty when the thread could not be found.
type PendingReviewComment struct {
	ID                string
	DatabaseID        int64
	Path              string
	Line              int
	Body              string
	OriginalCommitOID string
	ReviewThreadID    string
}

// PendingReviewNode is one of the acting identity's pending reviews as read
// from the host, with ALL of its comments. CommitOID is the REVIEW-level
// commit (never a comment's commit); it is empty when the host reports a null
// commit.
type PendingReviewNode struct {
	ID         string
	DatabaseID int64
	URL        string
	CommitOID  string
	Body       string
	Comments   []PendingReviewComment
}

// SubmittedReviewNode is a review the acting identity has already submitted:
// its review-level commit and its comments (the viewer's own, read in full).
type SubmittedReviewNode struct {
	ID         string
	DatabaseID int64
	CommitOID  string
	Comments   []PendingReviewComment
}

// PendingReviewData is the result of GetPendingReview. HeadSHA is the PR head
// from GraphQL headRefOid. Reviews holds EVERY pending review of the acting
// identity, ordered so the lowest databaseId is first (empty when there is
// none). Submitted holds the identity's own submitted reviews.
type PendingReviewData struct {
	HeadSHA   string
	Reviews   []PendingReviewNode
	Submitted []SubmittedReviewNode
}

// Lowest returns the pending review the lookup reports (the lowest
// databaseId), or nil when there is none.
func (d *PendingReviewData) Lowest() *PendingReviewNode {
	if d == nil || len(d.Reviews) == 0 {
		return nil
	}
	return &d.Reviews[0]
}

type gqlPendingComment struct {
	ID             string `json:"id"`
	DatabaseID     int64  `json:"databaseId"`
	Path           string `json:"path"`
	Line           *int   `json:"line"`
	OriginalLine   *int   `json:"originalLine"`
	Body           string `json:"body"`
	OriginalCommit *struct {
		OID string `json:"oid"`
	} `json:"originalCommit"`
}

type gqlLookupReview struct {
	ID         string   `json:"id"`
	DatabaseID int64    `json:"databaseId"`
	URL        string   `json:"url"`
	State      string   `json:"state"`
	Author     gqlLogin `json:"author"`
	Commit     *struct {
		OID string `json:"oid"`
	} `json:"commit"`
	Body     string                      `json:"body"`
	Comments connPage[gqlPendingComment] `json:"comments"`
}

type gqlID struct {
	ID string `json:"id"`
}

type gqlLookupThread struct {
	ID       string          `json:"id"`
	Comments connPage[gqlID] `json:"comments"`
}

type lookupEnvelope struct {
	Viewer     gqlLogin `json:"viewer"`
	Repository struct {
		PullRequest *struct {
			HeadRefOid    string                    `json:"headRefOid"`
			Pending       connPage[gqlLookupReview] `json:"pending"`
			Submitted     connPage[gqlLookupReview] `json:"submitted"`
			ReviewThreads connPage[gqlLookupThread] `json:"reviewThreads"`
		} `json:"pullRequest"`
	} `json:"repository"`
}

func toLookupComment(c gqlPendingComment) PendingReviewComment {
	line := 0
	switch {
	case c.Line != nil:
		line = *c.Line
	case c.OriginalLine != nil:
		line = *c.OriginalLine
	}
	out := PendingReviewComment{ID: c.ID, DatabaseID: c.DatabaseID, Path: c.Path, Line: line, Body: c.Body}
	if c.OriginalCommit != nil {
		out.OriginalCommitOID = c.OriginalCommit.OID
	}
	return out
}

// completeConnection returns every node of a connection whose first page was
// already read, paging the rest through more, with the connection's total. A
// connection that holds more than limit nodes, or whose count disagrees with
// what was read, is an error: the lookup is fail-closed.
func completeConnection[T any](what string, first connPage[T], limit int, more func(after string) (connPage[T], error)) ([]T, error) {
	nodes := append([]T(nil), first.Nodes...)
	total := first.TotalCount
	if first.PageInfo.HasNextPage {
		if first.PageInfo.EndCursor == "" {
			return nil, fmt.Errorf("github: %s reports another page but gave no way to reach it", what)
		}
		rest, t, err := fetchConnection(limit-len(nodes), first.PageInfo.EndCursor, more)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, rest...)
		total = t
	}
	if total > len(nodes) {
		return nil, fmt.Errorf("github: %s truncated (%d of %d read)", what, len(nodes), total)
	}
	return nodes, nil
}

// GetPendingReview resolves the acting identity's pending reviews on
// repo#number. It is strictly read-only and FAIL-CLOSED: any condition that
// leaves the answer uncertain (a host failure, a response that does not parse,
// a PR that did not resolve, a missing head or viewer, a pending review that
// is not the viewer's, a connection that could not be read in full) is an
// error, never a "no pending review" answer. An empty Reviews is returned only
// when the host affirmatively reported zero pending reviews. More than one
// pending review is NOT an error: they are all returned, lowest databaseId
// first.
func (p *Provider) GetPendingReview(ctx context.Context, repo string, number int) (*PendingReviewData, error) {
	owner, name, err := splitRepo(repo, number)
	if err != nil {
		return nil, err
	}
	vars := map[string]string{"owner": owner, "name": name}
	var d lookupEnvelope
	if err := p.runGraphQL(ctx, pendingReviewQuery, vars, number, "", &d); err != nil {
		return nil, fmt.Errorf("github: pending-review graphql: %w", err)
	}
	pr := d.Repository.PullRequest
	if pr == nil {
		return nil, errors.New("github: pending-review graphql response did not resolve the pull request")
	}
	if pr.HeadRefOid == "" {
		return nil, errors.New("github: pending-review graphql response carried no headRefOid")
	}
	viewer := d.Viewer.Login
	if viewer == "" {
		return nil, errors.New("github: pending-review graphql response carried no viewer login")
	}
	out := &PendingReviewData{HeadSHA: pr.HeadRefOid}

	pending, err := completeConnection("pending reviews", pr.Pending, maxLookupNodes, func(after string) (connPage[gqlLookupReview], error) {
		var dd prQueryEnvelope[struct {
			Reviews connPage[gqlLookupReview] `json:"reviews"`
		}]
		if err := p.runGraphQL(ctx, pendingReviewsPageQuery, vars, number, after, &dd); err != nil {
			return connPage[gqlLookupReview]{}, err
		}
		if dd.Repository.PullRequest == nil {
			return connPage[gqlLookupReview]{}, errPRNotResolved(repo, number)
		}
		return dd.Repository.PullRequest.Reviews, nil
	})
	if err != nil {
		return nil, err
	}
	for _, n := range pending {
		if !strings.EqualFold(n.State, "PENDING") {
			return nil, fmt.Errorf("github: pending-review query returned a review in state %q", n.State)
		}
		if !strings.EqualFold(n.Author.Login, viewer) {
			return nil, errors.New("github: a pending review is not authored by the acting identity")
		}
		comments, err := p.reviewComments(ctx, n.ID, &n.Comments)
		if err != nil {
			return nil, err
		}
		rev := PendingReviewNode{ID: n.ID, DatabaseID: n.DatabaseID, URL: n.URL, Body: n.Body, Comments: comments}
		if n.Commit != nil {
			rev.CommitOID = n.Commit.OID
		}
		out.Reviews = append(out.Reviews, rev)
	}
	sort.SliceStable(out.Reviews, func(i, j int) bool { return out.Reviews[i].DatabaseID < out.Reviews[j].DatabaseID })

	submitted, err := completeConnection("submitted reviews", pr.Submitted, maxLookupNodes, func(after string) (connPage[gqlLookupReview], error) {
		var dd prQueryEnvelope[struct {
			Reviews connPage[gqlLookupReview] `json:"reviews"`
		}]
		if err := p.runGraphQL(ctx, submittedReviewsPageQuery, vars, number, after, &dd); err != nil {
			return connPage[gqlLookupReview]{}, err
		}
		if dd.Repository.PullRequest == nil {
			return connPage[gqlLookupReview]{}, errPRNotResolved(repo, number)
		}
		return dd.Repository.PullRequest.Reviews, nil
	})
	if err != nil {
		return nil, err
	}
	for _, n := range submitted {
		if !strings.EqualFold(n.Author.Login, viewer) {
			continue
		}
		sub := SubmittedReviewNode{ID: n.ID, DatabaseID: n.DatabaseID}
		if n.Commit != nil {
			sub.CommitOID = n.Commit.OID
		}
		if n.Comments.TotalCount > 0 {
			// The listing carried a count only; read the comments in full.
			if sub.Comments, err = p.reviewComments(ctx, n.ID, nil); err != nil {
				return nil, err
			}
		}
		out.Submitted = append(out.Submitted, sub)
	}

	if err := p.resolveThreads(ctx, repo, vars, number, pr.ReviewThreads, out); err != nil {
		return nil, err
	}
	return out, nil
}

// reviewComments returns ALL comments of one review. first, when non-nil, is
// the first page the listing already carried; nil starts from the beginning.
func (p *Provider) reviewComments(ctx context.Context, reviewID string, first *connPage[gqlPendingComment]) ([]PendingReviewComment, error) {
	fetch := func(after string) (connPage[gqlPendingComment], error) {
		var dd struct {
			Node *struct {
				Comments connPage[gqlPendingComment] `json:"comments"`
			} `json:"node"`
		}
		if err := p.runGraphQL(ctx, reviewCommentsPageQuery, map[string]string{"reviewId": reviewID}, 0, after, &dd); err != nil {
			return connPage[gqlPendingComment]{}, err
		}
		if dd.Node == nil {
			return connPage[gqlPendingComment]{}, fmt.Errorf("github: review %s did not resolve", reviewID)
		}
		return dd.Node.Comments, nil
	}
	var raw []gqlPendingComment
	var err error
	if first != nil {
		raw, err = completeConnection("review comments", *first, maxLookupNodes, fetch)
	} else {
		var total int
		raw, total, err = fetchConnection(maxLookupNodes, "", fetch)
		if err == nil && total > len(raw) {
			err = fmt.Errorf("github: review comments truncated (%d of %d read)", len(raw), total)
		}
	}
	if err != nil {
		return nil, err
	}
	out := make([]PendingReviewComment, 0, len(raw))
	for _, c := range raw {
		out = append(out, toLookupComment(c))
	}
	return out, nil
}

// resolveThreads fills ReviewThreadID on every comment of out by reading the
// review threads' comment ids. It stops reading as soon as every comment has
// its thread, so a PR with many unrelated threads costs one page.
func (p *Provider) resolveThreads(ctx context.Context, repo string, vars map[string]string, number int, first connPage[gqlLookupThread], out *PendingReviewData) error {
	need := map[string]struct{}{}
	for _, r := range out.Reviews {
		for _, c := range r.Comments {
			need[c.ID] = struct{}{}
		}
	}
	for _, r := range out.Submitted {
		for _, c := range r.Comments {
			need[c.ID] = struct{}{}
		}
	}
	if len(need) == 0 {
		return nil
	}
	threadOf := map[string]string{}
	absorb := func(threadID string, ids []gqlID) {
		for _, c := range ids {
			if _, ok := need[c.ID]; ok {
				threadOf[c.ID] = threadID
			}
		}
	}
	done := func() bool { return len(threadOf) >= len(need) }
	visit := func(t gqlLookupThread) error {
		absorb(t.ID, t.Comments.Nodes)
		if done() || !t.Comments.PageInfo.HasNextPage {
			return nil
		}
		_, _, err := fetchConnection(maxLookupNodes, t.Comments.PageInfo.EndCursor, func(after string) (connPage[gqlID], error) {
			var dd struct {
				Node *struct {
					Comments connPage[gqlID] `json:"comments"`
				} `json:"node"`
			}
			if err := p.runGraphQL(ctx, threadCommentIDsPageQuery, map[string]string{"threadId": t.ID}, 0, after, &dd); err != nil {
				return connPage[gqlID]{}, err
			}
			if dd.Node == nil {
				return connPage[gqlID]{}, fmt.Errorf("github: review thread %s did not resolve", t.ID)
			}
			absorb(t.ID, dd.Node.Comments.Nodes)
			page := dd.Node.Comments
			if done() {
				page.PageInfo.HasNextPage = false
			}
			return page, nil
		})
		return err
	}
	for _, t := range first.Nodes {
		if err := visit(t); err != nil {
			return err
		}
	}
	if !done() && first.PageInfo.HasNextPage {
		_, _, err := fetchConnection(maxLookupNodes, first.PageInfo.EndCursor, func(after string) (connPage[gqlLookupThread], error) {
			var dd prQueryEnvelope[struct {
				ReviewThreads connPage[gqlLookupThread] `json:"reviewThreads"`
			}]
			if err := p.runGraphQL(ctx, threadIDsPageQuery, vars, number, after, &dd); err != nil {
				return connPage[gqlLookupThread]{}, err
			}
			if dd.Repository.PullRequest == nil {
				return connPage[gqlLookupThread]{}, errPRNotResolved(repo, number)
			}
			page := dd.Repository.PullRequest.ReviewThreads
			for _, t := range page.Nodes {
				if err := visit(t); err != nil {
					return connPage[gqlLookupThread]{}, err
				}
			}
			if done() {
				page.PageInfo.HasNextPage = false
			}
			return page, nil
		})
		if err != nil {
			return err
		}
	}
	for i := range out.Reviews {
		for j := range out.Reviews[i].Comments {
			out.Reviews[i].Comments[j].ReviewThreadID = threadOf[out.Reviews[i].Comments[j].ID]
		}
	}
	for i := range out.Submitted {
		for j := range out.Submitted[i].Comments {
			out.Submitted[i].Comments[j].ReviewThreadID = threadOf[out.Submitted[i].Comments[j].ID]
		}
	}
	return nil
}
