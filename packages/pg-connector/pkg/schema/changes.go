// changes.go: the wire shapes of the pr instance's `changes` and `changes_ack`
// ops, served by the pg-connector-github daemon from its change log (bead
// pg2-z5fax.4.8; PRSchemaVersion 12). The shape extends the umbrella's own
// changes answer (sources[] of {backend, status, version, truncated, reason}
// and changes[] of {change, source, entity}): each change gains seq, kinds
// and fields, each source gains next_seq, and the answer gains
// sources_freshness[].
package schema

import "encoding/json"

// ChangeSource is one sources[] row of a changes answer. Backend and Status
// keep the umbrella's names: the daemon fills Backend with the instance's
// registry name and Status with "succeeded" (a poll that fails is an error, not
// a degraded row); the umbrella MAY overwrite them.
type ChangeSource struct {
	Backend string `json:"backend"`
	Status  string `json:"status"`
	// Version is the daemon's per-key position count: the key's acknowledged
	// sequence number, so a reader that compares it keeps working.
	Version   int64  `json:"version"`
	Truncated bool   `json:"truncated"`
	Reason    string `json:"reason,omitempty"`
	// NextSeq is the log position scanned up to; it can be past the last
	// returned row because rows of non-members are skipped. The consumer
	// acknowledges it with changes_ack once it has flushed the rows.
	NextSeq int64 `json:"next_seq"`
}

// PRChange is one changes[] entry. Change is "added", "removed" or "changed"
// for the polling key; Entity is the summary-level PR with its Version and
// HeadSHA set to the ROW'S OWN recorded values (the current ones are not used),
// so a redelivered row keeps its identity after a newer change. Seq is the
// change log's sequence number (0 for a reset replay entry); Kinds and Fields
// are the row's own, each an empty JSON array rather than null when there are
// none.
type PRChange struct {
	Change string          `json:"change"`
	Source string          `json:"source"`
	Entity PR              `json:"entity"`
	Seq    int64           `json:"seq"`
	Kinds  []string        `json:"kinds"`
	Fields json.RawMessage `json:"fields"`
}

// SourceFreshness is one sources_freshness[] entry: a configured query's
// last whole-query origin success and last error. The times are RFC 3339 UTC
// strings and are omitted when there is none.
type SourceFreshness struct {
	Query         string `json:"query"`
	LastSuccessAt string `json:"last_success_at,omitempty"`
	LastError     string `json:"last_error,omitempty"`
}

// PRChangesResult is the answer of the pr instance's changes op. Every array is
// an empty array, never null.
type PRChangesResult struct {
	Sources          []ChangeSource    `json:"sources"`
	Changes          []PRChange        `json:"changes"`
	SourcesFreshness []SourceFreshness `json:"sources_freshness"`
}

// ChangesAckResult is the answer of the changes_ack op: the key's stored
// position after the acknowledgement.
type ChangesAckResult struct {
	Consumer string `json:"consumer"`
	Query    string `json:"query"`
	AckedSeq int64  `json:"acked_seq"`
}
