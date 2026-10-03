// Package pr declares the pr capability's provider interface — a small,
// capability-scoped Go interface (never named after a backend/system)
// (INV-CAP-1) that a Tier-2 PR backend's concrete provider implements. It
// matches this repo's existing small-per-capability-interface convention
// (e.g. vcs.Provider in packages/pg-pr/pkg/provider/vcs) rather than one
// interface spanning multiple systems (INV-CAP-1).
//
// This package sits alongside pkg/schema and pkg/scriptout as part of the
// module's shared surface importable across backend boundaries — see
// cmd/pg-connector's layout-convention check.
package pr

import (
	"context"
	"encoding/json"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Provider is the pr capability's provider interface: a Show-style read
// plus the list/files/commits ops this docket names (naming_convention_test.go;
// interfaces.md's pr op catalog). The categorize/feedback_set write ops
// this interface used to also carry were retired by bead pg2-2j5ac.28.7:
// category/disposition are re-derived by pg-desk's interpreter rather than
// persisted by any backend (statelessness, D3). A concrete backend MAY
// additionally implement pkg/provider.AuthChecker, asserted via a
// type-check rather than folded into this interface (INV-AUTH-1) — see
// NewDispatchTable in dispatch.go.
type Provider interface {
	// Show returns id's current full state, including comments/
	// review-thread entries each with their own id (interfaces.md's pr op
	// catalog). The returned schema.PR MUST carry its
	// own AsOf/Stale pair (bead pg2-681xo): AsOf is this read's own as-of
	// time, and Stale is this Provider's own as-of/stale determination —
	// true only when this read served (rather than freshly fetched) a
	// cached copy of the underlying PR facts that has aged past this
	// Provider's own staleness bound. A Provider with no such cache (every
	// current implementation) always returns Stale false with AsOf set to
	// the read's own call time.
	Show(ctx context.Context, id string) (*schema.PR, error)

	// List runs query (already resolved from the request's own
	// config.queries block — dispatch.go's "list" handler does that
	// resolution centrally, reporting query_not_recognized itself before
	// ever calling List, so a concrete Provider need not repeat that check)
	// and returns the matching PRs. query MAY carry more than one
	// expression (design: "run each, union results deduplicated by
	// id, report truncated if any member truncated") — List, not the
	// dispatch table, does that fan-in. idsOnly, when true, means the
	// caller only wants PRListResult.PresentIDs populated; a Provider MAY
	// still choose to populate Entities anyway (harmless, just wasted
	// work) but need not. bead pg2-2j5ac.28.1.
	//
	// cursor is the incoming page/incremental-fetch token, opaque to
	// everything except the Provider that produced it (design: section 4.2
	// — a per-backend-opaque JSON blob, e.g. a Jira cursor carrying an
	// "updated >= " bound, never a bare string) — nil on a first/full
	// fetch. dispatch.go's "list" handler decodes it off the wire and
	// passes it through UNVALIDATED: a Provider that does not implement
	// incremental listing MUST simply ignore whatever it is handed and
	// MUST always answer with PRListResult.Cursor nil (design: "A backend
	// that does not support incremental listing MUST return null and MUST
	// ignore any cursor it is handed"). The human-facing CLI verb
	// `pg-connector pr list` MUST NEVER pass a non-nil cursor on this verb
	// (design: "It MUST NOT pass a cursor on this verb") — only the
	// umbrella's own internal changes/ledger refresh path does. bead
	// pg2-2j5ac.30.6.
	List(ctx context.Context, query schema.QueryExpr, idsOnly bool, cursor json.RawMessage) (*schema.PRListResult, error)

	// Files returns id's changed-file list. A targeted op (resolves to the
	// one backend that owns id), matching Show's existing convention — NOT
	// the fan-out scheme List uses. bead pg2-2j5ac.28.2.
	Files(ctx context.Context, id string) (*schema.PRFilesResult, error)

	// Commits returns id's commit list. Each entry MUST carry the
	// commit's own GitHub author login (schema.PRCommit.Author's doc
	// comment) — the co-owned-ownership classification's consumer. A
	// targeted op, same convention as Files above. bead pg2-2j5ac.28.2.
	Commits(ctx context.Context, id string) (*schema.PRCommitsResult, error)
}

// ReviewComment is one inline comment of a ReviewSubmitRequest (contract
// 9.1): anchored to Path/Line on Side ("LEFT" or "RIGHT").
type ReviewComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side"`
	Body string `json:"body"`
}

