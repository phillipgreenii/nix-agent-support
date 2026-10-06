// Package activity declares the activity capability's provider interface: a
// small, capability-scoped Go interface (never named after a backend or
// system) that a Tier-2 backend's concrete provider implements. It follows
// this repo's per-capability convention (package named after the capability,
// interface named Provider), the same shape as pkg/provider/attention.
//
// Activity is range-shaped and stateless: the single op takes a time range in
// its op args and returns what the operator did inside it. There is no cursor,
// no ledger and no cache entry.
//
// A concrete backend MAY additionally implement pkg/provider.AuthChecker,
// asserted via a type-check rather than folded into this interface; see
// NewDispatchTable in dispatch.go.
package activity

import (
	"context"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Provider is the activity capability's provider interface.
type Provider interface {
	// ListActivity returns every item the operator did in [since, before), as
	// this backend's system records it, scoped to the operator's own identity.
	// since is inclusive and before is exclusive; a zero since means
	// open-ended. The items MAY be returned in any order (the caller sorts).
	ListActivity(ctx context.Context, since, before time.Time) (*schema.ActivityListResult, error)
}
