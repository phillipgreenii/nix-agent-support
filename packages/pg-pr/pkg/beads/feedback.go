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

// Package-level convenience wrapper using the default Client.

// CloseFeedback closes a feedback bead using the default Client.
func CloseFeedback(ctx context.Context, id, reason string) error {
	return NewClient().CloseFeedback(ctx, id, reason)
}
