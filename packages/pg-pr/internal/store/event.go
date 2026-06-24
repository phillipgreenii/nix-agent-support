package store

import "encoding/json"

// Event is an in-process domain event carried across the commit boundary by the
// outbox. Payload is opaque JSON the handler decodes.
type Event struct {
	Type    string
	Payload json.RawMessage
}

// PRPayload is the JSON body of pr.* lifecycle events. It carries everything
// the beadsbridge handler needs to project a complete merge-request bead.
type PRPayload struct {
	Repo         string `json:"repo"`
	Number       int    `json:"number"`
	Title        string `json:"title"`
	Ownership    string `json:"ownership"`
	Merged       bool   `json:"merged"`
	State        string `json:"state,omitempty"`
	Branch       string `json:"branch,omitempty"`
	Base         string `json:"base,omitempty"`
	Author       string `json:"author,omitempty"`
	URL          string `json:"url,omitempty"`
	Draft        bool   `json:"draft,omitempty"`
	LastSyncedAt string `json:"last_synced_at,omitempty"`
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
