// pr.go: the pr entity/capability's shared JSON wire shape, built by the
// "generic pr entity/capability" packet on top of the Tier-1 core's
// pkg/schema placeholder (see doc.go).
//
// The field set is carried over from this repo's existing
// packages/pg-pr/pkg/api.PR/api.Comment/api.Review types, per the design's
// explicit carry-over decision that pg-connector's PR backend "carries over
// pg-pr's existing GitHub logic unchanged, reading and writing the same
// underlying GitHub state pg-pr always has". It is
// deliberately a SMALLER field set than pg-pr's internal api.PR: this is the
// pr capability's generic wire contract (what a `pr show` caller needs —
// identity and review/feedback state), never pg-pr's own
// sync/dashboard-ingestion shape. Fields specific to that internal use
// (StackID, MergedAt, enrichment/co-ownership bookkeeping, …) are out of
// scope here [freedom boundary]. The category/disposition write fields
// this comment used to describe were removed by bead pg2-2j5ac.28.7
// (categorize/feedback_set retired — re-derived by pg-desk instead of
// migrated/persisted here).
package schema

// PRSchemaVersion is the pr capability's own schema version, populated into
// the wire envelope's schemaVersion field by each of the pr capability's
// dispatch-table entries (pkg/provider/pr.NewDispatchTable) — independent
// of pkg/scriptout.ProtocolVersion (INV-VER-1). Named PRSchemaVersion
// (not a bare SchemaVersion) to match the other three entities'
// <Entity>SchemaVersion convention (CISchemaVersion, IssueSchemaVersion,
// ScmSchemaVersion) — see scm.go's doc comment for why a bare
// SchemaVersion in this shared package was inconsistent [finding A29].
//
// Bumped 1 -> 2 by bead pg2-681xo, which added the AsOf/Stale fields below.
// schemaVersion versions a capability's own field shape, full stop —
// IssueSchemaVersion's own 1 -> 2 bump (bead pg2-1q9c0, for adding the
// Tracker field) already established this repo's precedent that ANY
// field-shape change bumps the version, additive or not, so a
// version_mismatch check can catch build skew on an additive change too,
// not only a breaking one. This is the first field-shape change to
// schema.PR since the capability's initial version, and it is made now —
// before any additional PR consumer (df-categorize, df-feedback, more
// Tier-3 tools) lands against the pre-freshness shape — precisely to avoid
// an even more expensive schema bump a later addition would otherwise
// force.
//
// Bumped 2 -> 3 by bead pg2-2j5ac.28.1, which added the
// "list" op and its PRListResult wire shape below — schemaVersion is one
// integer per schema-bearing CAPABILITY (INV-VER-1), not one per Go
// struct, so a capability gaining a whole new op's wire shape is exactly
// the same kind of change the 1 -> 2 bump's precedent already covers.
//
// Bumped 3 -> 4 by bead pg2-2j5ac.28.2, which added PR's dashboard-facing
// fact fields (HeadSHA, Additions, Deletions, ChangedFiles, Mergeable,
// MergeStateStatus, ReviewRequests, ChecksRollup) plus the "files"/
// "commits" ops and their PRFilesResult/PRCommitsResult wire shapes below.
// The design source this packet curated from still describes this as a
// "2 -> 3" bump (it was written before pg2-2j5ac.28.1's own 2 -> 3 bump for
// "list" landed) — 3 was already spent by that earlier packet, so this is
// the next integer per this const's own one-bump-per-field-shape-change
// precedent (see the 1 -> 2 / 2 -> 3 comments above), not a deviation from
// it.
const PRSchemaVersion = 4

