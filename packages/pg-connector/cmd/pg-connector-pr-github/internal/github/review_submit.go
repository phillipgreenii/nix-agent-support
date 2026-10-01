package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/api"
)

// This file holds the review_submit write path (entity-change-flow contract
// 9.1): post a PENDING review anchored to a head SHA, stamp the bot marker,
// and find/delete the actor's existing pending review. It lives apart from
// github.go on purpose: github.go is hash-pinned against pg-pr's copy
// (testdata/pg-pr-drift), and none of this exists in pg-pr.

// BotMarker is the HTML comment this backend stamps on every review body and
// every inline comment it posts, so a later reader can tell backend-authored
// review text from human-authored text. It is deliberately pg-connector
// specific (pg-pr's marker package is Go-internal and not importable here).
const BotMarker = "<!-- pg-connector-pr-github:review -->"

// botAttribution is the visible half of the stamp.
const botAttribution = "_Posted by pg-connector._"

// StampBotMarker appends the bot marker and visible attribution to body. It
// is idempotent: a body already carrying BotMarker is returned unchanged.
func StampBotMarker(body string) string {
	if strings.Contains(body, BotMarker) {
		return body
	}
	if body == "" {
		return botAttribution + "\n" + BotMarker
	}
	return body + "\n\n" + botAttribution + "\n" + BotMarker
}

// ReviewSubmitComment is one anchored inline comment of a submitted review.
// Side is "LEFT" or "RIGHT"; "" means RIGHT.
type ReviewSubmitComment struct {
	Path string
	Line int
	Side string
	Body string
}

// pendingReviewPageSize is the REST page size used when listing reviews.
const pendingReviewPageSize = 100

// FindPendingReview returns the numeric REST id of the authenticated user's
// existing PENDING review on repo#number. GitHub only ever returns a PENDING
// review to its own author, so a PENDING entry in the listing is the actor's.
// found is false when there is none.
func (p *Provider) FindPendingReview(ctx context.Context, repo string, number int) (id int64, found bool, err error) {
	if err := validateRepo(repo); err != nil {
		return 0, false, err
	}
	if number <= 0 {
		return 0, false, fmt.Errorf("github: invalid PR number %d", number)
	}
	for page := 1; ; page++ {
		raw, err := p.gh.Run(ctx, "api",
			fmt.Sprintf("repos/%s/pulls/%d/reviews?per_page=%d&page=%d", repo, number, pendingReviewPageSize, page))
		if err != nil {
			return 0, false, fmt.Errorf("github: list reviews: %w", err)
		}
		var entries []struct {
			ID    int64  `json:"id"`
			State string `json:"state"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(raw), &entries); err != nil {
			return 0, false, fmt.Errorf("github: parse reviews listing: %w", err)
		}
		for _, e := range entries {
			if strings.EqualFold(e.State, "PENDING") {
				return e.ID, true, nil
			}
		}
		if len(entries) < pendingReviewPageSize {
			return 0, false, nil
		}
	}
}

// DeleteReview deletes the (pending) review with REST id reviewID.
func (p *Provider) DeleteReview(ctx context.Context, repo string, number int, reviewID int64) error {
	if err := validateRepo(repo); err != nil {
		return err
	}
	if number <= 0 {
		return fmt.Errorf("github: invalid PR number %d", number)
	}
	if _, err := p.gh.Run(ctx, "api",
		fmt.Sprintf("repos/%s/pulls/%d/reviews/%d", repo, number, reviewID),
		"--method", "DELETE"); err != nil {
		return fmt.Errorf("github: delete review %d: %w", reviewID, err)
	}
	return nil
}

// PostPendingReview posts a PENDING review (no event, so it stays unsubmitted)
// anchored to commitID, stamping BotMarker on the body and on each comment.
// Every comment must already be anchorable (path + positive line); the caller
// validates. Unlike PostReview it carries each comment's side through. A host
// failure is returned wrapped with gh's stderr, so callers can match "HTTP 422".
func (p *Provider) PostPendingReview(ctx context.Context, repo string, number int, commitID, body string, comments []ReviewSubmitComment) (*api.Review, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	if number <= 0 {
		return nil, fmt.Errorf("github: invalid PR number %d", number)
	}
	rcs := make([]reviewComment, 0, len(comments))
	for _, c := range comments {
		side := c.Side
		if side == "" {
			side = "RIGHT"
		}
		rcs = append(rcs, reviewComment{Path: c.Path, Body: StampBotMarker(c.Body), Line: c.Line, Side: side})
	}
	payload := map[string]any{
		"commit_id": commitID,
		"body":      StampBotMarker(body),
	}
	if len(rcs) > 0 {
		payload["comments"] = rcs
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("github: marshal review payload: %w", err)
	}
	raw, err := p.gh.RunStdin(
		ctx, payloadJSON,
		"api",
		fmt.Sprintf("repos/%s/pulls/%d/reviews", repo, number),
		"--method", "POST",
		"--input", "-",
	)
	if err != nil {
		return nil, fmt.Errorf("github: post review: %w", err)
	}
	var resp struct {
		NodeID string `json:"node_id"`
		State  string `json:"state"`
		Body   string `json:"body"`
	}
	if len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &resp)
	}
	state := strings.ToLower(resp.State)
	if state == "" {
		state = "pending"
	}
	return &api.Review{ID: resp.NodeID, State: state, Body: resp.Body}, nil
}
