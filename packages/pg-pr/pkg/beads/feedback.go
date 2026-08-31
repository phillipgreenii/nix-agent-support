// Package beads — feedback bead wrappers (custom bd type=feedback,
// status=hooked).
//
// Feedback beads are children of a processing-cycle bead. Each represents
// a single upstream event the LLM may address (a comment thread, a CI
// failure, a review thread, …) and carries a fingerprint so the sync
// engine can dedup across runs.
package beads

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// FeedbackFields is the metadata schema persisted on each feedback bead.
type FeedbackFields struct {
	Kind        string `json:"kind"`
	ExternalID  string `json:"external_id,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	AuthorRole  string `json:"author_role,omitempty"`
	// ReplyDraft holds the body the LLM wants the sync engine to post as a
	// reply to the upstream thread. The sync engine reads it on the next
	// pass, posts via vcs.ReplyToThread, and writes ResponseID back.
	ReplyDraft string `json:"reply_draft,omitempty"`
	// ResponseID is the upstream comment id returned by ReplyToThread; its
	// presence is the idempotency marker — sync skips posting again when set.
	ResponseID string `json:"response_id,omitempty"`
}

// Feedback is a parsed view of a feedback bead.
type Feedback struct {
	ID     string
	Title  string
	Status string
	Fields FeedbackFields
}

// CloseFeedback closes a feedback bead with the given reason.
func (c *Client) CloseFeedback(ctx context.Context, id, reason string) error {
	if id == "" {
		return errors.New("feedback: id required")
	}
	args := []string{"close", id}
	if reason != "" {
		args = append(args, "--reason", reason)
	}
	if _, err := c.Runner.Run(ctx, args...); err != nil {
		if strings.Contains(err.Error(), "already closed") {
			return nil
		}
		return fmt.Errorf("close feedback: %w", err)
	}
	return nil
}

func feedbackFieldsFromMetadata(m map[string]any) FeedbackFields {
	f := FeedbackFields{}
	for k, v := range m {
		switch k {
		case "kind":
			f.Kind = asString(v)
		case "external_id":
			f.ExternalID = asString(v)
		case "fingerprint":
			f.Fingerprint = asString(v)
		case "author_role":
			f.AuthorRole = asString(v)
		case "reply_draft":
			f.ReplyDraft = asString(v)
		case "response_id":
			f.ResponseID = asString(v)
		}
	}
	return f
}

// ListFeedbackBeadIDs returns the IDs of all beads with type=feedback, in any
// status (open or closed). It is used by the migrate-feedback command to find
// legacy feedback beads that predate the store and should be closed.
func (c *Client) ListFeedbackBeadIDs(ctx context.Context) ([]string, error) {
	// --limit=0 disables bd's default result cap so the migration sees every
	// legacy feedback bead (matches ListMergeRequests); --all includes closed.
	out, err := c.Runner.Run(ctx, "list", "--type=feedback", "--all", "--json", "--limit=0")
	if err != nil {
		return nil, fmt.Errorf("list feedback beads: %w", err)
	}
	issues, err := parseBDList(out)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(issues))
	for _, iss := range issues {
		ids = append(ids, iss.ID)
	}
	return ids, nil
}

// ListFeedbackChildrenOfCycle returns the IDs of feedback beads (any status)
// that are children — parent-child dependents — of the given process-feedback
// cycle bead. It is the PARENT-SCOPED counterpart ListFeedbackBeadIDs lacks
// (pg2-kij93): that method enumerates every feedback bead in the entire
// workspace with no way to filter by parent, so it cannot be used to find
// "the feedback under THIS cycle" without also touching every unrelated
// feedback bead in the database. cascadeClose (internal/beadsbridge) needs
// exactly that scoped lookup to close a cycle's feedback grandchildren when
// its PR reaches a terminal state, without ever enumerating the whole
// feedback population.
//
// It deliberately SHARES its mechanism with ListChildrenOfPR — both are a
// parent-child "dep list --direction=up" scoped to one bead — rather than
// re-implementing the same query a second time; the name here documents the
// caller's intent (find a cycle's feedback children) rather than introducing
// a second code path.
func (c *Client) ListFeedbackChildrenOfCycle(ctx context.Context, cycleID string) ([]string, error) {
	if cycleID == "" {
		return nil, errors.New("feedback: cycle id required")
	}
	return c.ListChildrenOfPR(ctx, cycleID)
}

// Package-level convenience wrapper using the default Client.

// CloseFeedback closes a feedback bead using the default Client.
func CloseFeedback(ctx context.Context, id, reason string) error {
	return NewClient().CloseFeedback(ctx, id, reason)
}
