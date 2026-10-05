// Package changes holds the wire types of contract pg-desk.changes/v1 — the
// change envelope pg-desk hands to a consumer (design 9.3) — and the small
// pure helpers shared by every verb that renders change records.
package changes

// Contract is the contract string every envelope carries.
const Contract = "pg-desk.changes/v1"

// Source status values [design 9.3].
const (
	StatusOK       = "ok"
	StatusDegraded = "degraded"
	StatusFailed   = "failed"
)

// Cursor is the consumer's change_log position around one call: From is the
// cursor before the call, To the highest seq delivered in it (equal to From
// when nothing was delivered).
type Cursor struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
}

// Source is one watched query the call consulted. Reason is set only when
// Status is not ok.
type Source struct {
	Query  string `json:"query"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// Record is one change record. ID is pg-desk's canonical entity id for the
// type; Title is display only and MUST NOT be used for decisions.
type Record struct {
	Seq     int64    `json:"seq"`
	Type    string   `json:"type"`
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Version int64    `json:"version"`
	Kinds   []string `json:"kinds"`
	Origin  string   `json:"origin"`
	At      string   `json:"at"`
}

// Envelope is the body of one `pg-desk <type> changes` call. Consumer is the
// --consumer name of the call; Sources has one row per watched query consulted.
type Envelope struct {
	Contract string   `json:"contract"`
	Type     string   `json:"type"`
	Consumer string   `json:"consumer"`
	Cursor   Cursor   `json:"cursor"`
	Sources  []Source `json:"sources"`
	Records  []Record `json:"records"`
}
