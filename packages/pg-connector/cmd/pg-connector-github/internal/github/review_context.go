package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/eventlog"
)

// This file reads the full review context of one PR for `pr show`: every
// review with its body and state (the viewer's own PENDING review included),
// every review thread with its REAL review-thread node id and its resolved and
// outdated flags, and all of the thread comments, plus the PR's issue
// comments.
//
// Paging and caps. Each connection is read in pages of 100
// and the read stops at its cap: maxReviews reviews, maxThreads threads, and
// maxComments comments per PR (issue comments and thread comments share that
// one budget). A connection that held more than its cap let the read fetch is
// reported through api.ConnectionReport with Truncated true, Total (the
// connection's totalCount) and Returned; nothing is dropped silently.
//
// Ordering. GitHub's GraphQL schema gives NO server-side ordering for
// PullRequest.reviews (arguments: after, author, before, first, last, states)
// nor for PullRequest.reviewThreads or a thread's comments (arguments: after,
// before, first, last). Only PullRequest.comments takes an orderBy, and that
// accepts only UPDATED_AT, which is not creation order; it is left unused so
// the three connections behave alike. This was checked for this change against
// the argument lists above; the pg-pr enrichment query (packages/pg-pr/pkg/
// provider/vcs/github/enrich.go) records the same fact from live use and
// paginates reviewThreads from creation time for that reason. So pages are
// read in server order (oldest first in practice) up to the cap and what was
// fetched is sorted newest-first on the client. A truncated connection is
// therefore the first items the server returned, never "the newest 1000", and
// callers MUST NOT treat a truncated set as complete.
//
// Time budget (bead pg2-vq8ii). `pr show` reads GetPR, the threads, the issue
// comments and the reviews serially under ONE whole-call deadline
// (scriptout.DefaultBackendTimeout, 30s). The thread read is by far the
// heaviest (a page is 100 threads each carrying 100 comments), so on a PR with
// hundreds of threads it used up the whole deadline and the call died while
// reading the cheap issue comments that come after it ("list issue comments:
// deadline exceeded", 14 desk-pr gather failures on 2026-10-19). Each
// connection is now given a share of the time the call has left when the read
// starts (threadsTimeShare, issueCommentsTimeShare, reviewsTimeShare); once a
// connection's share is spent it reads no further page and what it has is
// returned as a truncated connection (ConnectionReport.Truncated with
// Total > Returned), exactly like a cap cut, so the later connections still get
// their first page and the call succeeds with a report that says what is
// missing. A context with no deadline (a test) has no time bound.
const (
	maxReviews  = 1000
	maxThreads  = 1000
	maxComments = 1000
)

// The share of the call's remaining time a connection may spend paging, as a
// fraction of the time left when the read began; see the file comment. The
// first page of a connection is always read, whatever its share.
const (
	// threadsTimeShare covers the review threads and the follow-up pages of
	// their comments (the heaviest reads).
	threadsTimeShare = 0.5
	// issueCommentsTimeShare is the point, in the same measure, by which the
	// issue-comment paging must be done, leaving the rest for the reviews.
	issueCommentsTimeShare = 0.8
	// reviewsTimeShare bounds the reviews read, which runs last and normally
	// needs a single page.
	reviewsTimeShare = 0.8
)

// pageBound is a point in time after which a connection reads no further page.
// The zero value is unbounded.
type pageBound struct {
	until time.Time
	now   func() time.Time
}

// spent reports whether the bound has passed.
func (b pageBound) spent() bool {
	if b.until.IsZero() {
		return false
	}
	now := b.now
	if now == nil {
		now = time.Now
	}
	return !now().Before(b.until)
}

// boundAt returns the bound that is share (0..1) of the way from now to ctx's
// deadline, or an unbounded one when ctx has no deadline.
func (p *Provider) boundAt(ctx context.Context, share float64) pageBound {
	now := p.now
	if now == nil {
		now = time.Now
	}
	dl, ok := ctx.Deadline()
	if !ok {
		return pageBound{now: now}
	}
	start := now()
	return pageBound{until: start.Add(time.Duration(float64(dl.Sub(start)) * share)), now: now}
}

