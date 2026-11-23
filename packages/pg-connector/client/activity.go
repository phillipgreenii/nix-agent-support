package client

import "encoding/json"

// ActivitySourceResult is one sources[] row of an `activity list` answer: the
// standard fan-out row plus the truncated flag the activity verb adds.
type ActivitySourceResult struct {
	SourceResult
	Truncated bool `json:"truncated"`
}

// ActivityListItem is one items[] element of an `activity list` answer. Item
// stays raw so a caller can decode (and reject) it per item, so one bad item
// never costs the rest.
type ActivityListItem struct {
	Source string          `json:"source"`
	Item   json.RawMessage `json:"item"`
}

// ActivityListOutcome is an `activity list` verb's response: one sources[]
// row per queried backend and a flat items list.
type ActivityListOutcome struct {
	Sources []ActivitySourceResult `json:"sources"`
	Items   []ActivityListItem     `json:"items"`
}

// ActivityItem is the activity item envelope inside ActivityListItem.Item.
// Fields stays raw: its shape depends on Kind.
type ActivityItem struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	EntityType  string          `json:"entity_type"`
	EntityID    string          `json:"entity_id"`
	OccurredAt  string          `json:"occurred_at"`
	Approximate bool            `json:"approximate"`
	Summary     string          `json:"summary"`
	URL         string          `json:"url"`
	Labels      []string        `json:"labels"`
	Fields      json.RawMessage `json:"fields"`
	AsOf        string          `json:"as_of"`
	Stale       bool            `json:"stale"`
}
