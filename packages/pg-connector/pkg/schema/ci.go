// ci.go: the ci entity/capability's shared JSON wire shape, built by the
// "generic ci entity/capability" packet on top of the Tier-1 core's
// pkg/schema placeholder (see doc.go). connector.ci is list-valued
// (multiple simultaneously-registered CI backends, matching pr/issue)
// (INV-REG-1).
//
// The field set began as this repo's existing
// packages/pg-pr/pkg/api.CIRun type, per this repo's general carry-over
// convention for pg-connector's schemas, and has since grown by additions
// (PRID, Repo, AsOf, Stale, Attempt; see each field's own doc comment).
// api.CIRun's Description field is deliberately not carried over here.
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
//
// Bumped 3 -> 4 by bead pg2-2j5ac.52.6.3, which added the Attempt field
// below (additive, omitempty; one bump per field-shape change, as with
// PRSchemaVersion).
//
// Bumped 4 -> 5 by bead pg2-gllcn, which added the Jobs field (and the
// CIJob type) below: per-job results attributed to their run (additive,
// omitempty; existing run-level fields are untouched). Note CIRun is no
// longer comparable with == (it now holds a slice); compare with
// reflect.DeepEqual.
const CISchemaVersion = 5

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

	// Attempt is the run's GitHub Actions attempt number. GitHub keeps a
	// run's databaseId (ID) when it is re-run and increments the attempt
	// instead, so (ID, Attempt) identifies one build. 0 (omitted) when the
	// backend reports no attempt; it is never synthesized. gh reports only
	// each run's latest attempt.
	Attempt int `json:"attempt,omitempty"`

	// Jobs lists this run's per-job results, added by CISchemaVersion's
	// 4 -> 5 bump (bead pg2-gllcn) so a consumer can tell WHICH job of a
	// failed run failed (e.g. pg-desk's pg2-p2ojd: a PR whose only failing
	// job is build-test-validate is still reviewable). Additive and
	// optional: omitted (nil) means "not fetched", never "the run has no
	// jobs" — consumers that ignore the field are unaffected, and every
	// run-level field above is unchanged.
	//
	// Jobs are fetched with one extra per-run API call, so a backend MUST
	// bound that cost; pg-connector-ci-github-actions fetches them only for
	// non-successful, completed runs on the PR's current head SHA (capped
	// per list call), and a failed job fetch leaves Jobs omitted rather
	// than failing the run listing. An all-success PR therefore triggers
	// no job fetches at all.
	Jobs []CIJob `json:"jobs,omitempty"`

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
	// briefly added a real cache for this purpose (bead pg2-4aoeg) but bead
	// pg2-2j5ac.28.7 deleted it outright (statelessness, D3) — this
	// backend's ListRuns always reports Stale false again, so a live
	// answer never claims staleness it did not observe. Real
	// stale-fallback for every ci-capable backend now exists (phase 14,
	// bead pg2-2j5ac.42.3): cmd/pg-connector's own fanOutCIList serves a
	// backend answering unavailable from the umbrella's on-disk entity
	// cache instead, overwriting Stale true and AsOf to the cached as-of
	// time on the runs it returns — D3 stands (no backend gained a store;
	// only the umbrella did).
	Stale bool `json:"stale"`
}

// CIJob is one job of a CIRun, carried by CIRun.Jobs. A job belongs to
// exactly the CIRun that holds it (attribution is by containment, so there
// is no run-id back-reference field).
type CIJob struct {
	// ID is the backend's own job id (GitHub Actions: the job's databaseId,
	// rendered as a decimal string like CIRun.ID). Omitted when the backend
	// reports none.
	ID string `json:"id,omitempty"`
	// Name is the job's display name (e.g. "build-test-validate").
	Name string `json:"name"`
	// Status is the job's lifecycle state, lower-cased (queued,
	// in_progress, completed), matching CIRun.Status's convention.
	Status string `json:"status"`
	// Conclusion is the job's outcome, lower-cased (success, failure,
	// cancelled, skipped, ...); empty until the job completes, matching
	// CIRun.Conclusion's convention.
	Conclusion string `json:"conclusion"`
	// URL links to the job in the backend's UI when the backend has one
	// (omitted otherwise); a build-link consumer (pg2-gnu5v) MAY reuse it.
	URL string `json:"url,omitempty"`
}
