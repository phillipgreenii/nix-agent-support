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
func (c *Client) CreateProcessingCycle(ctx context.Context, prBeadID, title string) (string, error) {
	if prBeadID == "" {
		return "", errors.New("processing-cycle: pr bead id required")
	}
	if title == "" {
		title = prBeadID
	}
	fullTitle := processingCycleTitlePrefix + title
	out, err := c.Runner.Run(ctx,
		"create",
		"--type=task",
		"--title", fullTitle,
		"-d", fullTitle,
		"--silent",
	)
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
func (c *Client) FindOpenProcessingCycle(ctx context.Context, prBeadID string) (string, bool, error) {
	if prBeadID == "" {
		return "", false, errors.New("processing-cycle: pr bead id required")
	}
	// Strategy: list dependents of the merge-request bead (children) filtered
	// to open tasks whose title carries the canonical prefix.
	out, err := c.Runner.Run(ctx,
		"list",
		"--type=task",
		"--status=open",
		"--json",
		"--limit=0",
	)
	if err != nil {
		return "", false, fmt.Errorf("list processing-cycles: %w", err)
	}
	issues, err := parseBDList(out)
	if err != nil {
		return "", false, err
	}
	for _, iss := range issues {
		if !strings.HasPrefix(iss.Title, processingCycleTitlePrefix) {
			continue
		}
		// Verify the parent-child link points at prBeadID.
		if c.isChildOf(ctx, iss.ID, prBeadID) {
			return iss.ID, true, nil
		}
	}
	return "", false, nil
}

// isChildOf returns true when childID has a parent-child dependency on
// parentID. Best-effort: errors silently return false so we never claim
// a wrong match.
func (c *Client) isChildOf(ctx context.Context, childID, parentID string) bool {
	out, err := c.Runner.Run(ctx, "dep", "list", childID, "--json")
	if err != nil {
		return false
	}
	if !strings.Contains(out, parentID) {
		return false
	}
	// Coarse string match is good enough because parentID is a deterministic
	// bd id (e.g., "beads_pg2-299"); collisions are essentially impossible
	// across the project's prefix space.
	return true
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
func CreateProcessingCycle(ctx context.Context, prBeadID, title string) (string, error) {
	return NewClient().CreateProcessingCycle(ctx, prBeadID, title)
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
