// Package alert declares the alert capability's provider interface — a small,
// capability-scoped Go interface (never named after a backend/system,
// INV-CAP-1) that a Tier-2 alert backend's concrete provider implements.
//
// A backend implements this interface AND attention.Provider in one binary,
// merging their dispatch tables (INV-ALERT-7): attention is answered directly
// by the backend, never derived by the umbrella from alert list.
//
// This package sits alongside pkg/schema and pkg/scriptout as part of the
// module's shared surface importable across backend boundaries.
package alert

import (
	"context"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Provider is the alert capability's provider interface. It is read-only: no
// acknowledge, silence, or hide method may be added (INV-ALERT-6). A concrete
// backend MAY additionally implement pkg/provider.AuthChecker, asserted via a
// type-check (INV-AUTH-1).
type Provider interface {
	// Show returns the currently-firing alert with the given id, or
	// scriptout.ErrNotFound when it is not currently firing.
	Show(ctx context.Context, id string) (*schema.Alert, error)

	// List returns the firing alerts matching query. A nil/empty query means
	// the backend's entire firing set, unfiltered (a fixed connector
	// semantic, not a built-in query name). A multi-element query means run
	// each element and union the results, deduplicated by id. The result
	// MUST contain only actively-firing alerts: no query may widen that
	// (INV-ALERT-1). idsOnly, when true, means the caller only wants
	// PresentIDs populated.
	List(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.AlertListResult, error)

	// ListHistory returns firing episodes overlapping [since, until). query
	// is an optional named-query filter, already resolved to its expression
	// (nil when no name was given). This is a parameter-keyed op in the
	// ci.Provider.ListRuns style, and is deliberately separate from List
	// because its shape and cost model differ; attention never uses it.
	ListHistory(ctx context.Context, since, until time.Time, query schema.QueryExpr) (*schema.AlertHistoryResult, error)
}
