// Package beads — draft-review bead wrappers (bd type=task).
//
// A draft-review bead represents the obligation to review one PR. It is a
// child of the merge-request bead, created by the beadsbridge when sync first
// detects a PR (for my PRs, or teammate PRs once out of draft). An agent claims
// it and produces the review; routing the review output is handled separately
// (pg2-4c5i.34 / .35).
//
// It reuses the builtin bd `task` type and is discriminated by a title prefix,
// exactly like the processing-cycle bead — so no custom-type config is needed.
package beads

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// draftReviewTitlePrefix is matched verbatim by findDraftReviewChild.
const draftReviewTitlePrefix = "draft-review: "

// EnsureDraftReviewBead ensures exactly one draft-review bead (open OR closed)
// exists as a child of prBeadID, and returns its ID. title is appended after
// the canonical prefix; mine adds the `mine` ownership label so downstream
// routing (pg2-4c5i.34/.35) can distinguish my PRs from teammate PRs.
//
// Idempotent on re-delivery: if a draft-review child already exists it is
// returned without creating a second. It MUST NOT resurrect a closed
// draft-review bead — a completed review (closed bead) suppresses recreation.
// A lookup error PROPAGATES (the caller skips and retries next tick); it is
// never treated as "none exists" (that is the duplicate-cycle bug,
// processingcycle.go:84-90).
func (c *Client) EnsureDraftReviewBead(ctx context.Context, prBeadID, title string, mine bool) (string, error) {
	if prBeadID == "" {
		return "", errors.New("draft-review: pr bead id required")
	}
	existing, err := c.findDraftReviewChild(ctx, prBeadID)
	if err != nil {
		return "", err // propagate — do NOT treat as "none exists"
	}
	if existing != "" {
		return existing, nil // open or closed → do not recreate
	}
	if title == "" {
		title = prBeadID
	}
	fullTitle := draftReviewTitlePrefix + title
	createArgs := []string{
		"create",
		"--type=task",
		"--title", fullTitle,
		"-d", fullTitle,
		"--silent",
	}
	if mine {
		createArgs = append(createArgs, "-l", "mine")
	}
	out, err := c.Runner.Run(ctx, createArgs...)
	if err != nil {
		return "", fmt.Errorf("create draft-review: %w", err)
	}
	id := strings.TrimSpace(out)
	if id == "" {
		return "", errors.New("bd create returned empty ID")
	}
	// Wire parent-child: the draft-review bead depends on (is a child of) the
	// merge-request bead.
	if _, err := c.Runner.Run(ctx,
		"dep", "add", id, prBeadID,
		"--type=parent-child",
		"--no-cycle-check",
	); err != nil {
		return id, fmt.Errorf("link draft-review %s to pr %s: %w", id, prBeadID, err)
	}
	return id, nil
}

// findDraftReviewChild returns the ID of an existing draft-review child of
// prBeadID (open OR closed), or "" when none. Strategy mirrors
// FindOpenProcessingCycle but INCLUDES closed beads (so a completed review
// suppresses recreation): resolve the PR's children once (ListChildrenOfPR),
// then intersect with `task` beads (open + closed, via --all) whose title
// carries the draft-review prefix. Every bd error PROPAGATES.
func (c *Client) findDraftReviewChild(ctx context.Context, prBeadID string) (string, error) {
	childIDs, err := c.ListChildrenOfPR(ctx, prBeadID)
	if err != nil {
		return "", fmt.Errorf("find draft-review child: list children of %s: %w", prBeadID, err)
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
		"--all", // include closed — required for no-resurrection
		"--json",
		"--limit=0",
	)
	if err != nil {
		return "", fmt.Errorf("find draft-review child: list tasks: %w", err)
	}
	issues, err := parseBDList(out)
	if err != nil {
		return "", err
	}
	for _, iss := range issues {
		if !strings.HasPrefix(iss.Title, draftReviewTitlePrefix) {
			continue
		}
		if _, ok := isChild[iss.ID]; ok {
			return iss.ID, nil
		}
	}
	return "", nil
}
