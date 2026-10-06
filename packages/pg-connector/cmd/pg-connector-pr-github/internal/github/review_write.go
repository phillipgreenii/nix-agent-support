package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// This file holds the WRITE primitives the review_submit create-or-append flow
// is built from. Each is a single, plain gh write: none deletes, replaces or
// submits a review, and none goes through runRead, so the read retry layer
// never replays a write that may already have reached GitHub. Finding the
// existing pending review (GetPendingReview), the re-read before each write,
// the per-PR lock and the body-section merge belong to the callers.
//
// It lives apart from github.go on purpose: github.go is hash-pinned against
// pg-pr's copy (testdata/pg-pr-drift), and none of this exists in pg-pr.
//
// The three primitives:
//
//   - CreateBodyOnlyPendingReview: a REST create of a PENDING review at a
//     commit carrying only a body. A REST create that carried comments would
//     be atomic (one bad anchor most likely fails the whole call and leaves
//     nothing) and cannot express a reply at all, so every comment is added
//     afterwards through GraphQL, one failure-isolated path for new points and
//     replies alike. A body-only pending review is a valid
//     pullRequestReviewId for those writes, including a reply to a thread of
//     a SUBMITTED review.
//   - WriteReviewItems: GraphQL writes in documents of at most
//     maxWriteAliasesPerDocument mutations, every alias checked.
//   - UpdateReviewBody: a whole-body updatePullRequestReview write
//     (last-writer-wins; the caller builds the new body from a fresh read).

// ErrPendingReviewExists is returned by CreateBodyOnlyPendingReview when GitHub
// refuses the create because the acting identity already has a pending review
// on the PR ("one pending review", HTTP 422). A hand-started review appeared
// between the caller's read and its create; the caller loops back to the read
// and appends instead.
var ErrPendingReviewExists = errors.New("github: a pending review already exists for this identity")

// ErrTwoPendingReviews is returned by UpdateReviewBody when GitHub refuses the
// write with "User can only have one pending review per pull request": the
// identity holds more than one pending review on the PR, so the whole-body
// write is not accepted.
var ErrTwoPendingReviews = errors.New("github: identity has more than one pending review on the pull request")

// maxWriteAliasesPerDocument bounds the mutations in one GraphQL document. A
// document of 110 aliases landed 18 and then failed with "Resource limits for
// this query exceeded"; documents of 10 landed every item.
const maxWriteAliasesPerDocument = 10

// WriteFailReason says why one write item did not land.
type WriteFailReason string

const (
	// ReasonAnchorRejected: the new point's anchor (path, line, side) was
	// refused; the thread came back null. Permanent for that request.
	ReasonAnchorRejected WriteFailReason = "anchor_rejected"
	// ReasonThreadNotFound: the reply's thread does not exist on this PR or
	// the reply came back with no comment. Permanent for that request.
	ReasonThreadNotFound WriteFailReason = "thread_not_found"
	// ReasonRateLimited: a rate limit answer (primary or secondary), or the
	// item was never sent because an earlier document hit one. Not retried
	// inside the call.
	ReasonRateLimited WriteFailReason = "rate_limited"
	// ReasonUnconfirmed: the outcome is unknown (a document-level error, a
	// transport failure or an alias missing from the answer). The item may or
	// may not have landed; a re-read decides.
	ReasonUnconfirmed WriteFailReason = "unconfirmed"
)

// CreatedReview identifies a pending review just created.
type CreatedReview struct {
	// NodeID is the GraphQL id (the pullRequestReviewId of later writes).
	NodeID string
	// ID is the REST database id.
	ID int64
	// State is the lower-cased review state ("pending" when GitHub omits it).
	State string
}

// ReviewWriteItem is one comment to add to a pending review: a new point
// (ReplyToThreadID empty) or a reply (ReplyToThreadID is the review-thread
// node id; Path, Line and Side are then ignored).
type ReviewWriteItem struct {
	Path string
	Line int
	// Side is "LEFT" or "RIGHT"; "" means RIGHT.
	Side            string
	Body            string
	ReplyToThreadID string
}

// IsReply reports whether the item replies to an existing thread.
func (i ReviewWriteItem) IsReply() bool { return i.ReplyToThreadID != "" }

// ReviewWriteResult is the outcome of one ReviewWriteItem, in input order.
type ReviewWriteResult struct {
	Landed bool
	// Reason is set when Landed is false.
	Reason WriteFailReason
	// ThreadID and CommentID are the created thread and first comment ids
	// (the comment id only, for a reply) when GitHub returned them.
	ThreadID  string
	CommentID string
}

