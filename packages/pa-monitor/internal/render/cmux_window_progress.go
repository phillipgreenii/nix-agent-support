package render

import "time"

// CmuxWindowProgress is the single 5h progress builder shared by both cmux
// sidebar producers — the TUI push (internal/tui) and the headless cmux-bridge
// (cmd/pa-monitor) — so they never disagree about whether the 5h block is
// exhausted (pg2-vux8d).
//
// When windowResetsAt is set the current block is exhausted and we are waiting
// for the window to reset: it latches to a full bar with an explanatory label,
// exactly as the TUI already did. Otherwise it defers to CmuxBlockProgress,
// which prefers the authoritative five_hour used_percentage (marking it
// "(stale)" past staleAfter) and falls back to the cost/cap estimate.
func CmuxWindowProgress(windowResetsAt time.Time, fivePct *float64, capturedAt time.Time, costPct float64, costOK bool, now time.Time, staleAfter time.Duration) (frac float64, label string, ok bool) {
	if !windowResetsAt.IsZero() {
		return 1.0, "5h block exhausted — waiting for reset", true
	}
	return CmuxBlockProgress(fivePct, capturedAt, costPct, costOK, now, staleAfter)
}
