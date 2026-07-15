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