// normalizeWriteSide maps "" to RIGHT and upper-cases the rest.
func normalizeWriteSide(side string) string {
	side = strings.ToUpper(strings.TrimSpace(side))
	if side == "" {
		return "RIGHT"
	}
	return side
}

// ghFailureText is the lower-cased text to classify a failed write by: the
// error message, gh's stderr when separate, and whatever gh printed to stdout
// (gh puts the response body there for an API error).
func ghFailureText(err error, raw []byte) string {
	var b strings.Builder
	if err != nil {
		b.WriteString(err.Error())
		b.WriteByte(' ')
		b.WriteString(failureText(err))
		b.WriteByte(' ')
	}
	b.Write(raw)
	return strings.ToLower(b.String())
}

// isRateLimitText reports a primary or secondary rate limit answer.
func isRateLimitText(lower string) bool {
	return strings.Contains(lower, "rate limit") || strings.Contains(lower, "abuse detection")
}

// CreateBodyOnlyPendingReview creates a PENDING review (no event, so it stays
// unsubmitted) anchored to commitID that carries only body. It sends no
// comments. When the acting identity already has a pending review GitHub
// answers HTTP 422 "one pending review" and the error wraps
// ErrPendingReviewExists; any other failure is returned wrapped with gh's
// stderr.
func (p *Provider) CreateBodyOnlyPendingReview(ctx context.Context, repo string, number int, commitID, body string) (*CreatedReview, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	if number <= 0 {
		return nil, fmt.Errorf("github: invalid PR number %d", number)
	}
	if strings.TrimSpace(commitID) == "" {
		return nil, errors.New("github: commit id is required")
	}
	payload, err := json.Marshal(map[string]any{"commit_id": commitID, "body": body})
	if err != nil {
		return nil, fmt.Errorf("github: marshal review payload: %w", err)
	}
	raw, err := p.gh.RunStdin(
		ctx, payload,
		"api",
		fmt.Sprintf("repos/%s/pulls/%d/reviews", repo, number),
		"--method", "POST",
		"--input", "-",
	)
	if err != nil {
		lower := ghFailureText(err, raw)
		if strings.Contains(lower, "one pending review") {
			return nil, fmt.Errorf("github: create pending review: %w: %w", ErrPendingReviewExists, err)
		}
		return nil, fmt.Errorf("github: create pending review: %w", err)
	}
	var resp struct {
		ID     int64  `json:"id"`
		NodeID string `json:"node_id"`
		State  string `json:"state"`
	}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, fmt.Errorf("github: parse create pending review response: %w", err)
		}
	}
	state := strings.ToLower(resp.State)
	if state == "" {
		state = "pending"
	}
	return &CreatedReview{NodeID: resp.NodeID, ID: resp.ID, State: state}, nil
}

// gqlWriteError is one entry of a GraphQL response's errors array.
type gqlWriteError struct {
	Message string `json:"message"`
	Path    []any  `json:"path"`
}

// aliasName is the response key of item index i.
func aliasName(i int) string { return fmt.Sprintf("w%d", i) }

// buildWriteDocument renders one GraphQL document (and its variables) adding
// items. The items' bodies and anchors travel as variables, never spliced into
// the document text.
func buildWriteDocument(reviewID string, items []ReviewWriteItem) (string, map[string]any) {
	vars := map[string]any{"review": reviewID}
	var decls, sels []string
	decls = append(decls, "$review: ID!")
	for i, it := range items {
		a := aliasName(i)
		bvar := fmt.Sprintf("b%d", i)
		decls = append(decls, fmt.Sprintf("$%s: String!", bvar))
		vars[bvar] = it.Body
		if it.IsReply() {
			tvar := fmt.Sprintf("t%d", i)
			decls = append(decls, fmt.Sprintf("$%s: ID!", tvar))
			vars[tvar] = it.ReplyToThreadID
			sels = append(sels, fmt.Sprintf(
				"  %s: addPullRequestReviewThreadReply(input: {pullRequestReviewId: $review, pullRequestReviewThreadId: $%s, body: $%s}) { comment { id } }",
				a, tvar, bvar,
			))
			continue
		}
		pvar, lvar, svar := fmt.Sprintf("p%d", i), fmt.Sprintf("l%d", i), fmt.Sprintf("s%d", i)
		decls = append(decls,
			fmt.Sprintf("$%s: String!", pvar),
			fmt.Sprintf("$%s: Int!", lvar),
			fmt.Sprintf("$%s: DiffSide!", svar))
		vars[pvar] = it.Path
		vars[lvar] = it.Line
		vars[svar] = normalizeWriteSide(it.Side)
		sels = append(sels, fmt.Sprintf(
			"  %s: addPullRequestReviewThread(input: {pullRequestReviewId: $review, path: $%s, line: $%s, side: $%s, body: $%s}) { thread { id comments(first: 1) { nodes { id } } } }",
			a, pvar, lvar, svar, bvar,
		))
	}
	doc := "mutation(" + strings.Join(decls, ", ") + ") {\n" + strings.Join(sels, "\n") + "\n}\n"
	return doc, vars
}

