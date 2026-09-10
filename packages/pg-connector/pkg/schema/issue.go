// issue.go: the issue entity/capability's shared JSON wire shape, built by
// the "generic issue entity/capability" packet on top of the Tier-1 core's
// pkg/schema placeholder (see doc.go) and the already-landed pr capability
// packet's own pr.go as its structural precedent.
//
// The field set is carried over from this repo's existing
// packages/pg-pr/pkg/api.Issue struct (ID, Title, State, URL, Priority,
// Labels, IssueType), which today backs pg-pr's read-only
// issues.Provider.GetIssue. This packet's issue.Provider (see
// pkg/provider/issue) widens that shape from read-only to read+write
// (create/comment/transition) so Issue can be a full connector, not just a
// mirror — the field set itself does not change to do that.
package schema

// IssueSchemaVersion is the issue capability's own schema version,
// populated into the wire envelope's schemaVersion field by each of the
// issue capability's dispatch-table entries
// (pkg/provider/issue.NewDispatchTable) — independent of pr's own
// PRSchemaVersion (each entity type/capability versions its own schema
// separately) and of pkg/scriptout.ProtocolVersion.
//
// Bumped 1 -> 2 by bead pg2-1q9c0 (design review finding A9), which added
// the Tracker field below — per INV-VER-1, schemaVersion "versions that
// capability's own field shape," and this is the first field-shape change
// since the capability's initial version. No existing consumer exists yet
// (the issue capability has one Tier-2 backend, pg-connector-issue-beads;
// Phase 2's issue-jira has not landed), so the bump has no live compat
// impact today — it is here so a future consumer's version_mismatch
// handling is exercised against a real precedent rather than a hypothetical
// one.
//
// Bumped 2 -> 3 by bead pg2-akfw5 (review finding A-33), which added
// Description/Assignee/Parent/Deps
// below — same "any field-shape change bumps the version" precedent as the
// 1 -> 2 bump.
//
// Bumped 3 -> 4 by bead pg2-2j5ac.28.1, which added the
// "list" op and its IssueListResult wire shape below — same "a capability
// gaining a whole new op's wire shape bumps its one schemaVersion integer"
// precedent pkg/schema/pr.go's own 2 -> 3 bump records for the sibling pr
// capability.
//
// Bumped 4 -> 5 by bead pg2-2j5ac.28.3, which added the AsOf/Stale pair
// (mirroring schema.PR's own pg2-681xo precedent exactly) plus
// UpdatedAt/DueDate/Metadata/ExternalRefs below, and widened
// pkg/provider/issue.Provider with the update/close/deps ops — same
// "any field-shape change, or a capability gaining a whole new op's wire
// shape, bumps its one schemaVersion integer" precedent as every earlier
// bump on this constant.
//
// NOTE on the numeral itself: this bead's own drafting text anticipated a
// "v3 -> v4" bump, written before bead pg2-2j5ac.28.1's List addition
// landed and consumed v4 first — the two beads' schema work was drafted
// against the same starting v3 but landed in the opposite order from what
// the drafting text assumed. This is v4 -> v5, the correct next integer
// given actual landing order; the invariant itself ("this bead bumps the
// version by exactly one, to cover its own new field shape") holds
// regardless of which specific numeral that lands on.
const IssueSchemaVersion = 5