// connPage is one page of a GraphQL connection.
type connPage[T any] struct {
	TotalCount int `json:"totalCount"`
	PageInfo   struct {
		HasNextPage bool   `json:"hasNextPage"`
		EndCursor   string `json:"endCursor"`
	} `json:"pageInfo"`
	Nodes []T `json:"nodes"`
}

// fetchConnection reads a connection page by page, starting after cursor
// `after` (empty = the beginning), until the connection is exhausted or limit
// nodes were collected. It returns the nodes (at most limit) and the
// connection's totalCount as the last page reported it. At least one page is
// always read, so a limit of zero still yields the total.
func fetchConnection[T any](limit int, after string, fetch func(after string) (connPage[T], error)) ([]T, int, error) {
	return fetchConnectionBounded(limit, after, pageBound{}, fetch)
}

// fetchConnectionBounded is fetchConnection that also stops, with what it has,
// once bound is spent: no page after the first is requested then. The result is
// then shorter than the connection's total, which the caller reports as a
// truncation.
func fetchConnectionBounded[T any](limit int, after string, bound pageBound, fetch func(after string) (connPage[T], error)) ([]T, int, error) {
	var nodes []T
	total := 0
	for {
		page, err := fetch(after)
		if err != nil {
			return nil, 0, err
		}
		total = page.TotalCount
		room := limit - len(nodes)
		if room > len(page.Nodes) {
			room = len(page.Nodes)
		}
		if room > 0 {
			nodes = append(nodes, page.Nodes[:room]...)
		}
		if !page.PageInfo.HasNextPage || len(nodes) >= limit || bound.spent() {
			return nodes, total, nil
		}
		if page.PageInfo.EndCursor == "" || len(page.Nodes) == 0 {
			return nil, 0, errors.New("github: GraphQL connection reports another page but gave no way to reach it")
		}
		after = page.PageInfo.EndCursor
	}
}

func reportOf(total, returned int) api.ConnectionReport {
	return api.ConnectionReport{Truncated: total > returned, Total: total, Returned: returned}
}

// runGraphQL runs one GraphQL document through gh and decodes its data member
// into dest. vars are string variables (-f); the PR number is an Int (-F);
// after, when non-empty, is the paging cursor.
func (p *Provider) runGraphQL(ctx context.Context, query string, vars map[string]string, number int, after string, dest any) error {
	args := []string{"api", "graphql", "-F", "query=" + query}
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "-f", k+"="+vars[k])
	}
	if number > 0 {
		args = append(args, "-F", fmt.Sprintf("number=%d", number))
	}
	if after != "" {
		args = append(args, "-f", "after="+after)
	}
	raw, err := p.runRead(ctx, readOpts{}, args...)
	if err != nil {
		return err
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("github: parse graphql response: %w", err)
	}
	if len(env.Errors) > 0 {
		msgs := make([]string, 0, len(env.Errors))
		for _, e := range env.Errors {
			msgs = append(msgs, e.Message)
		}
		return fmt.Errorf("github: graphql: %s", strings.Join(msgs, "; "))
	}
	if len(env.Data) == 0 {
		return errors.New("github: graphql response carried no data")
	}
	if err := json.Unmarshal(env.Data, dest); err != nil {
		return fmt.Errorf("github: parse graphql data: %w", err)
	}
	// A document that selects rateLimit { cost } reports what it just spent;
	// add it to the call's event (graphql_cost, bead pg2-ir8bs). A document
	// that does not select it adds nothing.
	var rl struct {
		RateLimit *struct {
			Cost int `json:"cost"`
		} `json:"rateLimit"`
	}
	if err := json.Unmarshal(env.Data, &rl); err == nil && rl.RateLimit != nil {
		eventlog.AddGraphQLCost(ctx, rl.RateLimit.Cost)
	}
	return nil
}