// runGraphQLWrite sends one GraphQL document through the write seam (stdin
// JSON, never retried) and returns gh's stdout and error.
func (p *Provider) runGraphQLWrite(ctx context.Context, doc string, vars map[string]any) ([]byte, error) {
	payload, err := json.Marshal(map[string]any{"query": doc, "variables": vars})
	if err != nil {
		return nil, fmt.Errorf("github: marshal graphql payload: %w", err)
	}
	return p.gh.RunStdin(ctx, payload, "api", "graphql", "--input", "-")
}

// WriteReviewItems adds items to the pending review reviewID (its GraphQL
// node id) and reports one result per item, in input order. Items go out in
// GraphQL documents of at most 10 mutations, sent one after another, and every
// alias of every answer is checked: a null thread or a missing comment is a
// failed item, never a success, even when the document carries no error and
// its neighbours landed.
//
// The call never retries. A rate limit answer marks its document's items (and
// every item not yet sent) rate_limited. A document-level failure (transport
// error, resource limit, malformed answer) marks the items it did not
// demonstrably land as unconfirmed; the next document is still tried. The
// returned error is non-nil only for invalid arguments, in which case nothing
// was sent.
func (p *Provider) WriteReviewItems(ctx context.Context, reviewID string, items []ReviewWriteItem) ([]ReviewWriteResult, error) {
	if strings.TrimSpace(reviewID) == "" {
		return nil, errors.New("github: review id is required")
	}
	for i, it := range items {
		if it.IsReply() {
			continue
		}
		if strings.TrimSpace(it.Path) == "" || it.Line <= 0 {
			return nil, fmt.Errorf("github: item %d: a new point needs a path and a positive line", i)
		}
	}
	results := make([]ReviewWriteResult, len(items))
	rateLimited := false
	for start := 0; start < len(items); start += maxWriteAliasesPerDocument {
		end := min(start+maxWriteAliasesPerDocument, len(items))
		chunk := items[start:end]
		if rateLimited {
			for i := range chunk {
				results[start+i] = ReviewWriteResult{Reason: ReasonRateLimited}
			}
			continue
		}
		if ctx.Err() != nil {
			for i := range chunk {
				results[start+i] = ReviewWriteResult{Reason: ReasonUnconfirmed}
			}
			continue
		}
		doc, vars := buildWriteDocument(reviewID, chunk)
		raw, err := p.runGraphQLWrite(ctx, doc, vars)
		out, limited := classifyWriteDocument(chunk, raw, err)
		copy(results[start:end], out)
		rateLimited = rateLimited || limited
	}
	return results, nil
}

