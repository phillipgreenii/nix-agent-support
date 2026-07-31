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
	HasConflict  bool   `json:"has_conflict,omitempty"`
}

// FeedbackSummary is the substance of a feedback.created event: what the sync
// actually surfaced that still NEEDS PROCESSING on this PR (pg2-onq1e). It is
// computed by UnaddressedFeedback from committed rows, so it is derived state,
// never a snapshot the emitter guesses at.
//
// It exists so the beadsbridge projection can answer two questions WITHOUT a
// store or GitHub round-trip:
//
//   - "is there anything to process at all?" — Unaddressed == 0 means the tick
//     surfaced nothing unaddressed, so NO process-feedback bead may be created.
//     That is what stops the self-feeding loop where an agent's own reply
//     comments and push produced a fresh, substance-free bead.
//   - "is this the SAME work an existing/closed bead already covers?" — Digest
//     is stable over the unaddressed item set, so a bead can carry it as a
//     marker label and a later tick can tell "nothing new" from "new findings".
type FeedbackSummary struct {
	// Unaddressed is the number of feedback items still needing processing.
	Unaddressed int `json:"unaddressed"`
	// ByKind counts the unaddressed items per feedback kind (e.g.
	// {"code-comment-thread":2,"ci-failure":1}). encoding/json sorts map keys,
	// so the marshalled form is deterministic.
	ByKind map[string]int `json:"by_kind,omitempty"`
	// Reviewers is the sorted, de-duplicated set of logins that authored the
	// unaddressed items (empty logins — e.g. ci-failure rows — are omitted).
	Reviewers []string `json:"reviewers,omitempty"`
	// Digest is a short content hash over the SORTED fingerprints of the
	// unaddressed items. It changes iff the unaddressed SET changes, so it is
	// the "genuinely new feedback" test; it does NOT change when only a
	// timestamp, a re-render, or an already-counted item's body reflows.
	Digest string `json:"digest,omitempty"`
}

// FeedbackPayload is the JSON body of feedback.created events. It carries the
// PR identity the beadsbridge handler keys on — (Repo, Number) — plus the
// ownership verdict and, since pg2-onq1e, the Summary describing what needs
// processing.
//
// Summary is a POINTER on purpose: nil means "this emitter did not compute a
// summary" (a legacy outbox row enqueued before the upgrade, or a hand-rolled
// test payload), which the bridge treats as unknown and falls back to its
// pre-pg2-onq1e behaviour. A non-nil Summary with Unaddressed == 0 is a
// POSITIVE statement that there is nothing to process, and suppresses the bead.
type FeedbackPayload struct {
	Repo    string           `json:"repo"`
	Number  int              `json:"number"`
	Mine    bool             `json:"mine"`
	Summary *FeedbackSummary `json:"summary,omitempty"`
}

// AttentionPayload is the JSON body of pr.attention events (pg2-4c5i.13). It
// carries the shared needsAttention verdict so the beadsbridge handler can
// ensure (Need) or close (!Need) the teammate-attention bead. Re-emitted every
// tick from persisted facts, so a dropped fire-once event self-heals (R1).
type AttentionPayload struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Need   bool   `json:"need"`
	Reason string `json:"reason,omitempty"`
}

// Event type constants.
const (
	EventPROpened         = "pr.opened"
	EventPRUpdated        = "pr.updated"
	EventPRClosed         = "pr.closed"
	EventPRMerged         = "pr.merged"
	EventPRAttention      = "pr.attention"
	EventFeedbackCreated  = "feedback.created"
	EventFeedbackDisposed = "feedback.disposed"
	EventFeedbackResolved = "feedback.resolved"
)