// Issue is the issue capability's shared JSON wire shape, returned by the
// issue capability's "show" and "create" ops and carried by
// pkg/provider/issue.Provider's Show/Create methods.
type Issue struct {
	// ID is the issue's identity, a string per pg-pr's existing
	// api.Issue.ID convention (backends may use anything from a numeric
	// GitHub Issue number to a Jira key to a beads id).
	ID    string `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
	URL   string `json:"url"`

	// Priority is the issue's priority string exactly as ITS OWN tracker
	// represents it — e.g. bd's "P0".."P4", not a fixed cross-backend
	// "High"/"Medium"/"Low" scale (an earlier revision of this comment
	// advertised that scale as if it were universal; bd itself rejects
	// those values, per review finding A-33). Each backend renders its own tracker-native form and
	// declares the values it actually accepts via its capabilities
	// response's vocabulary.priority (see e.g. issue-beads'
	// PriorityVocabulary) — this field does not pin one scale for every
	// backend. Empty when the backend does not supply one — carried over
	// from api.Issue.Priority.
	Priority string `json:"priority,omitempty"`

	// Labels is the list of label strings attached to the issue. Empty
	// when the backend does not supply them or the issue has none —
	// carried over from api.Issue.Labels.
	Labels []string `json:"labels,omitempty"`

	// IssueType is the issue-type string as returned by the tracker (e.g.
	// "Bug", "Story", "Incident"). Empty when the backend does not supply
	// one — carried over from api.Issue.IssueType.
	IssueType string `json:"issue_type,omitempty"`

	// Tracker identifies which backend-specific tracker/workspace actually
	// answered this request (e.g. a bd workspace directory, a Jira base
	// URL, a GitHub host) — added by bead pg2-1q9c0 (design review finding
	// A9) so a caller can verify which tracker was hit rather than having
	// to assume it matches whatever it intended. Empty when a backend does
	// not (yet) supply one; a backend with only one possible tracker (no
	// ambient-selection hazard) MAY leave this empty.
	Tracker string `json:"tracker,omitempty"`

	// Description is the issue's free-text description/body. Empty when
	// the backend does not supply one, or the issue has none — added by
	// bead pg2-akfw5 (review finding A-33: Show previously dropped this entirely).
	Description string `json:"description,omitempty"`

	// Assignee is the issue's current assignee, in whatever identity form
	// its own tracker uses (a bd actor string, a Jira/GitHub login, ...).
	// Empty when unassigned or the backend does not supply one — added by
	// bead pg2-akfw5 (review finding A-33).
	Assignee string `json:"assignee,omitempty"`

	// Parent is the id of this issue's parent in a hierarchical tracker
	// (e.g. one of bd's dot-suffixed child ids). Empty when the issue has
	// no parent or the backend does not supply one — added by bead
	// pg2-akfw5 (review finding A-33).
	Parent string `json:"parent,omitempty"`

	// Deps lists this issue's own dependency edges — both hierarchical
	// parent-child links and typed edges (e.g. bd's "blocks",
	// "discovered-from") — in whatever the tracker itself calls the
	// relationship. Empty when the backend does not supply this or the
	// issue has none — added by bead pg2-akfw5 (review finding A-33).
	//
	// This is a ONE-LEVEL, ALL-edge-type field (bd's own `dependencies`
	// array on a single `show` response) — distinct from
	// issue.Provider's own Deps method/IssueDepsResult below, which walks
	// the RECURSIVE, blocks-only chain. Do not conflate the two.
	Deps []IssueDependency `json:"deps,omitempty"`

	// AsOf is this read's own as-of time (RFC3339, UTC) — added by bead
	// pg2-2j5ac.28.3, mirroring the pr capability's own AsOf/Stale pair and
	// semantics exactly (bead pg2-681xo's INV-ASOF-1 contract: "every
	// acted-on read seam MUST carry its own as-of time ... an item or
	// payload with no usable as-of time MUST be reported stale"). Empty
	// only when a backend has no usable as-of time for this read, which
	// MUST pair with Stale true rather than a plausible-looking but
	// meaningless timestamp.
	//
	// This is a fact about a successful read's own payload, deliberately
	// kept separate from pg-connector's outcome/error taxonomy (the CLI
	// exit-code scheme and the wire Error.Code enum both classify whether
	// a CALL succeeded; AsOf/Stale classify whether a successful call's
	// DATA is current) — a targeted `show` that returns stale data is
	// still exit 0, never folded into a sixth error/exit code.
	AsOf string `json:"as_of"`
	// Stale is this backend's own as-of/stale determination for this read
	// (INV-ASOF-2: the backend that answers a read is the sole computer of
	// its own staleness; a consumer MUST NOT re-derive one from AsOf
	// itself). Always populated (not omitempty), matching Issue's other
	// plain boolean facts — false is itself informative.
	//
	// Both of this capability's current backends (issue-beads,
	// issue-jira) perform a live read on every call with no local cache of
	// tracker facts, so both always report Stale false with AsOf set to
	// that live call's own completion time — mirroring
	// schema.PR.Stale's own doc comment, which noted this was true of
	// every ci/issue/scm backend as of bead pg2-681xo.
	Stale bool `json:"stale"`

	// UpdatedAt is the issue's own last-modified time, in whatever
	// timestamp form its tracker returns (bd's own `updated_at` is
	// RFC3339). Empty when the backend does not supply one.
	UpdatedAt string `json:"updated_at,omitempty"`

	// DueDate is the issue's due date/time, in whatever form its tracker
	// returns (bd's own `--due`/`due_at`; the daily-focus v2 design depends
	// on Jira's native duedate field too, per this bead's own Contract).
	// Empty when unset or the backend does not supply one.
	DueDate string `json:"due_date,omitempty"`

	// Metadata is a string-to-string map of backend-specific custom fields
	// — deliberately typed map[string]string on the wire (binding
	// decision: metadata is string-to-string on the wire), never a
	// typed/nested value: a backend whose own native field is typed (e.g.
	// bd's own metadata values, which may decode as a JSON number or
	// boolean rather than a string) coerces it to its decimal/canonical
	// string form on the way out. Empty/nil when the backend has no
	// metadata to report.
	Metadata map[string]string `json:"metadata,omitempty"`

	// ExternalRefs lists ids the tracker exposes pointing at other systems
	// (e.g. bd's own single `external_ref` field, a GitHub/Jira
	// cross-link). Empty when the backend has none to report — a plural
	// wire shape even though today's beads backend only ever supplies zero
	// or one (bd's own external_ref field is singular).
	ExternalRefs []string `json:"external_refs,omitempty"`
}

// IssueDependency is one edge in Issue.Deps: another issue's id, plus this
// tracker's own name for the relationship (e.g. bd's "parent-child" or
// "blocks" `dependency_type`) — added by bead pg2-akfw5 (review finding A-33).
type IssueDependency struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// IssueListResult is the "list" op's wire result payload for the issue
// capability — the same generic shape PRListResult (pr.go) documents in
// full; see its doc comment for Cursor/Entities/PresentIDs/ids_only
// semantics, which apply identically here with Issue in place of PR
// (bead pg2-2j5ac.28.1).
type IssueListResult struct {
	Entities   []Issue  `json:"entities"`
	PresentIDs []string `json:"present_ids"`
	Cursor     *string  `json:"cursor"`
	Truncated  bool     `json:"truncated"`
}

// IssueDepsResult is the "deps" op's wire result payload for the issue
// capability (bead pg2-2j5ac.28.3): the recursive UPWARD (transitively
// blocked-by) dependency set — what "waiting-on-me" tooling needs, and
// distinct from Issue.Deps' own one-level, all-edge-type field above (see
// its doc comment).
//
// IDs is always populated with the full recursive id set (never omitted,
// even when full is false). Entities is populated only when the caller
// passed full=true, each entry the same Issue shape "show" returns
// (INV-VER-1 — no separate schema for this). A backend with no dependency
// concept of its own answers an empty IDs/Entities, never an error.
type IssueDepsResult struct {
	IDs      []string `json:"ids"`
	Entities []Issue  `json:"entities,omitempty"`
}
