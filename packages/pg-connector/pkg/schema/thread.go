// thread.go: the thread entity/capability's shared JSON wire shape, built
// by Phase 13's "Thread v1 schema + claude -p backend + conformance +
// registry" packet (bead pg2-2j5ac.40.3) on top of the Tier-1 core's
// pkg/schema placeholder (see doc.go), using pkg/provider/issue's own
// package shape and issue.go/pr.go's field/doc-comment conventions as its
// structural precedent [design: 4.5, 8].
//
// Unlike issue/pr, thread has no write ops at all (Provider declares only
// Show/List — see pkg/provider/thread/iface.go) and exactly one Tier-2
// backend at this phase (pg-connector-thread-slack, transport-only over
// claude -p against the machine's already-configured Slack MCP) — see
// cmd/pg-connector-thread-slack.
package schema

// ThreadSchemaVersion is the thread capability's own schema version,
// populated into the wire envelope's schemaVersion field by the thread
// capability's dispatch-table entries (pkg/provider/thread.NewDispatchTable)
// — independent of every other capability's own schema version (each
// entity type/capability versions its own schema separately, INV-VER-1) and
// of pkg/scriptout.ProtocolVersion. This is the thread capability's first
// version: it has no prior shape to have bumped from. It is also
// registered into CurrentSchemaVersions in versions.go, so a future
// thread-capability schema-version mismatch is detectable rather than
// silently skipped by cmd/pg-connector/config_validate.go's
// checkSchemaVersions (an unknown-to-this-build capability key is
// otherwise treated as "no opinion," never a mismatch) — mirrors
// AttentionSchemaVersion/SearchSchemaVersion's identical precedent.
const ThreadSchemaVersion = 1

// Thread is the thread capability's shared JSON wire shape, returned by
// the thread capability's "show" op and one element of "list"'s
// ThreadListResult.Entities. The field set is exactly what design section
// 4.5 names — a Slack (or Slack-MCP-shaped) thread's own facts, never a
// model-derived judgment (the compute-only rule, [design: 8]): every field
// below is either a fact the MCP itself returns, or (AsOf/Stale) computed
// deterministically in Go.
type Thread struct {
	// ID is the thread's identity — this backend uses the Slack MCP's own
	// thread/message timestamp identifier (Slack's "ts" value, the
	// conventional thread-identifying token for a Slack thread), a string
	// per every other capability's own "ID is a string, whatever form the
	// backend's own system uses" convention (Issue.ID's doc comment).
	ID string `json:"id"`

	// Channel is the Slack channel (or DM/group) the thread lives in, in
	// whatever form the MCP returns it (a channel id or a human-readable
	// name — this backend does not normalize between the two; see
	// runner.go's own doc comment for the exact prompt shape).
	Channel string `json:"channel"`

	// Permalink is the thread's Slack permalink URL — this docket's sibling
	// packet ("pg-desk run thread") cross-references threads by this value,
	// mirroring how PR/Issue's own URL field is the cross-reference key for
	// those capabilities.
	Permalink string `json:"permalink"`

	// StartedBy is the thread's original poster, in whatever identity form
	// the MCP returns (a Slack user id or display name). Empty when the
	// backend does not supply one.
	StartedBy string `json:"started_by,omitempty"`

	// Participants lists every distinct participant the MCP reports for
	// this thread, in whatever identity form it returns them (mirrors
	// StartedBy). Empty when the backend does not supply this or the
	// thread has no replies yet.
	Participants []string `json:"participants,omitempty"`

	// LastReplyAt is the thread's most recent reply time, in whatever
	// timestamp form the MCP returns (Slack's own "ts"-shaped string, or an
	// RFC3339 string if the MCP already normalizes it — this backend does
	// not reformat it). Empty when the thread has no replies yet, or the
	// backend does not supply one.
	LastReplyAt string `json:"last_reply_at,omitempty"`

	// ReplyCount is the thread's total reply count, as reported by the MCP
	// — a plain fact, not omitempty (0 is itself informative: a thread with
	// no replies yet), mirroring Stale's own "always populated" convention
	// below.
	ReplyCount int `json:"reply_count"`

	// Text is the thread's own root/parent message text.
	Text string `json:"text"`

	// MentionsMe reports whether the configured identity (whatever the
	// Slack MCP itself resolves as "me" — this backend passes no separate
	// identity parameter of its own) is mentioned anywhere in the thread —
	// a fact the MCP itself determines, never inferred by the model's own
	// judgment (the compute-only rule, [design: 8]). Always populated (not
	// omitempty): false is itself informative, mirroring Issue.Stale's own
	// convention.
	MentionsMe bool `json:"mentions_me"`

	// AsOf is this read's own as-of time (RFC3339, UTC), mirroring
	// Issue.AsOf/PR.AsOf's identical INV-ASOF-1 contract: "every acted-on
	// read seam MUST carry its own as-of time." Empty only when this
	// backend has no usable as-of time for this read, which MUST pair with
	// Stale true rather than a plausible-looking but meaningless
	// timestamp.
	AsOf string `json:"as_of"`

	// Stale is this backend's own as-of/stale determination for this read
	// (INV-ASOF-2). pg-connector-thread-slack performs a live claude -p/MCP
	// call on every request with no local cache of Slack's own facts, so it
	// always reports Stale false with AsOf set to that live call's own
	// completion time — mirroring every other current backend's identical
	// precedent (Issue.Stale's own doc comment).
	Stale bool `json:"stale"`
}

// ThreadListResult is the "list" op's wire result payload for the thread
// capability — the same generic shape IssueListResult/PRListResult
// document in full (see IssueListResult's own doc comment for
// Cursor/Entities/PresentIDs/ids_only semantics, which apply identically
// here with Thread in place of Issue), with one binding-decision departure:
// Truncated is UNCONDITIONALLY true for every thread "list" reply
// [design: 4.5, 8, D23] — pg-connector-thread-slack has no reliable way to
// confirm the Slack MCP returned the complete match set, so it always
// hedges rather than computing a conditional value the way
// pg-connector-issue-jira's own List does. Cursor is always nil: this
// capability has no incremental-listing backend at this phase (mirrors
// pkg/provider/issue.Provider.List's own no-cursor-parameter shape).
type ThreadListResult struct {
	Entities   []Thread `json:"entities"`
	PresentIDs []string `json:"present_ids"`
	Cursor     *string  `json:"cursor"`
	Truncated  bool     `json:"truncated"`
}
