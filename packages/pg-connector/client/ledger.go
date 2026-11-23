package client

import "time"

// LedgerError mirrors the connector ledger's last_error: when a fetch failed
// (or came back partial) and a low-cardinality code for why.
type LedgerError struct {
	At   time.Time `json:"at"`
	Code string    `json:"code"`
}

// LedgerConsumer is the part of a ledger row's per-consumer state a reader
// needs: when the consumer last polled the row.
type LedgerConsumer struct {
	LastSeen *time.Time `json:"last_seen"`
}

// LedgerRow is the subset of one `pg-connector ledger show` row that a
// freshness reader needs; ledger show prints an array of rows. RefreshedAt
// and LastError are null when the connector has recorded none.
type LedgerRow struct {
	Type        string       `json:"type"`
	Backend     string       `json:"backend"`
	Query       string       `json:"query"`
	Instance    string       `json:"instance,omitempty"`
	RefreshedAt *time.Time   `json:"refreshed_at"`
	LastError   *LedgerError `json:"last_error"`
	// Consumers maps a consumer name (pg-router) to its cursor state.
	Consumers map[string]LedgerConsumer `json:"consumers,omitempty"`
}