// PR is the pr capability's shared JSON wire shape, returned by the pr
// capability's "show" op and carried by pkg/provider/pr.Provider.Show
// (interfaces.md's pr op catalog).
type PR struct {
	// ID is the PR's identity, carried over as-is from pg-pr's existing
	// api.Comment.ID string convention (a string, not a numeric id).
	ID     string   `json:"id"`
	Repo   string   `json:"repo"`
	Number int      `json:"number"`
	Title  string   `json:"title"`
	State  string   `json:"state"`
	Branch string   `json:"branch"`
	Base   string   `json:"base"`
	Author string   `json:"author"`
	URL    string   `json:"url"`
	Draft  bool     `json:"draft"`
	Merged bool     `json:"merged"`
	Body   string   `json:"body,omitempty"`
	Labels []string `json:"labels,omitempty"`

	// AsOf is this read's own as-of time (RFC3339, UTC) — added by bead
	// pg2-681xo, mirroring the pair pg-pr's own read seams already publish
	// under packages/pg-pr/docs/behavior/invariants.md's INV-ASOF-1
	// ("every acted-on read seam MUST carry its own as-of time ... an item
	// or payload with no usable as-of time MUST be reported stale").
	// Empty only when a backend has no usable as-of time for this read,
	// which MUST pair with Stale true rather than a plausible-looking but
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
	// itself). Always populated (not omitempty), matching PR's other plain
	// boolean facts (Draft, Merged) — false is itself informative.
	//
	// The current GitHub backend (cmd/pg-connector-pr-github) always
	// performs a live GitHub read for every PR fact on every Show call —
	// its own local Store holds only this backend's category/disposition
	// writes, never a cached copy of GitHub's PR facts — so it always
	// reports Stale false with AsOf set to that live read's own call time.
	// A future backend that DOES serve PR facts from a local cache/store is
	// the one expected to ever report Stale true. The ci/issue/scm
	// sibling schemas do not carry this pair today: none of their current
	// backends caches the remote fact data they return either (investigated
	// at the time this field was added), so there is no live-called-vs-cached
	// distinction yet to expose for them — a future backend that adds such a
	// cache for any of them should adopt this same AsOf/Stale pair rather
	// than inventing a parallel convention.
	Stale bool `json:"stale"`

	// Comments are the PR's own top-level (non-review-thread) comments.
	Comments []PRComment `json:"comments,omitempty"`
	// Reviews are the PR's review summaries, each carrying its own
	// review-thread comments (see PRReview.Comments).
	Reviews []PRReview `json:"reviews,omitempty"`

	// The fields below (bead pg2-2j5ac.28.2) carry facts pg-desk/the
	// dashboard needs on every "show" so it never has to fan out to a
	// second `ci` call per PR per tick — bead pg2-2j5ac.28.2's own PR-facts
	// design bullet. All are additive; every existing PRSchemaVersion-3
	// consumer keeps decoding unchanged.

	// HeadSHA is the OID of the PR's current head commit.
	HeadSHA string `json:"head_sha,omitempty"`
	// Additions/Deletions/ChangedFiles are the PR's diff-size facts, as
	// GitHub reports them on the PR itself (not summed here from Files,
	// which a caller fetches separately via the "files" op).
	Additions    int `json:"additions,omitempty"`
	Deletions    int `json:"deletions,omitempty"`
	ChangedFiles int `json:"changed_files,omitempty"`
	// Mergeable is GitHub's merge-conflict signal: one of "MERGEABLE",
	// "CONFLICTING", "UNKNOWN" (GitHub's own enum, carried through
	// verbatim — a freedom-boundary choice: the design pins the field's
	// existence and semantics, not a Go representation narrower than
	// GitHub's own tri-state).
	Mergeable string `json:"mergeable,omitempty"`
	// MergeStateStatus is GitHub's authoritative merge-readiness signal
	// (branch protection, required checks, review policy folded together):
	// one of "CLEAN", "BLOCKED", "BEHIND", "DIRTY", "UNSTABLE", "DRAFT",
	// "HAS_HOOKS", "UNKNOWN" — GitHub's own enum, carried through verbatim.
	MergeStateStatus string `json:"merge_state_status,omitempty"`
	// ReviewRequests are the PR's currently-requested reviewers — both
	// individual account logins and team slugs (design: "logins and team
	// slugs"), unlike PRComment/PRReview's Author, which is always an
	// individual account.
	ReviewRequests []string `json:"review_requests,omitempty"`
	// ChecksRollup folds the PR's head-commit check-run/status data into
	// one of "success", "failure", "pending", "none" — carried here so the
	// dashboard does not fan out to the `ci` capability per PR per tick.
	// How a backend computes this from GitHub's underlying check-run data
	// is a freedom-boundary choice; the design pins only this closed value
	// set.
	ChecksRollup string `json:"checks_rollup,omitempty"`
}

