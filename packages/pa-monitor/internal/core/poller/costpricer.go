package poller

import (
	"context"

	"github.com/phillipgreenii/pa-monitor/internal/core/ccusage"
)

// CostPricer is the poller's consumer-side port (ADR 0021 §3) for the active
// 5h block cost. It abstracts where the cost snapshot comes from so the poller
// depends only on this interface, never on the concrete ccusage provider
// (Runner/CachedRunner/ParseActiveBlock). The ccusage adapter
// (ccusage.NewProvider) is its first implementation, wired at the composition
// root; tests inject a fake.
//
// The returned *ccusage.Block is the shared domain DTO; ADR 0021 defers
// renaming it to usage.* until Phase 4, so the port names it here as data, not
// as a dependency on the ccusage provider logic.
type CostPricer interface {
	// ActiveBlock returns the current active 5h block, or (nil,nil) when none
	// is available yet (e.g. before the first successful probe).
	ActiveBlock(ctx context.Context) (*ccusage.Block, error)
	// Probed reports whether the first probe has completed and the error from
	// the most recent probe attempt (nil when the last attempt succeeded).
	Probed() (probed bool, err error)
}
