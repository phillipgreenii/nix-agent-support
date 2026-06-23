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
// the edge to the parent).
//
// Labels are not populated here — production callers fetch the workspace's
// human-labeled set once via HumanLabeledBeads and overlay it on the
// returned nodes. Tests that need labels do the same.
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
	// Reuse parseBDList which handles both the bd 1.0.4+ envelope
	// ({"data":[...],"schema_version":N}) and the legacy bare-array shape.
	rawIssues, err := parseBDList(trimmed)
	if err != nil {
		return nil, fmt.Errorf("decode bd dep tree json: %w", err)
	}
	raw := rawIssues
	nodes := make([]DepNode, 0, len(raw))
	for _, r := range raw {
		if r.ID == rootID {
			continue
		}
		nodes = append(nodes, DepNode{
			ID:     r.ID,
			Title:  r.Title,
			Status: r.Status,
		})
	}
	return nodes, nil
}

// HumanLabeledBeads returns the set of bead IDs in this workspace that carry
// the `human` label. Used by the sync engine to overlay `human` onto
// DepTreeUp results without per-bead label lookups — replaces one
// `bd label list <id>` call per dep with a single `bd query` per workspace.
//
// `bd query "label=human" --json` returns issue objects. bd 1.0.4+ wraps
// them in an envelope: {"data":[...],"schema_version":N}. Older builds
// returned a bare JSON array. parseBDList handles both shapes; only the id
// field of each issue is consulted here.
//
// bd may also surface errors as a JSON object {"error":"..."} on stdout with
// exit code 0; that case is detected and returned as an error before parsing.
func (c *Client) HumanLabeledBeads(ctx context.Context) (map[string]bool, error) {
	out, err := c.Runner.Run(ctx, "query", "label=human", "--json")
	if err != nil {
		return nil, fmt.Errorf("bd query label=human --json: %w", err)
	}
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return map[string]bool{}, nil
	}
	// bd may surface errors as {"error":"..."} with exit code 0. Detect by
	// trying to decode an error object first; the envelope shape has "data"
	// not "error", so a non-empty error field is unambiguous.
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var errObj struct {
			Error string `json:"error"`
		}
		if jerr := json.Unmarshal([]byte(trimmed), &errObj); jerr == nil && errObj.Error != "" {
			return nil, fmt.Errorf("bd query label=human: %s", errObj.Error)
		}
		// Otherwise it's the standard {"data":[...],"schema_version":N} envelope;
		// fall through to parseBDList.
	}
	issues, err := parseBDList(trimmed)
	if err != nil {
		return nil, fmt.Errorf("decode bd query label=human json: %w", err)
	}
	set := make(map[string]bool, len(issues))
	for _, iss := range issues {
		if iss.ID != "" {
			set[iss.ID] = true
		}
	}
	return set, nil
}

// ApplyHumanLabels overlays the `human` label onto deps whose ID is in set.
// Modifies deps in place to keep the call cheap. Existing labels on each
// node are preserved; the function only ensures `human` is present when
// the set says it should be.
func ApplyHumanLabels(deps []DepNode, set map[string]bool) {
	if len(deps) == 0 || len(set) == 0 {
		return
	}
	for i := range deps {
		if !set[deps[i].ID] {
			continue
		}
		if !hasLabel(deps[i].Labels, "human") {
			deps[i].Labels = append(deps[i].Labels, "human")
		}
	}
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

// PRFeedbackInSubtree returns every feedback bead in prBeadID's recursive
// parent-child subtree (MR -> processing-cycle -> feedback), from a single
// `bd dep tree <pr> --direction=up --json` call. This avoids the per-bead
// isChildOf scan that ListFeedback(cycleID) incurs, so the daemon's per-PR
// feedback handling costs O(1) bd calls regardless of workspace feedback
// volume. Includes feedback of all statuses (open + closed); callers filter
// by Status as needed (the CI-success resolver wants open; the dedup wants all
// fingerprints).
//
// The dep-tree node JSON is the same shape as `bd list --json`, so it is
// decoded with parseBDList; only issue_type=="feedback" nodes contribute, via
// the same feedbackFieldsFromMetadata parser ListFeedback uses.
func (c *Client) PRFeedbackInSubtree(ctx context.Context, prBeadID string) ([]Feedback, error) {
	if strings.TrimSpace(prBeadID) == "" {
		return nil, fmt.Errorf("pr feedback subtree: pr bead id required")
	}
	out, err := c.Runner.Run(ctx, "dep", "tree", prBeadID, "--direction=up", "--json")
	if err != nil {
		return nil, fmt.Errorf("bd dep tree --direction=up: %w", err)
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	issues, err := parseBDList(out)
	if err != nil {
		return nil, fmt.Errorf("decode bd dep tree json: %w", err)
	}
	fbs := make([]Feedback, 0, len(issues))
	for _, iss := range issues {
		if iss.Type != TypeFeedback {
			continue
		}
		fbs = append(fbs, Feedback{
			ID:     iss.ID,
			Title:  iss.Title,
			Status: iss.Status,
			Fields: feedbackFieldsFromMetadata(iss.Metadata),
		})
	}
	return fbs, nil
}
