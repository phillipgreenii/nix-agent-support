package store

import "encoding/json"

// Event is an in-process domain event carried across the commit boundary by the
// outbox. Payload is opaque JSON the handler decodes.
type Event struct {
	Type    string
	Payload json.RawMessage
}

// Event type constants.
const (
	EventPROpened         = "pr.opened"
	EventPRUpdated        = "pr.updated"
	EventPRClosed         = "pr.closed"
	EventPRMerged         = "pr.merged"
	EventFeedbackCreated  = "feedback.created"
	EventFeedbackDisposed = "feedback.disposed"
	EventFeedbackResolved = "feedback.resolved"
)