// classifyWriteDocument turns one document's answer into per-item results and
// reports whether a rate limit was seen. runErr is the gh error, if any; gh
// still hands back stdout with an error, and a partial answer is honoured so
// aliases that demonstrably landed are not reported unconfirmed.
func classifyWriteDocument(chunk []ReviewWriteItem, raw []byte, runErr error) ([]ReviewWriteResult, bool) {
	results := make([]ReviewWriteResult, len(chunk))
	text := ghFailureText(runErr, nil)

	var resp struct {
		Data   map[string]json.RawMessage `json:"data"`
		Errors []gqlWriteError            `json:"errors"`
	}
	parsed := len(bytes.TrimSpace(raw)) > 0 && json.Unmarshal(raw, &resp) == nil
	if !parsed {
		resp.Data, resp.Errors = nil, nil
	}
	for _, e := range resp.Errors {
		text += " " + strings.ToLower(e.Message)
	}
	if runErr != nil && !parsed {
		text += " " + strings.ToLower(string(raw))
	}
	limited := isRateLimitText(text)

	// Per-alias error messages (errors whose path names an alias).
	aliasErr := map[string]string{}
	for _, e := range resp.Errors {
		if len(e.Path) == 0 {
			continue
		}
		if name, ok := e.Path[0].(string); ok {
			aliasErr[name] += " " + strings.ToLower(e.Message)
		}
	}

	for i, it := range chunk {
		a := aliasName(i)
		cell, present := resp.Data[a]
		cellNull := present && (len(bytes.TrimSpace(cell)) == 0 || string(bytes.TrimSpace(cell)) == "null")
		msg, hasErr := aliasErr[a]

		if !present {
			// Never answered: the document failed before this alias ran.
			if limited || hasErr && isRateLimitText(msg) {
				results[i] = ReviewWriteResult{Reason: ReasonRateLimited}
			} else {
				results[i] = ReviewWriteResult{Reason: ReasonUnconfirmed}
			}
			continue
		}
		if it.IsReply() {
			var v struct {
				Comment *struct {
					ID string `json:"id"`
				} `json:"comment"`
			}
			if !cellNull {
				_ = json.Unmarshal(cell, &v)
			}
			if v.Comment != nil && v.Comment.ID != "" {
				results[i] = ReviewWriteResult{Landed: true, CommentID: v.Comment.ID}
				continue
			}
			results[i] = ReviewWriteResult{Reason: failedItemReason(it, msg, hasErr)}
			continue
		}
		var v struct {
			Thread *struct {
				ID       string `json:"id"`
				Comments struct {
					Nodes []struct {
						ID string `json:"id"`
					} `json:"nodes"`
				} `json:"comments"`
			} `json:"thread"`
		}
		if !cellNull {
			_ = json.Unmarshal(cell, &v)
		}
		if v.Thread != nil && v.Thread.ID != "" {
			r := ReviewWriteResult{Landed: true, ThreadID: v.Thread.ID}
			if len(v.Thread.Comments.Nodes) > 0 {
				r.CommentID = v.Thread.Comments.Nodes[0].ID
			}
			results[i] = r
			continue
		}
		results[i] = ReviewWriteResult{Reason: failedItemReason(it, msg, hasErr)}
	}
	return results, limited
}

// failedItemReason classifies an alias that answered but did not land.
func failedItemReason(it ReviewWriteItem, aliasMsg string, hasErr bool) WriteFailReason {
	if hasErr && isRateLimitText(aliasMsg) {
		return ReasonRateLimited
	}
	if it.IsReply() {
		return ReasonThreadNotFound
	}
	return ReasonAnchorRejected
}

const updatePullRequestReviewMutation = `
mutation($review: ID!, $body: String!) {
  updatePullRequestReview(input: {pullRequestReviewId: $review, body: $body}) {
    pullRequestReview { id }
  }
}
`

// UpdateReviewBody replaces the WHOLE body of the pending review reviewID (its
// GraphQL node id) with body. It is last-writer-wins: the caller builds body
// from a fresh read. With more than one pending review GitHub refuses ("User
// can only have one pending review per pull request") and the error wraps
// ErrTwoPendingReviews; any other failure is returned wrapped with gh's
// stderr. The write is not retried.
func (p *Provider) UpdateReviewBody(ctx context.Context, reviewID, body string) error {
	if strings.TrimSpace(reviewID) == "" {
		return errors.New("github: review id is required")
	}
	raw, err := p.runGraphQLWrite(ctx, updatePullRequestReviewMutation, map[string]any{"review": reviewID, "body": body})
	var resp struct {
		Data struct {
			Update *struct {
				Review *struct {
					ID string `json:"id"`
				} `json:"pullRequestReview"`
			} `json:"updatePullRequestReview"`
		} `json:"data"`
		Errors []gqlWriteError `json:"errors"`
	}
	if len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &resp)
	}
	if err == nil && len(resp.Errors) == 0 && resp.Data.Update != nil && resp.Data.Update.Review != nil {
		return nil
	}
	lower := ghFailureText(err, raw)
	for _, e := range resp.Errors {
		lower += " " + strings.ToLower(e.Message)
	}
	cause := err
	if cause == nil {
		msgs := make([]string, 0, len(resp.Errors))
		for _, e := range resp.Errors {
			msgs = append(msgs, e.Message)
		}
		if len(msgs) == 0 {
			msgs = append(msgs, "update returned no review")
		}
		cause = errors.New(strings.Join(msgs, "; "))
	}
	if strings.Contains(lower, "only have one pending review") {
		return fmt.Errorf("github: update review body: %w: %w", ErrTwoPendingReviews, cause)
	}
	return fmt.Errorf("github: update review body: %w", cause)
}
