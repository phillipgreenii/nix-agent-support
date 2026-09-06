// pr.go: the pr entity/capability's shared JSON wire shape, built by the
// "generic pr entity/capability" packet on top of the Tier-1 core's
// pkg/schema placeholder (see doc.go).
//
// The field set is carried over from this repo's existing
// packages/pg-pr/pkg/api.PR/api.Comment/api.Review types, per the design's
// explicit carry-over decision that pg-connector's PR backend "carries over
// pg-pr's existing GitHub logic unchanged, reading and writing the same
// underlying GitHub state pg-pr always has" [design: §9, §5.3]. It is
// deliberately a SMALLER field set than pg-pr's internal api.PR: this is the
// pr capability's generic wire contract (what a `pr show` caller needs —
// identity, review/feedback state, and the two dedicated write fields),
// never pg-pr's own sync/dashboard-ingestion shape. Fields specific to that
// internal use (StackID, MergedAt, enrichment/co-ownership bookkeeping, …)
// are out of scope here [freedom boundary, §4].
package schema

// SchemaVersion is the pr capability's own schema version, populated into
// the wire envelope's schemaVersion field by each of the pr capability's
// dispatch-table entries (pkg/provider/pr.NewDispatchTable) — independent
// of pkg/scriptout.ProtocolVersion [design: §4.2 — "that classification
// lives with the capability (which owns its schema)"].
//
// Bumped 1 -> 2 by bead pg2-681xo, which added the AsOf/Stale fields below.
// Per §4.3, schemaVersion "versions that capability's own field shape" —
// IssueSchemaVersion's own 1 -> 2 bump (bead pg2-1q9c0, for adding the
// Tracker field) already established this repo's precedent that ANY
// field-shape change bumps the version, additive or not, so a
// version_mismatch check can catch build skew on an additive change too,
// not only a breaking one. This is the first field-shape change to
// schema.PR since the capability's initial version, and it is made now —
// before any additional PR consumer (df-categorize, df-feedback, more
// Tier-3 tools) lands against the pre-freshness shape — precisely to avoid
// the "expensive schema bump" scenario a later addition would otherwise
// force.
const SchemaVersion = 2

// PR is the pr capability's shared JSON wire shape, returned by the pr
// capability's "show" op and carried by pkg/provider/pr.Provider.Show
// [design: §5.2, §6.1].
type PR struct {
	// ID is the PR's identity, carried over as-is from pg-pr's existing
	// api.Comment.ID string convention (a string, not a numeric id)
	// [design: §9, §5.3].
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

	// Category is a single-valued, backend-declared-vocabulary string
	// field, written only via the dedicated categorize op — never a GitHub
	// label [design: §6.1, §4.3].
	Category string `json:"category,omitempty"`

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
}

// PRComment is one PR-level or review-thread comment/finding. Both ID (on
// PR itself, above) and CommentID (used by feedback_set) are strings,
// carried over as-is from pg-pr's existing api.Comment.ID string field
// [design: §9, §5.3]. Disposition is the closed enum a caller re-evaluates
// via `pr show` and writes back via feedback_set [design: §2, §6.1].
type PRComment struct {
	ID          string      `json:"id"`
	Author      string      `json:"author"`
	Body        string      `json:"body"`
	Path        string      `json:"path,omitempty"`
	Line        int         `json:"line,omitempty"`
	ThreadID    string      `json:"thread_id,omitempty"`
	Resolved    bool        `json:"resolved"`
	Disposition Disposition `json:"disposition,omitempty"`
}

// PRReview is one PR review summary. Its own inline/review-thread comments
// are carried as Comments, each with its own id and current disposition
// per PRComment above — a `pr show` response must return them so a caller
// can re-evaluate every comment on the PR from its current state
// [design: §2, §6.1].
type PRReview struct {
	ID       string      `json:"id"`
	Author   string      `json:"author"`
	State    string      `json:"state"`
	Body     string      `json:"body,omitempty"`
	Comments []PRComment `json:"comments,omitempty"`
}

// Disposition is the closed enum a PR comment/review-thread entry's current
// disposition is drawn from, and the value feedback_set writes
// [design: §2, §6.1].
type Disposition string

const (
	DispositionOpen     Disposition = "open"
	DispositionWillFix  Disposition = "will-fix"
	DispositionWontFix  Disposition = "wont-fix"
	DispositionNoAction Disposition = "no-action"
)

// ValidDispositions is the closed set above, in a stable order — used for
// validation and CLI help/error text.
var ValidDispositions = []Disposition{DispositionOpen, DispositionWillFix, DispositionWontFix, DispositionNoAction}

// IsValid reports whether d is one of ValidDispositions.
func (d Disposition) IsValid() bool {
	for _, v := range ValidDispositions {
		if d == v {
			return true
		}
	}
	return false
}

// CategorizeResult is the categorize op's wire result payload: a plain
// set/overwrite acknowledgement, no add/remove/toggle ambiguity since
// category is a dedicated field rather than a member of a shared label
// namespace [design: §6.1].
type CategorizeResult struct {
	ID       string `json:"id"`
	Category string `json:"category"`
}

// FeedbackSetResult is the feedback_set op's wire result payload
// [design: §6.1].
type FeedbackSetResult struct {
	ID          string      `json:"id"`
	CommentID   string      `json:"comment_id"`
	Disposition Disposition `json:"disposition"`
}
