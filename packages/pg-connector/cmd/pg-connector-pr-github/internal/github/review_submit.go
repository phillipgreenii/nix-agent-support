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
// 9.1): post a PENDING review anchored to a head SHA, stamp the bot marker and
// content digest, and delete a pending review. Finding the actor's pending
// review is review_pending's job (GetPendingReview). It lives apart from
// github.go on purpose: github.go is hash-pinned against pg-pr's copy
// (testdata/pg-pr-drift), and none of this exists in pg-pr.

// BotMarker is the HTML comment this backend stamps on every inline comment it
// posts (and on a body that already carries it), so a later reader can tell
// backend-authored review text from human-authored text. The review body
// carries the digest marker instead (DigestMarkerPrefix), which is also an
// authorship marker. It is deliberately pg-connector
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
// anchored to commitID, stamping BotMarker on each comment and, on the body, a
// digest marker (StampBodyWithDigest) covering the body and every comment so a
// later reader can prove the review is unedited.
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
	texts := make([]string, 0, len(comments))
	for _, c := range comments {
		side := c.Side
		if side == "" {
			side = "RIGHT"
		}
		text := StampBotMarker(c.Body)
		texts = append(texts, text)
		rcs = append(rcs, reviewComment{Path: c.Path, Body: text, Line: c.Line, Side: side})
	}
	payload := map[string]any{
		"commit_id": commitID,
		"body":      StampBodyWithDigest(body, texts),
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
