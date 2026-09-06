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
// SchemaVersion (each entity type/capability versions its own schema
// separately) and of pkg/scriptout.ProtocolVersion.
//
// Bumped 1 -> 2 by bead pg2-1q9c0 (design review finding A9), which added
// the Tracker field below — per §4.3, schemaVersion "versions that
// capability's own field shape," and this is the first field-shape change
// since the capability's initial version. No existing consumer exists yet
// (the issue capability has one Tier-2 backend, pg-connector-issue-beads;
// Phase 2's issue-jira has not landed), so the bump has no live compat
// impact today — it is here so a future consumer's version_mismatch
// handling is exercised against a real precedent rather than a hypothetical
// one.
const IssueSchemaVersion = 2

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

	// Priority is the issue's priority string as returned by the tracker
	// (e.g. "High", "Medium", "Low"). Empty when the backend does not
	// supply one — carried over from api.Issue.Priority.
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
}
