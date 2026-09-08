// Package search declares the search capability's provider interface — a
// small, capability-scoped Go interface (never named after a
// backend/system) (INV-CAP-1) that a Tier-2 backend or standalone plugin's
// concrete provider implements. It matches this repo's existing
// small-per-capability-interface convention (e.g. pr.Provider in
// packages/pg-connector/pkg/provider/pr) rather than one interface
// spanning multiple systems (INV-CAP-1).
//
// The design's own prose names this capability's interface "search.Source"
// (as a concept), but this repo's OWN established convention for every
// other capability names its provider interface Provider inside a package
// named after the capability (pr.Provider, ci.Provider, issue.Provider,
// scm.Provider, attention.Provider — never e.g. pr.Source). This package
// resolves that in favor of the repo's own established convention:
// package search, interface Provider — matching every sibling capability
// and what naming_convention_test.go's own capabilityPackages list already
// anticipates. "search.Source" remains an informal way to refer to this
// capability in prose/comments where useful, but the exported Go symbol is
// Provider. (The attention sibling capability independently reaches the
// identical resolution for attention.Provider, for the same reason — this
// is a shared repo convention both apply on their own, not a dependency of
// one on the other's landed code.)
//
// This package sits alongside pkg/schema and pkg/scriptout as part of the
// module's shared surface importable across backend boundaries — see
// cmd/pg-connector's layout-convention check.
package search

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Provider is the search capability's provider interface: a single,
// read-only op. Search queries every type this backend can return — there
// is no type-filter parameter; "the queried type(s)" is simply the union of
// every type any registered source can return. A concrete backend MAY
// additionally implement pkg/provider.AuthChecker, asserted via a
// type-check rather than folded into this interface (INV-AUTH-1) — see
// NewDispatchTable in dispatch.go.
type Provider interface {
	// Search returns every result this backend's own domain considers a
	// match for query. fields is the caller-requested attribute list — MAY
	// be empty, meaning "no specific attributes requested"; how an empty
	// list is interpreted (no attributes vs. every attribute this backend
	// has) is left to the concrete implementation — a freedom boundary this
	// interface does not narrow further. This method does not itself
	// validate fields against any known attribute name — that
	// responsibility sits entirely at the aggregation layer (the sibling
	// "search aggregation + CLI verb" packet), never duplicated here. A
	// well-behaved implementation silently ignores any requested attribute
	// it doesn't itself recognize or support — no error, no warning.
	Search(ctx context.Context, query string, fields []string) ([]schema.SearchResult, error)
}