// ReviewSubmitRequest is the review_submit op's input (contract 9.1). ID is
// the PR id, in the same style as every other pr op's args.
type ReviewSubmitRequest struct {
	ID               string          `json:"id"`
	HeadSHA          string          `json:"head_sha"`
	Body             string          `json:"body"`
	Comments         []ReviewComment `json:"comments"`
	SupersedePending bool            `json:"supersede_pending"`
}

// SupersedeOutcome reports the result of deleting the actor's existing
// pending review before posting. A failed delete is reported here, never as
// an op error (contract 9.1).
type SupersedeOutcome struct {
	Attempted bool   `json:"attempted"`
	Deleted   bool   `json:"deleted"`
	Error     string `json:"error,omitempty"`
}

// ReviewSubmitResult is the review_submit op's output (contract 9.1).
// Supersede is nil (omitted) when supersede_pending was not set.
type ReviewSubmitResult struct {
	ReviewID  string            `json:"review_id"`
	State     string            `json:"state"`
	HeadSHA   string            `json:"head_sha"`
	AsOf      string            `json:"as_of"`
	Supersede *SupersedeOutcome `json:"supersede,omitempty"`
}

// ReviewSubmitter is an OPTIONAL capability of a pr backend: it posts a
// PENDING review anchored to req.HeadSHA. It is deliberately not part of
// Provider; NewDispatchTable registers the review_submit op only when the
// provider type-asserts to it, so other backends keep compiling. Errors
// MUST be wrapped with scriptout.Err* from INV-ERR-1's taxonomy.
type ReviewSubmitter interface {
	SubmitReview(ctx context.Context, req ReviewSubmitRequest) (ReviewSubmitResult, error)
}

// PendingReviewRequest is the review_pending op's input (contract 9.1a). ID is
// the PR id, in the same style as every other pr op's args.
type PendingReviewRequest struct {
	ID string `json:"id"`
}

// PendingReviewComment is one inline comment of the acting identity's pending
// review (contract 9.1a). Line is 0 when the host reports no line. Marked is
// whether the comment body carries a bot-authorship marker.
type PendingReviewComment struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Body   string `json:"body"`
	Marked bool   `json:"marked"`
}

// PendingReview is the structured record of the acting identity's PENDING
// review on a PR (contract 9.1a). CommitSHA is the REVIEW-level anchored
// commit, never a comment's commit. Stale is true when CommitSHA is not the
// PR head the result reports (an empty CommitSHA is stale). AllMarked is true
// only when the body and every comment carry the marker.
type PendingReview struct {
	ReviewID   string                 `json:"review_id"`
	DatabaseID int64                  `json:"database_id"`
	State      string                 `json:"state"`
	CommitSHA  string                 `json:"commit_sha"`
	Stale      bool                   `json:"stale"`
	Body       string                 `json:"body"`
	BodyMarked bool                   `json:"body_marked"`
	Comments   []PendingReviewComment `json:"comments"`
	AllMarked  bool                   `json:"all_marked"`
}

// PendingReviewResult is the review_pending op's output (contract 9.1a).
// Pending false with a nil Review is the explicit "none" answer: the lookup
// succeeded and the acting identity has no pending review. A failed lookup is
// never expressed here; it is an error from the closed INV-ERR-1 taxonomy.
type PendingReviewResult struct {
	Pending bool           `json:"pending"`
	HeadSHA string         `json:"head_sha"`
	AsOf    string         `json:"as_of"`
	Review  *PendingReview `json:"review,omitempty"`
}

// PendingReviewReader is an OPTIONAL capability of a pr backend: it resolves
// the acting identity's pending review on a PR. It is deliberately not part of
// Provider; NewDispatchTable registers the review_pending op only when the
// provider type-asserts to it. It MUST be read-only and fail-closed: any
// uncertainty is an error (wrapped with scriptout.Err* from INV-ERR-1's
// taxonomy), never a "none" result.
type PendingReviewReader interface {
	PendingReview(ctx context.Context, req PendingReviewRequest) (PendingReviewResult, error)
}
