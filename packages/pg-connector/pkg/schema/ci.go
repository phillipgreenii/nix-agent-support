// ci.go: the ci entity/capability's shared JSON wire shape, built by the
// "generic ci entity/capability" packet on top of the Tier-1 core's
// pkg/schema placeholder (see doc.go). connector.ci is list-valued
// (multiple simultaneously-registered CI backends, matching pr/issue)
// (INV-REG-1).
//
// The field set is carried over from this repo's existing
// packages/pg-pr/pkg/api.CIRun type, per this repo's general carry-over
// convention for pg-connector's schemas — with one
// addition: PRID. The design defines CI as "a build/run, linked to a PR," and
// today's api.CIRun has no explicit PR-linkage field of its own (the link
// is implicit in caller context, e.g. ListRuns(ctx, repo, prNumber)'s own
// arguments) — PRID makes a CIRun value self-describing (interfaces.md's op catalog).
// api.CIRun's Description field is deliberately not carried over here: this
// packet's contract names exactly ID, Name, Status, Conclusion, URL,
// Provider, HeadSHA, and PRID as CIRun's field set.
package schema

// CISchemaVersion is the ci capability's own schema version, populated into
// the wire envelope's schemaVersion field by each of the ci capability's
// dispatch-table entries (pkg/provider/ci.NewDispatchTable) — independent
// of both pkg/scriptout.ProtocolVersion and the pr capability's own
// SchemaVersion: schemaVersion is one integer per schema-bearing capability,
// never a single global counter shared across capabilities
// (INV-VER-1).
const CISchemaVersion = 1

// CIRun is the ci capability's shared JSON wire shape, returned by the ci
// capability's "list_runs" op and carried by
// pkg/provider/ci.Provider.ListRuns (interfaces.md's ci op catalog).
type CIRun struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
	Provider   string `json:"provider"`

	// HeadSHA is the commit SHA the run was triggered against, carried over
	// as-is from packages/pg-pr/pkg/api.CIRun.HeadSHA.
	HeadSHA string `json:"head_sha,omitempty"`

	// PRID links this run to the PR it belongs to (interfaces.md's op catalog) — see this
	// file's header comment for why it was added on top of api.CIRun's
	// existing field set. Not omitempty: PRID is CIRun's own
	// identity-linkage field, always populated by a well-behaved provider,
	// mirroring PR.ID's own non-omitempty convention in pr.go.
	PRID string `json:"pr_id"`
}
