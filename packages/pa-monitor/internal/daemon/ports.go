package daemon

import (
	"context"
	"time"
)

// Limits is the current account-global rate_limits reading (ADR 0021 §1).
// Every field is independently optional: a nil *float64 or a zero time.Time
// means "unknown/stale", explicitly distinct from a real 0% reading or a 1970
// timestamp. Phase 0 observed seven_day absent on this account, so those fields
// are commonly unset. No consumer reads this yet — the LimitsSource adapter and
// its wiring land in Phase 3.
type Limits struct {
	FiveHourPct      *float64
	FiveHourResetsAt time.Time
	SevenDayPct      *float64
	SevenDayResetsAt time.Time
	CapturedAt       time.Time
}

// LimitsSource is the daemon's consumer-side port (ADR 0021 §1 + §3) for the
// authoritative status-line rate_limits. Current returns the single most-recent
// record across all sources, ordered by embedded ts, or nil when none is
// available yet. It MUST NOT correlate by session_id and MUST NOT substitute 0
// for an absent value.
//
// Phase 2 defines the port and ships NO adapter; the daemon wires it as nil so
// behavior is unchanged. Phase 3 lands the sibling-file reader adapter and the
// daemon sampling that consumes it.
type LimitsSource interface {
	Current(ctx context.Context) (*Limits, error)
}
