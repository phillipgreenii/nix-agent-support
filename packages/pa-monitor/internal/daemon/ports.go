package daemon

import (
	"context"

	"github.com/phillipgreenii/pa-monitor/internal/core/limits"
)

// Limits is the current account-global rate_limits reading (ADR 0021 §1). It is
// an alias of limits.Limits: the DTO + the ADR-0029 window-peak fold moved to the
// leaf package internal/core/limits so both this daemon port and the corpus
// Monitor's Limits observer share one implementation (pg2-5sxkb). The alias keeps
// every existing daemon.Limits reference (applyLimits, blockToStoreBlockWithLimits)
// working unchanged.
type Limits = limits.Limits

// LimitsSource is the daemon's consumer-side port (ADR 0021 §1 + §3) for the
// authoritative status-line rate_limits. Current returns the account-global
// CURRENT-WINDOW reading, or nil when none is available yet: the capture time is
// the newest record's ts, and each window's used_percentage is that window's PEAK
// (ADR 0029, refining §1's original "single most-recent record"). Account-global
// usage only rises within a fixed window, so the peak is correct; a session's
// REPORTED value can lag (it shows its last-seen snapshot), which is why the
// newest record — not the newest usage — can be a stale sub-peak.
// It MUST NOT correlate by session_id and MUST NOT substitute 0 for an absent value.
//
// Phase 2 defines the port and ships NO adapter; the daemon wires it as nil so
// behavior is unchanged. Phase 3 lands the sibling-file reader adapter and the
// daemon sampling that consumes it.
type LimitsSource interface {
	Current(ctx context.Context) (*Limits, error)
}
