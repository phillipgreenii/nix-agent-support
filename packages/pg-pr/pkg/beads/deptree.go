package beads

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// DepNode is one bead in a recursive dependency walk.
type DepNode struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Status string   `json:"status"`
	Labels []string `json:"labels"`
}

// DepTreeUp returns all beads that recursively depend on rootID (i.e. beads
// in the "up" direction from rootID). The root itself is NOT included.
//
// Implementation: `bd dep tree <rootID> --direction=up --json` produces a
// flat JSON array. The first entry is the root; subsequent entries are
// dependents at increasing depth (parent_id and edge_from_parent describe
// the edge to the parent). The dep-tree JSON does carry a "labels" field
// when present on a bead, but we still fetch labels via
// `bd label list <id> --json` for each bead so the surface stays explicit
// and tolerant of beads where the inline field is omitted (e.g. closed
// beads in older bd revisions).
func (c *Client) DepTreeUp(ctx context.Context, rootID string) ([]DepNode, error) {
	if strings.TrimSpace(rootID) == "" {
		return nil, fmt.Errorf("dep tree: root id required")
	}
	out, err := c.Runner.Run(ctx, "dep", "tree", rootID, "--direction=up", "--json")
	if err != nil {
		return nil, fmt.Errorf("bd dep tree --direction=up: %w", err)
	}
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return nil, nil
	}
	var raw []struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return nil, fmt.Errorf("decode bd dep tree json: %w", err)
	}
	nodes := make([]DepNode, 0, len(raw))
	for _, r := range raw {
		if r.ID == rootID {
			continue
		}
		labels, err := c.fetchLabels(ctx, r.ID)
		if err != nil {
			return nil, fmt.Errorf("fetch labels for %s: %w", r.ID, err)
		}
		nodes = append(nodes, DepNode{
			ID:     r.ID,
			Title:  r.Title,
			Status: r.Status,
			Labels: labels,
		})
	}
	return nodes, nil
}

// fetchLabels returns the labels for a single bead. Empty slice when bd
// returns no labels.
//
// `bd label list <id> --json` returns a flat JSON array of strings, e.g.
// `["human","needs-triage"]`, or `[]` when the bead has no labels.
func (c *Client) fetchLabels(ctx context.Context, id string) ([]string, error) {
	out, err := c.Runner.Run(ctx, "label", "list", id, "--json")
	if err != nil {
		return nil, fmt.Errorf("bd label list %s --json: %w", id, err)
	}
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return nil, nil
	}
	var labels []string
	if err := json.Unmarshal([]byte(trimmed), &labels); err != nil {
		return nil, fmt.Errorf("decode labels for %s: %w", id, err)
	}
	return labels, nil
}

// AllNonClosedHumanLabeled reports whether every non-closed dep carries the
// `human` label. An empty non-closed set returns false: a PR with nothing
// pending is not "waiting on me".
func AllNonClosedHumanLabeled(deps []DepNode) bool {
	anyOpen := false
	for _, d := range deps {
		if d.Status == "closed" {
			continue
		}
		anyOpen = true
		if !hasLabel(d.Labels, "human") {
			return false
		}
	}
	return anyOpen
}

// hasLabel reports whether want is present in labels.
func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}
