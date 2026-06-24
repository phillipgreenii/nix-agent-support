// Package beads — processing-cycle bead wrappers (bd type=task).
//
// A processing-cycle bead represents one round of LLM-driven work on a PR.
// It is the parent of feedback beads created during that round; the LLM
// closes the processing-cycle when it decides the cycle is complete, and
// the sync engine watches that close to drive ci-loop escalation and
// related lifecycle work.
package beads

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ProcessingCycle is a parsed view of a processing-cycle bead.
type ProcessingCycle struct {
	ID           string
	Title        string
	Status       string
	ParentPRID   string
	ClosedReason string
}

// processingCycleTitlePrefix is matched verbatim by FindOpenProcessingCycle.
const processingCycleTitlePrefix = "process-feedback: "

// CreateProcessingCycle creates a new processing-cycle bead and wires a
// parent-child dependency from the merge-request bead to it.
//
// title is appended after the canonical prefix; pass either a short
// descriptor ("foo/bar#42") or the empty string to let the wrapper derive
// the title from prID.
func (c *Client) CreateProcessingCycle(ctx context.Context, prBeadID, title string, mine bool) (string, error) {
	if prBeadID == "" {
		return "", errors.New("processing-cycle: pr bead id required")
	}
	if title == "" {
		title = prBeadID
	}
	fullTitle := processingCycleTitlePrefix + title
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
		return "", fmt.Errorf("create processing-cycle: %w", err)
	}
	id := strings.TrimSpace(out)
	if id == "" {
		return "", errors.New("bd create returned empty ID")
	}
	// Wire parent-child: the processing-cycle bead depends on (is a child
	// of) the merge-request bead.
	if _, err := c.Runner.Run(ctx,
		"dep", "add", id, prBeadID,
		"--type=parent-child",
		"--no-cycle-check",
	); err != nil {
		// Best-effort: log via the error chain. The bead itself was
		// created successfully so we still return the ID.
		return id, fmt.Errorf("link processing-cycle %s to pr %s: %w", id, prBeadID, err)
	}
	return id, nil
}

// FindOpenProcessingCycle returns the open processing-cycle bead linked
// to the given merge-request bead, if one exists. Returns (id, true) on
// hit; ("", false, nil) when none open.
//
// Strategy: resolve the merge-request's children with a SINGLE dependency
// query (ListChildrenOfPR), then intersect with the open process-feedback
// tasks. This replaces the previous approach of scanning every open task and
// probing each with a per-task `isChildOf` dep query — which made O(N) dep
// calls per PR against a slow dolt server.
//
// Critically, every bd error PROPAGATES. The old code's `isChildOf` swallowed
// dep-query errors as `false`, so a single transient bd/dolt failure turned
// into a silent "no open cycle" — and the sync caller responded by creating a
// SECOND cycle for a PR that already had one. That is the root cause of the
// duplicate-cycle accumulation (48 cycles for 27 PRs). Returning an error here
// makes the caller skip creation and retry on the next sync instead.
func (c *Client) FindOpenProcessingCycle(ctx context.Context, prBeadID string) (string, bool, error) {
	if prBeadID == "" {
		return "", false, errors.New("processing-cycle: pr bead id required")
	}
	childIDs, err := c.ListChildrenOfPR(ctx, prBeadID)
	if err != nil {
		return "", false, fmt.Errorf("find open processing-cycle: list children of %s: %w", prBeadID, err)
	}
	if len(childIDs) == 0 {
		return "", false, nil
	}
	isChild := make(map[string]struct{}, len(childIDs))
	for _, id := range childIDs {
		isChild[id] = struct{}{}
	}
	out, err := c.Runner.Run(ctx,
		"list",
		"--type=task",
		"--status=open",
		"--json",
		"--limit=0",
	)
	if err != nil {
		return "", false, fmt.Errorf("find open processing-cycle: list tasks: %w", err)
	}
	issues, err := parseBDList(out)
	if err != nil {
		return "", false, err
	}
	for _, iss := range issues {
		if !strings.HasPrefix(iss.Title, processingCycleTitlePrefix) {
			continue
		}
		if _, ok := isChild[iss.ID]; ok {
			return iss.ID, true, nil
		}
	}
	return "", false, nil
}

// CloseProcessingCycle closes a processing-cycle bead with the given
// reason. Idempotent: closing an already-closed bead is a no-op.
func (c *Client) CloseProcessingCycle(ctx context.Context, id, reason string) error {
	if id == "" {
		return errors.New("processing-cycle: id required")
	}
	args := []string{"close", id}
	if reason != "" {
		args = append(args, "--reason", reason)
	}
	if _, err := c.Runner.Run(ctx, args...); err != nil {
		// bd close on a closed bead is a no-op on recent bd; if older bd
		// errors, swallow the "already closed" path.
		if strings.Contains(err.Error(), "already closed") {
			return nil
		}
		return fmt.Errorf("close processing-cycle: %w", err)
	}
	return nil
}

// ListChildrenOfPR returns the IDs of all bd issues that have a
// parent-child dependency on prBeadID. Used by cascade-on-PR-close.
//
// bd's dependency direction model: `bd dep list <id>` defaults to
// `--direction=down`, which lists the things <id> depends on. To list
// things that depend on <id> (its children) we need `--direction=up`.
func (c *Client) ListChildrenOfPR(ctx context.Context, prBeadID string) ([]string, error) {
	if prBeadID == "" {
		return nil, errors.New("pr bead id required")
	}
	out, err := c.Runner.Run(ctx, "dep", "list", prBeadID, "--direction=up", "--json")
	if err != nil {
		return nil, fmt.Errorf("list children of %s: %w", prBeadID, err)
	}
	ids := extractIDs(out)
	if len(ids) == 0 {
		return nil, nil
	}
	uniq := make([]string, 0, len(ids))
	seen := map[string]struct{}{}
	for _, id := range ids {
		if id == prBeadID {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		uniq = append(uniq, id)
	}
	return uniq, nil
}

// extractIDs scans the bd-dep-list JSON for "id":"..." pairs and returns
// the values. The shape varies across bd versions, so we use a regex-like
// scan rather than depending on a specific layout.
func extractIDs(s string) []string {
	const key = `"id":`
	var out []string
	i := 0
	for {
		k := strings.Index(s[i:], key)
		if k < 0 {
			break
		}
		k += i + len(key)
		// Skip whitespace and an opening quote.
		for k < len(s) && (s[k] == ' ' || s[k] == '"') {
			k++
		}
		// Read until the closing quote.
		end := k
		for end < len(s) && s[end] != '"' {
			end++
		}
		if end > k {
			out = append(out, s[k:end])
		}
		i = end + 1
	}
	return out
}

// Package-level convenience wrappers using the default Client.

// CreateProcessingCycle creates a processing-cycle bead using the default
// Client.
func CreateProcessingCycle(ctx context.Context, prBeadID, title string, mine bool) (string, error) {
	return NewClient().CreateProcessingCycle(ctx, prBeadID, title, mine)
}

// FindOpenProcessingCycle finds an open processing-cycle bead using the
// default Client.
func FindOpenProcessingCycle(ctx context.Context, prBeadID string) (string, bool, error) {
	return NewClient().FindOpenProcessingCycle(ctx, prBeadID)
}

// CloseProcessingCycle closes a processing-cycle bead using the default
// Client.
func CloseProcessingCycle(ctx context.Context, id, reason string) error {
	return NewClient().CloseProcessingCycle(ctx, id, reason)
}