// prQueryEnvelope is the data shape of every repository.pullRequest query.
type prQueryEnvelope[T any] struct {
	// RateLimit is the document's own rateLimit selection when it carries one
	// (nil when it does not select it). runGraphQL has already added its Cost to
	// the call's event; Remaining and ResetAt feed judgeRateReading.
	RateLimit  *rateLimitSelection `json:"rateLimit"`
	Repository struct {
		PullRequest *T `json:"pullRequest"`
	} `json:"repository"`
}

// rateLimitSelection is the `rateLimit { cost remaining resetAt }` selection of
// a document that folds the rate-limit read into its own request.
type rateLimitSelection struct {
	Cost      int    `json:"cost"`
	Remaining int    `json:"remaining"`
	ResetAt   string `json:"resetAt"`
}

// judgeRateReading offers one response's rate-limit reading to gate (bead
// pg2-msgvt). A nil gate judges nothing. A refusal is returned as-is, and the
// caller is a page-fetch closure, so the refused page is neither used nor
// followed by another request: a throttled call never yields a partial result.
// The request that carried the refusing reading has already been spent, the one
// thing a fold cannot avoid (see SearchPRsEnrichedGated). A response with no
// rateLimit selection is an error when gate is non-nil (fail closed), never a
// reading of 0.
func judgeRateReading(sel *rateLimitSelection, gate RateGate) error {
	if gate == nil {
		return nil
	}
	if sel == nil {
		return errors.New("github: graphql response carried no rateLimit reading")
	}
	return gate(RateLimit{Remaining: sel.Remaining, ResetAt: sel.ResetAt})
}

// errPRNotResolved reads like gh's own unresolved-node error so the backend's
// not-found classification applies to a null pullRequest too.
func errPRNotResolved(repo string, number int) error {
	return fmt.Errorf("github: Could not resolve to a PullRequest with the number of %d in %s", number, repo)
}

func splitRepo(repo string, number int) (string, string, error) {
	if err := validateRepo(repo); err != nil {
		return "", "", err
	}
	if number <= 0 {
		return "", "", fmt.Errorf("github: invalid PR number %d", number)
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return "", "", fmt.Errorf("github: repo %q is not in owner/name form", repo)
	}
	return owner, name, nil
}

type gqlLogin struct {
	Login string `json:"login"`
}

type gqlReview struct {
	ID          string   `json:"id"`
	State       string   `json:"state"`
	Body        string   `json:"body"`
	CreatedAt   string   `json:"createdAt"`
	SubmittedAt string   `json:"submittedAt"`
	Author      gqlLogin `json:"author"`
	Commit      struct {
		OID string `json:"oid"`
	} `json:"commit"`
}

// reviewsPageQuery reads one page of a PR's reviews. The viewer's own PENDING
// review is part of this connection for the viewer. A review whose commit was
// since deleted reports a null commit, which decodes as an empty OID.
const reviewsPageQuery = `
query($owner: String!, $name: String!, $number: Int!, $after: String) {
  rateLimit { cost }
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviews(first: 100, after: $after) {
        totalCount
        pageInfo { hasNextPage endCursor }
        nodes {
          id
          state
          body
          createdAt
          submittedAt
          author { login }
          commit { oid }
        }
      }
    }
  }
}
`

