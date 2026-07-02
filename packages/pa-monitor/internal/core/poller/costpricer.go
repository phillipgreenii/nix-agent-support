package poller

import (
	"context"

	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
)

// CostPricer is the poller's consumer-side port (ADR 0021 §3) for the active
// 5h block cost. It abstracts where the cost snapshot comes from so the poller
// depends only on this interface. The native adapter (usage.NativePricer —
// transcript usage × the Account's config price table) is its production
// implementation, wired at the composition root; tests inject a fake. The
// returned *usage.Block is the shared domain DTO.
type CostPricer interface {
	// ActiveBlock returns the current active 5h block, or (nil,nil) when none
	// is available yet (e.g. before the first successful probe).
	ActiveBlock(ctx context.Context) (*usage.Block, error)
	// Probed reports whether the first probe has completed and the error from
	// the most recent probe attempt (nil when the last attempt succeeded).
	Probed() (probed bool, err error)
}
