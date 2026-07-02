package beads

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// reviewFailLabelPrefix is the per-bead consecutive-failure counter, persisted
// as a label (`review-fail-count-<n>`) so it survives a daemon restart
// (design §2.3.4). At most one such label is present at a time.
const reviewFailLabelPrefix = "review-fail-count-"

// needsHumanLabel drops a dead-lettered bead out of `bd ready` and flags it for
// manual triage.
const needsHumanLabel = "needs-human"

// ClaimDraftReview atomically claims a draft-review bead (assignee=you,
// status=in_progress; idempotent if already claimed). Recorded for
// observability — the daemon flock is the real mutex (design R3).
func (c *Client) ClaimDraftReview(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("draft-review: id required")
	}
	if _, err := c.Runner.Run(ctx, "update", id, "--claim"); err != nil {
		return fmt.Errorf("claim draft-review %s: %w", id, err)
	}
	return nil
}

// UnclaimDraftReview returns a claimed-but-unproduced bead to `bd ready` by
// resetting its status to open (bd has no explicit --unclaim; --claim set
// status=in_progress, so open restores readiness). Called on graceful
// production failure so a later tick retries.
func (c *Client) UnclaimDraftReview(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("draft-review: id required")
	}
	if _, err := c.Runner.Run(ctx, "update", id, "--status", "open"); err != nil {
		return fmt.Errorf("unclaim draft-review %s: %w", id, err)
	}
	return nil
}

// CloseDraftReview closes a produced draft-review bead. Idempotent: closing an
// already-closed bead is treated as success.
func (c *Client) CloseDraftReview(ctx context.Context, id, reason string) error {
	if id == "" {
		return fmt.Errorf("draft-review: id required")
	}
	args := []string{"close", id}
	if reason != "" {
		args = append(args, "--reason", reason)
	}
	if _, err := c.Runner.Run(ctx, args...); err != nil {
		if strings.Contains(err.Error(), "already closed") {
			return nil
		}
		return fmt.Errorf("close draft-review %s: %w", id, err)
	}
	return nil
}

// ReopenDraftReview re-opens a previously-closed draft-review bead so it
// becomes claimable again (re-review-on-head-advance, design §2.3.3).
func (c *Client) ReopenDraftReview(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("draft-review: id required")
	}
	if _, err := c.Runner.Run(ctx, "update", id, "--status", "open"); err != nil {
		return fmt.Errorf("reopen draft-review %s: %w", id, err)
	}
	return nil
}

// DeadLetterDraftReview parks a poison bead: status=blocked (drops it out of
// `bd ready`) plus a needs-human label for manual triage (design §2.3.4).
func (c *Client) DeadLetterDraftReview(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("draft-review: id required")
	}
	if _, err := c.Runner.Run(ctx, "update", id,
		"--status", "blocked", "--add-label", needsHumanLabel); err != nil {
		return fmt.Errorf("dead-letter draft-review %s: %w", id, err)
	}
	return nil
}

// ReviewFailCount reads the current consecutive-failure count from the bead's
// labels (0 when no review-fail-count-<n> label is present).
func (c *Client) ReviewFailCount(ctx context.Context, id string) (int, error) {
	labels, err := c.beadLabels(ctx, id)
	if err != nil {
		return 0, err
	}
	return failCountFromLabels(labels), nil
}

// BumpReviewFailCount increments the fail count by one, replacing the old
// review-fail-count-<n> label. Returns the new count.
func (c *Client) BumpReviewFailCount(ctx context.Context, id string) (int, error) {
	labels, err := c.beadLabels(ctx, id)
	if err != nil {
		return 0, err
	}
	current := failCountFromLabels(labels)
	next := current + 1
	args := []string{"update", id, "--add-label", failLabel(next)}
	if current > 0 {
		args = append(args, "--remove-label", failLabel(current))
	}
	if _, err := c.Runner.Run(ctx, args...); err != nil {
		return 0, fmt.Errorf("bump review fail count %s: %w", id, err)
	}
	return next, nil
}

// ResetReviewFailCount clears any review-fail-count-<n> label (called on a
// successful production so the next failure starts from 1 again).
func (c *Client) ResetReviewFailCount(ctx context.Context, id string) error {
	labels, err := c.beadLabels(ctx, id)
	if err != nil {
		return err
	}
	current := failCountFromLabels(labels)
	if current == 0 {
		return nil
	}
	if _, err := c.Runner.Run(ctx, "update", id, "--remove-label", failLabel(current)); err != nil {
		return fmt.Errorf("reset review fail count %s: %w", id, err)
	}
	return nil
}

// beadLabels returns the labels of one bead via `bd show <id> --json`.
func (c *Client) beadLabels(ctx context.Context, id string) ([]string, error) {
	out, err := c.Runner.Run(ctx, "show", id, "--json")
	if err != nil {
		return nil, fmt.Errorf("show draft-review %s: %w", id, err)
	}
	issues, err := parseBDList(out)
	if err != nil {
		return nil, err
	}
	for _, iss := range issues {
		if iss.ID == id {
			return iss.Labels, nil
		}
	}
	if len(issues) == 1 {
		return issues[0].Labels, nil
	}
	return nil, nil
}

func failLabel(n int) string { return reviewFailLabelPrefix + strconv.Itoa(n) }

func failCountFromLabels(labels []string) int {
	for _, l := range labels {
		if strings.HasPrefix(l, reviewFailLabelPrefix) {
			if n, err := strconv.Atoi(strings.TrimPrefix(l, reviewFailLabelPrefix)); err == nil {
				return n
			}
		}
	}
	return 0
}