// reviewsConnection reads every review of repo#number up to maxReviews, in
// server order, with the connection total.
func (p *Provider) reviewsConnection(ctx context.Context, repo string, number int) ([]gqlReview, int, error) {
	owner, name, err := splitRepo(repo, number)
	if err != nil {
		return nil, 0, err
	}
	vars := map[string]string{"owner": owner, "name": name}
	return fetchConnectionBounded(maxReviews, "", p.boundAt(ctx, reviewsTimeShare), func(after string) (connPage[gqlReview], error) {
		var d prQueryEnvelope[struct {
			Reviews connPage[gqlReview] `json:"reviews"`
		}]
		if err := p.runGraphQL(ctx, reviewsPageQuery, vars, number, after, &d); err != nil {
			return connPage[gqlReview]{}, err
		}
		if d.Repository.PullRequest == nil {
			return connPage[gqlReview]{}, errPRNotResolved(repo, number)
		}
		return d.Repository.PullRequest.Reviews, nil
	})
}

func toAPIReview(r gqlReview) api.Review {
	return api.Review{
		ID:        r.ID,
		Author:    r.Author.Login,
		State:     r.State,
		Body:      r.Body,
		CommitOID: r.Commit.OID,
		// Empty for a pending review (it has no submission time yet).
		SubmittedAt: r.SubmittedAt,
	}
}

// ListReviewsReport returns every review of the PR (submitted ones and the
// viewer's own pending one) with its body and state, newest first, together
// with the report for the reviews connection. See the file comment for the
// paging, cap and ordering rules.
func (p *Provider) ListReviewsReport(ctx context.Context, repo string, number int) (*api.ReviewsResult, error) {
	nodes, total, err := p.reviewsConnection(ctx, repo, number)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		return reviewTime(nodes[i]).After(reviewTime(nodes[j]))
	})
	out := make([]api.Review, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, toAPIReview(n))
	}
	return &api.ReviewsResult{Reviews: out, Report: reportOf(total, len(out))}, nil
}

