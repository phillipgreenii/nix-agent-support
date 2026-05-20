// Package beads — action bead wrappers (bd builtin type=task or type=bug).
//
// Action beads are concrete things to do that the LLM creates while
// processing feedback. They live under a merge-request bead (parent-child)
// and carry discovered-from links back to the feedback beads they address.
package beads

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ActionKind enumerates the canonical action kinds the LLM may create.
type ActionKind string

const (
	ActionKindFixCI           ActionKind = "fix-ci"
	ActionKindRespond         ActionKind = "respond"
	ActionKindApplySuggestion ActionKind = "apply-suggestion"
	ActionKindRefactor        ActionKind = "refactor"
	ActionKindDeferToFuturePR ActionKind = "defer-to-future-pr"
)

// CreateActionInput is the typed input for creating an action bead.
type CreateActionInput struct {
	MergeRequestID    string
	AddressesFeedback []string
	Kind              ActionKind
	BdType            string // "task" or "bug"; defaults to "task"
	Title             string
	Body              string
}

// CreateAction creates an action bead and wires:
//
//   - parent-child dep from the merge-request bead to the new action.
//   - discovered-from dep from each AddressesFeedback id to the action.
//
// The bd type defaults to task; pass BdType="bug" when the action represents
// a bug to fix.
func (c *Client) CreateAction(ctx context.Context, in CreateActionInput) (string, error) {
	if in.MergeRequestID == "" {
		return "", errors.New("action: merge-request id required")
	}
	if in.Kind == "" {
		return "", errors.New("action: kind required")
	}
	if strings.TrimSpace(in.Title) == "" {
		return "", errors.New("action: title required")
	}
	bdType := in.BdType
	if bdType == "" {
		bdType = "task"
	}
	if bdType != "task" && bdType != "bug" {
		return "", fmt.Errorf("action: bd type %q is not allowed (use task or bug)", bdType)
	}
	body := in.Body
	if strings.TrimSpace(body) == "" {
		body = in.Title
	}
	out, err := c.Runner.Run(ctx,
		"create",
		"--type="+bdType,
		"--title", in.Title,
		"-d", body,
		"--silent",
		// kind is stored as metadata so we can query it later.
		"--metadata", fmt.Sprintf(`{"kind":%q}`, string(in.Kind)),
	)
	if err != nil {
		return "", fmt.Errorf("create action: %w", err)
	}
	id := strings.TrimSpace(out)
	if id == "" {
		return "", errors.New("bd create returned empty ID")
	}

	// Parent-child link from the merge-request to the action.
	if _, err := c.Runner.Run(ctx,
		"dep", "add", id, in.MergeRequestID,
		"--type=parent-child",
		"--no-cycle-check",
	); err != nil {
		return id, fmt.Errorf("link action %s to merge-request %s: %w", id, in.MergeRequestID, err)
	}

	// Discovered-from link for each feedback bead this action addresses.
	for _, fbID := range in.AddressesFeedback {
		fbID = strings.TrimSpace(fbID)
		if fbID == "" {
			continue
		}
		if _, err := c.Runner.Run(ctx,
			"dep", "add", id, fbID,
			"--type=discovered-from",
			"--no-cycle-check",
		); err != nil {
			return id, fmt.Errorf("link action %s discovered-from %s: %w", id, fbID, err)
		}
	}
	return id, nil
}

// Package-level convenience wrapper using the default Client.

// CreateAction creates an action bead using the default Client.
func CreateAction(ctx context.Context, in CreateActionInput) (string, error) {
	return NewClient().CreateAction(ctx, in)
}
