// Package thread declares the thread capability's provider interface — a
// small, capability-scoped Go interface (never named after a
// backend/system, INV-CAP-1) that a Tier-2 thread backend's concrete
// provider implements. It mirrors pkg/provider/issue's own package
// shape — iface.go declaring a capability-scoped Provider interface named
// for capability-level verbs only — as its STRUCTURAL template (bead
// pg2-2j5ac.40.3's own Contract), the same convention
// pkg/provider/pr/issue already follow.
//
// Unlike issue/pr, Provider here declares only Show/List: the design's own
// thread acceptance-criteria block never asks for a write op (no
// Create/Comment/Transition/Update/Close/Deps) — thread is a read-only
// cross-reference source, mirroring how pkg/provider/attention.Provider
// and pkg/provider/search.Provider are also read-only-by-design for their
// own capabilities.
//
// This package sits alongside pkg/schema and pkg/scriptout as part of the
// module's shared surface importable across backend boundaries — see
// cmd/pg-connector's layout-convention check.
package thread

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Provider is the thread capability's provider interface: a Show-style
// read plus List — no write ops (see this file's own package doc comment
// for why). A concrete backend MAY additionally implement
// pkg/provider.AuthChecker, asserted via a type-check rather than folded
// into this interface (INV-AUTH-1) — see NewDispatchTable in dispatch.go.
type Provider interface {
	// Show returns id's current state. The returned schema.Thread MUST
	// carry its own AsOf/Stale pair (mirroring Issue.Show/PR.Show's
	// identical INV-ASOF-1 contract).
	Show(ctx context.Context, id string) (*schema.Thread, error)

	// List runs query (already resolved from the request's own
	// config.queries block — dispatch.go's "list" handler does that
	// resolution centrally, reporting query_not_recognized itself before
	// ever calling List, mirroring pkg/provider/issue.Provider.List's
	// identical convention) and returns the matching threads. query MAY
	// carry more than one expression (design: "run each, union results
	// deduplicated by id, report truncated if any member truncated") —
	// List, not the dispatch table, does that fan-in. idsOnly, when true,
	// means the caller only wants ThreadListResult.PresentIDs populated; a
	// Provider MAY still choose to populate Entities anyway (harmless,
	// just wasted work) but need not.
	//
	// This capability has no incremental-listing backend at this phase —
	// mirroring issue.Provider.List's own no-cursor-parameter shape rather
	// than pr.Provider.List's cursor-carrying one — and every reply's
	// ThreadListResult.Truncated MUST be unconditionally true
	// [design: 4.5, 8, D23]: a Provider implementation must not compute a
	// conditional value here the way pg-connector-issue-jira's List does.
	List(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.ThreadListResult, error)
}