// PRComment is one PR-level or review-thread comment/finding. Both ID (on
// PR itself, above) and CommentID are strings, carried over as-is from
// pg-pr's existing api.Comment.ID string field.
type PRComment struct {
	ID       string `json:"id"`
	Author   string `json:"author"`
	Body     string `json:"body"`
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
	ThreadID string `json:"thread_id,omitempty"`
	Resolved bool   `json:"resolved"`
}

// PRReview is one PR review summary. Its own inline/review-thread comments
// are carried as Comments, each with its own id and current disposition
// per PRComment above — a `pr show` response must return them so a caller
// can re-evaluate every comment on the PR from its current state
// (interfaces.md's pr op catalog).
type PRReview struct {
	ID       string      `json:"id"`
	Author   string      `json:"author"`
	State    string      `json:"state"`
	Body     string      `json:"body,omitempty"`
	Comments []PRComment `json:"comments,omitempty"`
}

// PRListResult is the "list" op's wire result payload (bead pg2-2j5ac.28.1,
// design's exact response shape: `{"entities": [], "present_ids":
// [], "cursor": null, "truncated": false}`).
//
// Cursor is *string (not a bare string) so the wire encoding always
// explicitly emits "cursor": null rather than omitting the field —
// design's own binding decision states cursor "MUST always be null in
// this packet" (incremental fetching is changes, phase 8), so this type
// never actually sets it to a non-nil value today, but the pointer form
// keeps the wire shape self-documenting either way.
//
// Entities carries the FULL matched PR set (each entry the same PR shape
// "show" returns) unless a caller passed ids_only, in which case a
// backend leaves Entities empty and populates only PresentIDs — a
// freedom-boundary choice: the design pins the response SHAPE, not
// whether every backend eagerly fetches full detail (Comments/Reviews) for
// every matched entity. This backend's own List MAY leave
// Comments/Reviews unpopulated even when Entities is populated: "list" is
// an enumeration/matching op, not a per-entity full-detail read — a caller
// wanting full detail for one matched PR calls "show" on its id.
//
// PresentIDs MUST always be the complete id set the query matches right
// now — populated regardless of ids_only, since ids_only
// only controls whether Entities is ALSO populated.
type PRListResult struct {
	Entities   []PR     `json:"entities"`
	PresentIDs []string `json:"present_ids"`
	Cursor     *string  `json:"cursor"`
	Truncated  bool     `json:"truncated"`
}

// PRFile is one changed file entry in a PR's diff, an element of the
// "files" op's wire result (bead pg2-2j5ac.28.2's PR-facts design bullet).
// A targeted op (resolves to the one backend that owns the given PR id),
// matching show/categorize/feedback_set's existing convention — NOT the
// fan-out scheme list/ci list use.
type PRFile struct {
	Path      string `json:"path"`
	Additions int    `json:"additions,omitempty"`
	Deletions int    `json:"deletions,omitempty"`
}

// PRFilesResult is the "files" op's wire result payload.
type PRFilesResult struct {
	ID    string   `json:"id"`
	Files []PRFile `json:"files"`
}

// PRCommit is one commit on a PR's branch, an element of the "commits" op's
// wire result. Author MUST carry the commit's own GitHub author login
// (bead pg2-2j5ac.28.2's PR-facts design bullet, closing sentence —
// "commits MUST carry each commit's own author login"), the co-owned-
// ownership classification's
// consumer; empty only when GitHub has no linked user account for the
// commit's author identity (e.g. an email with no matching GitHub account).
type PRCommit struct {
	SHA     string `json:"sha"`
	Author  string `json:"author"`
	Message string `json:"message,omitempty"`
}

// PRCommitsResult is the "commits" op's wire result payload. Like "files",
// a targeted op (see PRFile's doc comment).
type PRCommitsResult struct {
	ID      string     `json:"id"`
	Commits []PRCommit `json:"commits"`
}
