// Package provider declares small, capability-independent interfaces that a
// Tier-2 backend's own concrete provider MAY additionally implement,
// asserted via a type-check rather than folded into one monolithic
// interface — the same small-separately-asserted-interface pattern
// vcs.Provider/vcs.AuthChecker already use today in
// packages/pg-pr/pkg/provider/vcs/iface.go.
//
// This package intentionally knows nothing about the wire protocol
// (pkg/scriptout) or any capability's own schema (pkg/schema, or a sibling
// capability's own pkg/provider/<capability> package). Bridging a concrete
// provider's AuthChecker result to the wire-level auth_status response is a
// capability's own dispatch-table entry — built by the capability package,
// which imports both this package and its own provider type — not this
// package's job.
package provider

import "context"

// AuthChecker is an optional capability a backend's concrete provider may
// implement to support a one-shot auth preflight. CheckAuth returns nil
// when authenticated; a non-nil error otherwise (a capability's own
// provider package defines whatever auth-invalid sentinel its errors wrap,
// mirroring vcs.ErrAuthInvalid). A provider that does not implement
// AuthChecker is not treated as a forced/meaningless answer by anything in
// this module — pg-connector ships no shared credential-resolution library
// or convention of its own; each backend resolves its own credentials
// entirely on its own.
type AuthChecker interface {
	CheckAuth(ctx context.Context) error
}
