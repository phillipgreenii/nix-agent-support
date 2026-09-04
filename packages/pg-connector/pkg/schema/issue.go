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
const IssueSchemaVersion = 1

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
}
