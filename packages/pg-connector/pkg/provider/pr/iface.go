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
	List(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.PRListResult, error)

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
