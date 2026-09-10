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
// PRSchemaVersion: schemaVersion is one integer per schema-bearing capability,
// never a single global counter shared across capabilities
// (INV-VER-1).
//
// Bumped 1 -> 2 by bead pg2-4aoeg, which added the AsOf/Stale fields below,
// mirroring the pr capability's own 1 -> 2 bump for the identical pair
// (bead pg2-681xo). schemaVersion versions a capability's own field shape,
// full stop — pg2-681xo and IssueSchemaVersion's own 1 -> 2 bump (bead
// pg2-1q9c0, for adding the Tracker field) already established this
// repo's precedent that ANY field-shape change bumps the version,
// additive or not.
//
// Bumped 2 -> 3 by bead pg2-2j5ac.28.4, which added the Repo field below
// and, in lockstep, widened ci.Provider.GetLogs to take repo as a
// caller-supplied argument — reversing the 2026-09-06 operator ruling on
// pg2-f327j that had kept GetLogs id-only and resolved repo internally via
// a backend-local correlation store
// (pg-connector-ci-github-actions/internal/run_store.go, left in place for
// the removals packet blocked-by this one).
const CISchemaVersion = 3

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

	// Repo is this run's owning repo (owner/name form), added by
	// CISchemaVersion's 2 -> 3 bump so a caller already holding a CIRun (e.g.
	// from a prior list_runs/ListRuns response) can supply it straight to
	// ci.Provider.GetLogs — the caller-supplied-repo replacement for the
	// backend-local correlation store GetLogs used to consult. Not
	// omitempty: like PRID below, a well-behaved provider always populates
	// it for a run it returns.
	Repo string `json:"repo"`

	// PRID links this run to the PR it belongs to (interfaces.md's op catalog) — see this
	// file's header comment for why it was added on top of api.CIRun's
	// existing field set. Not omitempty: PRID is CIRun's own
	// identity-linkage field, always populated by a well-behaved provider,
	// mirroring PR.ID's own non-omitempty convention in pr.go.
	PRID string `json:"pr_id"`

	// AsOf is this read's own as-of time (RFC3339, UTC) — added by bead
	// pg2-4aoeg, extending the pr capability's own AsOf/Stale contract
	// (schema.PR.AsOf, bead pg2-681xo — itself following
	// packages/pg-pr/docs/behavior/invariants.md's INV-ASOF-1: "every
	// acted-on read seam MUST carry its own as-of time ... an item or
	// payload with no usable as-of time MUST be reported stale") onto the
	// ci capability. Empty only when a backend has no usable as-of time for
	// this run, which MUST pair with Stale true rather than a
	// plausible-looking but meaningless timestamp.
	//
	// This is a fact about a successful read's own payload, deliberately
	// kept separate from pg-connector's outcome/error taxonomy (the CLI
	// exit-code scheme and the wire Error.Code enum both classify whether a
	// CALL succeeded; AsOf/Stale classify whether a successful call's DATA
	// is current) — a `list_runs` call that returns stale runs is still
	// exit 0, never folded into a sixth error/exit code.
	// cmd/pg-connector/outcome.go's own healthy/degraded/failed exit-code
	// axis is the separate, whole-invocation concern of "how many
	// registered backends answered" and is unaffected by any one run's own
	// AsOf/Stale pair.
	AsOf string `json:"as_of"`
	// Stale is this backend's own as-of/stale determination for this run
	// (INV-ASOF-2: the backend that answers a read is the sole computer of
	// its own staleness; a consumer MUST NOT re-derive one from AsOf
	// itself). Always populated (not omitempty), matching CIRun's other
	// plain-value facts — false is itself informative.
	//
	// A ci backend with no local cache of the upstream CI system's run data
	// always reports Stale false with AsOf set to that live call's own
	// completion time — pg2-681xo's own doc comment on schema.PR.Stale
	// noted this was true of every ci/issue/scm backend as of that bead.
	// pg-connector-ci-github-actions (cmd/pg-connector-ci-github-actions)
	// is the first ci backend to add a real cache for this purpose (its own
	// RunListCache, run_list_cache.go): its ListRuns reports Stale true only
	// when GitHub Actions itself was degraded/unreachable for a live call it
	// would otherwise have made, and it served this PR's last-known-good
	// cached run list instead of erroring outright — AsOf in that case is
	// the CACHED read's own original as-of time, never the moment of the
	// failed live attempt.
	Stale bool `json:"stale"`
}
