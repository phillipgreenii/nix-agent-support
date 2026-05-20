// Package beads — feedback bead wrappers (custom bd type=feedback, status=hooked).
package beads

import "context"

// CreateFeedbackInput is the typed input for creating a feedback bead.
type CreateFeedbackInput struct {
	ProcessingCycleID string
	Kind              FeedbackKind
	ExternalID        string
	Fingerprint       string
	AuthorRole        AuthorRole
	Title             string
	Body              string
}

// CreateFeedback creates a feedback bead. Phase 0 stub.
func CreateFeedback(_ context.Context, _ CreateFeedbackInput) (id string, err error) {
	return "", ErrNotImplemented
}

// CloseFeedback closes a feedback bead. Phase 0 stub.
func CloseFeedback(_ context.Context, _, _ string) error { return ErrNotImplemented }

// MarkFeedbackResolvedUpstream marks a feedback bead as resolved upstream. Phase 0 stub.
func MarkFeedbackResolvedUpstream(_ context.Context, _ string) error { return ErrNotImplemented }