// reviewTime is when a review took effect: its submission time, or its
// creation time for a pending review that has no submission time yet.
func reviewTime(r gqlReview) time.Time {
	if t := parseTime(r.SubmittedAt); !t.IsZero() {
		return t
	}
	return parseTime(r.CreatedAt)
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

type gqlIssueComment struct {
	ID                string   `json:"id"`
	Author            gqlLogin `json:"author"`
	AuthorAssociation string   `json:"authorAssociation"`
	Body              string   `json:"body"`
	CreatedAt         string   `json:"createdAt"`
	UpdatedAt         string   `json:"updatedAt"`
}

type gqlThreadComment struct {
	ID                string   `json:"id"`
	Author            gqlLogin `json:"author"`
	AuthorAssociation string   `json:"authorAssociation"`
	Body              string   `json:"body"`
	Path              string   `json:"path"`
	Line              int      `json:"line"`
	OriginalLine      int      `json:"originalLine"`
	CreatedAt         string   `json:"createdAt"`
	UpdatedAt         string   `json:"updatedAt"`
	IsMinimized       bool     `json:"isMinimized"`
	MinimizedReason   string   `json:"minimizedReason"`
	OriginalCommit    struct {
		OID string `json:"oid"`
	} `json:"originalCommit"`
	PullRequestReview struct {
		ID string `json:"id"`
	} `json:"pullRequestReview"`
}

type gqlThread struct {
	ID         string                     `json:"id"`
	IsResolved bool                       `json:"isResolved"`
	IsOutdated bool                       `json:"isOutdated"`
	Comments   connPage[gqlThreadComment] `json:"comments"`
}

// threadCommentFields is the selection shared by the first page of a thread's
// comments (inside reviewThreadsPageQuery) and its follow-up pages.
const threadCommentFields = `
          id
          author { login }
          authorAssociation
          body
          path
          line
          originalLine
          createdAt
          updatedAt
          isMinimized
          minimizedReason
          originalCommit { oid }
          pullRequestReview { id }
`

// reviewThreadsPageQuery reads one page of a PR's review threads with the
// first page of each thread's comments. reviewThreads.id is the PRRT_ node id
// that addPullRequestReviewThreadReply takes.
const reviewThreadsPageQuery = `
query($owner: String!, $name: String!, $number: Int!, $after: String) {
  rateLimit { cost }
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviewThreads(first: 100, after: $after) {
        totalCount
        pageInfo { hasNextPage endCursor }
        nodes {
          id
          isResolved
          isOutdated
          comments(first: 100) {
            totalCount
            pageInfo { hasNextPage endCursor }
            nodes {` + threadCommentFields + `            }
          }
        }
      }
    }
  }
}
`

// threadCommentsPageQuery reads the following pages of ONE thread's comments,
// for a thread with more than one page of them.
const threadCommentsPageQuery = `
query($threadId: ID!, $after: String) {
  rateLimit { cost }
  node(id: $threadId) {
    ... on PullRequestReviewThread {
      comments(first: 100, after: $after) {
        totalCount
        pageInfo { hasNextPage endCursor }
        nodes {` + threadCommentFields + `        }
      }
    }
  }
}
`

// issueCommentsPageQuery reads one page of a PR's issue (conversation)
// comments, bot comments included.
const issueCommentsPageQuery = `
query($owner: String!, $name: String!, $number: Int!, $after: String) {
  rateLimit { cost }
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      comments(first: 100, after: $after) {
        totalCount
        pageInfo { hasNextPage endCursor }
        nodes {
          id
          author { login }
          authorAssociation
          body
          createdAt
          updatedAt
        }
      }
    }
  }
}
`

func (p *Provider) threadsConnection(ctx context.Context, repo string, number int, bound pageBound) ([]gqlThread, int, error) {
	owner, name, err := splitRepo(repo, number)
	if err != nil {
		return nil, 0, err
	}
	vars := map[string]string{"owner": owner, "name": name}
	return fetchConnectionBounded(maxThreads, "", bound, func(after string) (connPage[gqlThread], error) {
		var d prQueryEnvelope[struct {
			ReviewThreads connPage[gqlThread] `json:"reviewThreads"`
		}]
		if err := p.runGraphQL(ctx, reviewThreadsPageQuery, vars, number, after, &d); err != nil {
			return connPage[gqlThread]{}, err
		}
		if d.Repository.PullRequest == nil {
			return connPage[gqlThread]{}, errPRNotResolved(repo, number)
		}
		return d.Repository.PullRequest.ReviewThreads, nil
	})
}

// moreThreadComments reads the pages of a thread's comments after the first,
// up to limit comments.
func (p *Provider) moreThreadComments(ctx context.Context, t gqlThread, limit int, bound pageBound) ([]gqlThreadComment, error) {
	nodes, _, err := fetchConnectionBounded(limit, t.Comments.PageInfo.EndCursor, bound, func(after string) (connPage[gqlThreadComment], error) {
		var d struct {
			Node *struct {
				Comments connPage[gqlThreadComment] `json:"comments"`
			} `json:"node"`
		}
		if err := p.runGraphQL(ctx, threadCommentsPageQuery, map[string]string{"threadId": t.ID}, 0, after, &d); err != nil {
			return connPage[gqlThreadComment]{}, err
		}
		if d.Node == nil {
			return connPage[gqlThreadComment]{}, fmt.Errorf("github: review thread %s did not resolve", t.ID)
		}
		return d.Node.Comments, nil
	})
	return nodes, err
}

func (p *Provider) issueCommentsConnection(ctx context.Context, repo string, number, limit int, bound pageBound) ([]gqlIssueComment, int, error) {
	owner, name, err := splitRepo(repo, number)
	if err != nil {
		return nil, 0, err
	}
	vars := map[string]string{"owner": owner, "name": name}
	return fetchConnectionBounded(limit, "", bound, func(after string) (connPage[gqlIssueComment], error) {
		var d prQueryEnvelope[struct {
			Comments connPage[gqlIssueComment] `json:"comments"`
		}]
		if err := p.runGraphQL(ctx, issueCommentsPageQuery, vars, number, after, &d); err != nil {
			return connPage[gqlIssueComment]{}, err
		}
		if d.Repository.PullRequest == nil {
			return connPage[gqlIssueComment]{}, errPRNotResolved(repo, number)
		}
		return d.Repository.PullRequest.Comments, nil
	})
}

// ListCommentsReport returns the PR's issue comments and the comments of every
// review thread (the viewer's own pending comments included), newest first.
// Thread comments carry their thread's PRRT_ node id as ReviewThreadID and
// the thread's resolved and outdated flags; ThreadID keeps its own meaning
// (the comment's own node id) and is empty for an issue comment. Threads are
// read first, then issue comments with whatever remains of the maxComments
// budget. See the file comment for the paging, cap and ordering rules.
func (p *Provider) ListCommentsReport(ctx context.Context, repo string, number int) (*api.CommentsResult, error) {
	if _, _, err := splitRepo(repo, number); err != nil {
		return nil, err
	}
	// Both bounds are measured from the same start, so the issue comments'
	// share is what is left after the threads' (see the file comment).
	threadsBound := p.boundAt(ctx, threadsTimeShare)
	issueBound := p.boundAt(ctx, issueCommentsTimeShare)
	threads, threadTotal, err := p.threadsConnection(ctx, repo, number, threadsBound)
	if err != nil {
		return nil, fmt.Errorf("github: list review threads: %w", err)
	}

	out := make([]api.Comment, 0)
	commentTotal := 0
	for _, t := range threads {
		commentTotal += t.Comments.TotalCount
		nodes := t.Comments.Nodes
		if room := maxComments - len(out) - len(nodes); t.Comments.PageInfo.HasNextPage && room > 0 && !threadsBound.spent() {
			more, merr := p.moreThreadComments(ctx, t, room, threadsBound)
			if merr != nil {
				return nil, fmt.Errorf("github: list review thread comments: %w", merr)
			}
			nodes = append(append([]gqlThreadComment(nil), nodes...), more...)
		}
		for _, c := range nodes {
			if len(out) >= maxComments {
				break
			}
			out = append(out, toAPIThreadComment(t, c))
		}
	}

	issue, issueTotal, err := p.issueCommentsConnection(ctx, repo, number, maxComments-len(out), issueBound)
	if err != nil {
		return nil, fmt.Errorf("github: list issue comments: %w", err)
	}
	commentTotal += issueTotal
	for _, c := range issue {
		out = append(out, api.Comment{
			ID:         c.ID,
			Author:     c.Author.Login,
			AuthorRole: strings.ToLower(c.AuthorAssociation),
			Body:       c.Body,
			CreatedAt:  c.CreatedAt,
			UpdatedAt:  c.UpdatedAt,
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		return parseTime(out[i].CreatedAt).After(parseTime(out[j].CreatedAt))
	})
	return &api.CommentsResult{
		Comments:       out,
		Threads:        reportOf(threadTotal, len(threads)),
		CommentsReport: reportOf(commentTotal, len(out)),
	}, nil
}

func toAPIThreadComment(t gqlThread, c gqlThreadComment) api.Comment {
	line := c.Line
	if line == 0 {
		line = c.OriginalLine
	}
	return api.Comment{
		ID:         c.ID,
		Author:     c.Author.Login,
		AuthorRole: strings.ToLower(c.AuthorAssociation),
		Body:       c.Body,
		Path:       c.Path,
		Line:       line,
		// ThreadID is this comment's own node id, as it always was; the real
		// thread id is ReviewThreadID.
		ThreadID:          c.ID,
		ReviewThreadID:    t.ID,
		Resolved:          t.IsResolved,
		CreatedAt:         c.CreatedAt,
		UpdatedAt:         c.UpdatedAt,
		ThreadIsOutdated:  t.IsOutdated,
		IsMinimized:       c.IsMinimized,
		MinimizedReason:   c.MinimizedReason,
		OriginalCommitOID: c.OriginalCommit.OID,
		ReviewID:          c.PullRequestReview.ID,
	}
}
