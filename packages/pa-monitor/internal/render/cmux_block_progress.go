package render

import (
	"fmt"
	"time"
)

// CmuxBlockProgress computes the 5h progress bar fraction [0,1], its label, and ok
// for the cmux status sidebar. It prefers the authoritative status-line five_hour
// used_percentage (ADR 0021 §5) over the cost/cap estimate — the same choice the
// on-screen BlockRow makes — so the cmux status agrees with the TUI and claude.ai
// instead of showing the cost/cap dollar ratio.
//
// The authoritative value wins whenever it was ever captured (fivePct non-nil AND
// capturedAt set): a fresh reading renders "block NN%"; a reading older than
// staleAfter renders "block NN% (stale)" rather than reverting to the misleading
// cost estimate. Only when no authoritative reading exists at all does it fall back
// to the cost/cap percentage (costPct, gated by costOK). Both cmux-sidebar producers
// — the TUI's push and the headless cmux-bridge — call this so they render an
// identical label for the same state. Callers handle the paused / window-exhausted
// state before calling this.
func CmuxBlockProgress(fivePct *float64, capturedAt time.Time, costPct float64, costOK bool, now time.Time, staleAfter time.Duration) (frac float64, label string, ok bool) {
	if fivePct != nil && !capturedAt.IsZero() {
		pct := *fivePct
		if staleAfter > 0 && now.Sub(capturedAt) > staleAfter {
			return clampUnit(pct / 100), fmt.Sprintf("block %.0f%% (stale)", pct), true
		}
		return clampUnit(pct / 100), fmt.Sprintf("block %.0f%%", pct), true
	}
	if costOK {
		return clampUnit(costPct / 100), fmt.Sprintf("block %.0f%% of cap", costPct), true
	}
	return 0, "", false
}

// clampUnit constrains a bar fraction to [0,1]; a >100% reading (cost far over cap,
// or a limit past 100) still renders a full — not overflowing — bar.
func clampUnit(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
