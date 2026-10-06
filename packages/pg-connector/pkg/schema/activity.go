// activity.go: the activity capability's shared JSON wire shape. Activity is
// range-shaped and stateless: one wire op, list_activity, takes a time range
// in its op ARGS (never the config channel) and returns the happenings the
// source recorded inside it. There is no cursor, no ledger and no cache
// entry. The envelope below is the contract; ActivityItem.Fields is opaque
// to the umbrella.
package schema

import "encoding/json"

// ActivitySchemaVersion is the activity capability's own schema version,
// independent of every other capability's version (INV-VER-1). It is
// registered into CurrentSchemaVersions in versions.go so schema skew
// against a backend is detectable by config validate.
const ActivitySchemaVersion = 1

// ActivityItem is one source's own record of one thing the operator did.
type ActivityItem struct {
	// ID is unique within the emitting backend and stable across pulls: the
	// same real-world happening MUST produce the same ID every time it is
	// returned (this is what makes overlapping pulls idempotent downstream).
	ID string `json:"id"`
	// Kind names what happened, dotted "<entity_type>.<verb>" by convention
	// (pr.merged, issue.transitioned, commit, session). An open vocabulary:
	// each backend declares the kinds it emits in its capabilities
	// vocabulary.
	Kind string `json:"kind"`
	// EntityType and EntityID name the thing the happening is about, in that
	// type's own id form (pr: OWNER/REPO#N, issue: KEY or bead id).
	// EntityType is a source-defined string, not a closed enum; it need not
	// be a registered connector type (commit, session).
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
	// OccurredAt is when it happened, RFC3339 with offset, taken from the
	// system of record, never the time of the pull. Required: a happening
	// the backend cannot date MUST NOT be emitted at all. A bounded
	// imprecision the system itself imposes (day-granular data) MUST set
	// Approximate; a backend MUST NOT substitute a different timestamp
	// (an entity's updated_at for a comment's own time) silently.
	OccurredAt  string `json:"occurred_at"`
	Approximate bool   `json:"approximate,omitempty"`
	// Summary is one human-readable line; URL is optional.
	Summary string `json:"summary"`
	URL     string `json:"url,omitempty"`
	// Labels are opaque "key:value" tags a consumer may index on (repo:OWNER/NAME,
	// project:KEY, workspace:NAME, branch:NAME). Optional.
	Labels []string `json:"labels,omitempty"`
	// Fields is the kind-specific payload, an object, opaque to the umbrella.
	Fields json.RawMessage `json:"fields"`
	// AsOf/Stale: INV-ASOF-1/2, same contract every other capability carries.
	AsOf  string `json:"as_of"`
	Stale bool   `json:"stale"`
}

// ActivityListArgs is the list_activity op's args.
type ActivityListArgs struct {
	// Since is inclusive, RFC3339 with offset. Empty means open-ended: the
	// start of the backend's own record (INV-RANGE-1 names an open-ended
	// range as legal); Truncated then carries whatever cap the system imposes.
	Since string `json:"since,omitempty"`
	// Before is exclusive, RFC3339 with offset, always present (the umbrella
	// fills "now" when the caller gives none).
	Before string `json:"before"`
}

// ActivityListResult is the list_activity op's result.
type ActivityListResult struct {
	// Items is the happenings in range. A backend SHOULD emit [] rather
	// than null for an empty result; the umbrella verb normalizes a null to
	// [] before output.
	Items []ActivityItem `json:"items"`
	// Truncated is true when the backend or its system capped the result
	// (GitHub search's 1000-result cap, a page limit): the caller MUST treat
	// the range as incompletely covered.
	Truncated bool `json:"truncated"`
}
