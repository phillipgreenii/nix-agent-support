// Package calendar declares the calendar capability's provider interface —
// a small, capability-scoped Go interface (never named after a
// backend/system, INV-CAP-1) that a Tier-2 calendar backend's concrete
// provider implements. It mirrors pkg/provider/thread's own package shape
// (package named after the capability, interface named Provider — never
// e.g. calendar.Source) as its STRUCTURAL template, this docket's own
// Contract.
//
// Unlike issue/pr, this capability has no create/update ops at this phase:
// this docket's own decomposition found a real, load-bearing gap —
// osx-bridge-api's already-landed calendarapi.Provider interface
// (packages/osx-bridge-api/internal/calendarapi/types.go) exposes only
// Calendars()/Events() (read-only), and osx-bridge-api's own package doc
// comment states the write ops were deliberately deferred to "a later
// bead." Provider therefore declares only List/ListEvents — no Show,
// Create, or Update method exists on this interface in this round.
//
// This package sits alongside pkg/schema and pkg/scriptout as part of the
// module's shared surface importable across backend boundaries — see
// cmd/pg-connector's layout-convention check.
package calendar

import (
	"context"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Provider is the calendar capability's provider interface: two read ops,
// List (the generic, named-query-resolved op) and ListEvents (the primary,
// explicitly time-range-parameterized op) — no Show/Create/Update in this
// round (see this file's own package doc comment for why). A concrete
// backend MAY additionally implement pkg/provider.AuthChecker, asserted via
// a type-check rather than folded into this interface (INV-AUTH-1) — see
// NewDispatchTable in dispatch.go. Both methods return the SAME result type
// (schema.CalendarListResult); only the request shape differs. A Provider
// implementation MAY share internal logic between them however it likes
// [freedom boundary].
type Provider interface {
	// List runs query (already resolved from the request's own
	// config.queries block — dispatch.go's "list" handler does that
	// resolution centrally, reporting query_not_recognized itself before
	// ever calling List, mirroring pkg/provider/issue.Provider.List's and
	// pkg/provider/thread.Provider.List's identical convention) and
	// returns the matching events. idsOnly, when true, means the caller
	// only wants CalendarListResult.PresentIDs populated; a Provider MAY
	// still choose to populate Entities anyway (harmless, just wasted
	// work) but need not.
	//
	// query's element strings are NOT free text and NOT a JQL-style
	// grammar: each element MUST be a plain Go time.ParseDuration-parseable
	// duration string (ns/us/ms/s/m/h units only, no d/w), read as "how far
	// ahead of NOW to look" — mirroring this same repo's own established
	// convention for an identically-shaped value,
	// home/programs/pg-connector/default.nix's attention.perBackend.threshold
	// option, whose own doc comment states verbatim: "a Go
	// time.ParseDuration string (e.g. "72h"; there is no d/w unit, only
	// ns/us/ms/s/m/h)." A concrete implementation computes
	// start = time.Now(), end = start.Add(<parsed duration>), and — when
	// query carries more than one element — uses the LARGEST parsed
	// duration (mirroring ThreadListResult's own "run each, union"
	// convention adapted to calendar's single-time-range domain, where more
	// than one window collapses to the widest one rather than a
	// per-element union of disjoint result sets), then delegates to the
	// same underlying calendar-fetch logic ListEvents uses. A malformed
	// (non-time.ParseDuration) element MUST answer
	// scriptout.ErrInvalidArgument, never silently ignored or defaulted.
	//
	// This capability has no incremental-listing backend at this phase —
	// mirroring issue.Provider.List's and thread.Provider.List's current
	// 3-arg shape rather than pr.Provider.List's 4-arg cursor-carrying one.
	List(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.CalendarListResult, error)

	// ListEvents returns every event in [start, end) — mirroring
	// ci.Provider.ListRuns's own "fan-out-shaped, parameter-keyed, NOT a
	// named query" precedent. calendar, when empty, means every calendar
	// this backend is configured for; a non-empty value pins to exactly the
	// named calendar. Exactly how "every configured calendar" vs. one named
	// calendar is resolved is left to the concrete implementation [freedom
	// boundary] — this interface only fixes the signature.
	ListEvents(ctx context.Context, start, end time.Time, calendar string) (*schema.CalendarListResult, error)
}
