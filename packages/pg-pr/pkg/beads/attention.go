// Package beads — teammate-attention bead wrappers (bd type=task).
//
// An attention bead is a human-facing "this teammate PR needs me" signal. It is
// a child of the merge-request bead, projected by the pg2-4c5i.13 attention
// projector via the emit→bridge pattern. It OPENS when a teammate PR needs my
// review (draft review ready and nobody approved, OR new commits after I
// approved) and CLOSES when it no longer does (a teammate approved, or I
// reviewed the current head). It cascade-closes on PR close/merge because it is
// a child of the merge-request bead.
//
// It reuses the builtin bd `task` type and is discriminated by a title prefix,
// exactly like the draft-review and processing-cycle beads — so no custom-type
// config is needed. It carries a `team` label (it only ever tracks teammate PRs).
//
// This is a near-copy of draftreview.go with ONE deliberate difference in
// lookup semantics: draft-review is a one-shot obligation, so its ensure path
// finds OPEN-OR-CLOSED children (a closed review suppresses recreation forever).
// Attention is a REPEATING open/close signal, so BOTH ensure and close operate
// on the OPEN child only — a closed attention bead must NOT suppress re-opening
// when attention is needed again (design §2.7 / .13 plan §5).
package beads

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// attentionTitlePrefix is matched verbatim by findOpenAttentionChild.
const attentionTitlePrefix = "attention: "

// EnsureAttentionBead ensures an OPEN attention bead exists as a child of
// prBeadID, and returns its ID. title is appended after the canonical prefix;
// the bead carries the `team` label.
//
// Idempotent on re-delivery: if an OPEN attention child already exists it is
// returned without creating a second. Unlike EnsureDraftReviewBead it does NOT
// treat a CLOSED child as suppressing recreation — attention is a repeating
// signal, so a new bead is opened when attention is needed again after a
// not-needed window. A lookup error PROPAGATES (the caller skips and retries
// next tick); it is never treated as "none exists".
func (c *Client) EnsureAttentionBead(ctx context.Context, prBeadID, title string) (string, error) {
	if prBeadID == "" {
		return "", errors.New("attention: pr bead id required")
	}
	existing, err := c.findOpenAttentionChild(ctx, prBeadID)
	if err != nil {
		return "", err // propagate — do NOT treat as "none exists"
	}
	if existing != "" {
		return existing, nil // an OPEN child already exists → do not recreate
	}
	if title == "" {
		title = prBeadID
	}
	fullTitle := attentionTitlePrefix + title
	out, err := c.Runner.Run(ctx,
		"create",
		"--type=task",
		"--title", fullTitle,
		"-d", fullTitle,
		"--silent",
		"-l", "team",
	)
	if err != nil {
		return "", fmt.Errorf("create attention: %w", err)
	}
	id := strings.TrimSpace(out)
	if id == "" {
		return "", errors.New("bd create returned empty ID")
	}
	// Wire parent-child: the attention bead depends on (is a child of) the
	// merge-request bead — so cascadeClose closes it on PR close/merge.
	if _, err := c.Runner.Run(ctx,
		"dep", "add", id, prBeadID,
		"--type=parent-child",
		"--no-cycle-check",
	); err != nil {
		return id, fmt.Errorf("link attention %s to pr %s: %w", id, prBeadID, err)
	}
	return id, nil
}

// CloseAttentionBead closes the OPEN attention child of prBeadID, if one exists.
// Idempotent: a no-op when no open attention child is present. A lookup error
// PROPAGATES (mirrors the ensure path). reason is passed through to bd close.
func (c *Client) CloseAttentionBead(ctx context.Context, prBeadID, reason string) error {
	if prBeadID == "" {
		return errors.New("attention: pr bead id required")
	}
	open, err := c.findOpenAttentionChild(ctx, prBeadID)
	if err != nil {
		return err // propagate — do NOT treat as "nothing to close"
	}
	if open == "" {
		return nil // idempotent: nothing open to close
	}
	return c.CloseProcessingCycle(ctx, open, reason)
}

// findOpenAttentionChild returns the ID of the OPEN attention child of prBeadID,
// or "" when none. Strategy mirrors FindOpenProcessingCycle (OPEN only — a closed
// attention bead does NOT suppress re-opening): resolve the PR's children once
// (ListChildrenOfPR), then intersect with OPEN `task` beads whose title carries
// the attention prefix. Every bd error PROPAGATES.
func (c *Client) findOpenAttentionChild(ctx context.Context, prBeadID string) (string, error) {
	childIDs, err := c.ListChildrenOfPR(ctx, prBeadID)
	if err != nil {
		return "", fmt.Errorf("find attention child: list children of %s: %w", prBeadID, err)
	}
	if len(childIDs) == 0 {
		return "", nil
	}
	isChild := make(map[string]struct{}, len(childIDs))
	for _, id := range childIDs {
		isChild[id] = struct{}{}
	}
	out, err := c.Runner.Run(ctx,
		"list",
		"--type=task",
		"--status=open", // OPEN only — attention re-opens after a not-needed window
		"--json",
		"--limit=0",
	)
	if err != nil {
		return "", fmt.Errorf("find attention child: list tasks: %w", err)
	}
	issues, err := parseBDList(out)
	if err != nil {
		return "", err
	}
	for _, iss := range issues {
		if !strings.HasPrefix(iss.Title, attentionTitlePrefix) {
			continue
		}
		if _, ok := isChild[iss.ID]; ok {
			return iss.ID, nil
		}
	}
	return "", nil
}
