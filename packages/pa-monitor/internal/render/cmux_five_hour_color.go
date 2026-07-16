package render

import (
	"fmt"
	"math"
	"time"
)

// cmux workspace accent colors for the 5h rate-limit window, mirroring the
// claude-status-line limits part.
const (
	fiveHourRed    = "#cc3333"
	fiveHourYellow = "#e0b000"
	// Dimmed variants shown when the reading is stale (older than staleAfter):
	// same hue, darker/muted, so a stale window reads as visibly less confident
	// than a fresh red/yellow — consistent with the "(stale)" progress label
	// (pg2-be8jy).
	fiveHourRedStale    = "#7a2b2b"
	fiveHourYellowStale = "#8a6d00"
)

// blockSecs is the 5h window length in seconds (matches the bash render_limit
// call for the 5h sub-part: `render_limit ... 18000 5h`).
const blockSecs int64 = 18000

// CmuxFiveHourColor returns the cmux workspace color hex ("" means "clear / no
// color") and a countdown string ("" when none) for the 5-hour rate-limit
// window, mirroring the claude-status-line 5h render_limit logic. fivePct is
// Claude's used_percentage [0,100] (nil = unknown). resetsAt is the 5h window
// reset (zero = unknown). now is the comparison time.
//
// used% >= 80 renders RED plus a "(Hh Mm)" countdown (omitted when the reset is
// missing or already past). Below 80, the color is YELLOW when floor(used%)
// exceeds the percent-through-block pace and clear otherwise; the pace guard
// keeps a missing/expired reset from yellowing everything. The countdown is
// produced only in the red branch (yellow never shows one), matching the bash.
//
// Staleness (pg2-be8jy): when the authoritative reading is older than staleAfter
// (capturedAt set, staleAfter > 0, now-capturedAt > staleAfter), the red/yellow
// accent is DIMMED rather than shown as a confident fresh color — mirroring the
// "(stale)" progress label so the two surfaces agree. A "clear" (no-color)
// reading has nothing to dim and stays clear. The countdown is unaffected (it is
// derived from resetsAt, independent of capture age).
func CmuxFiveHourColor(fivePct *float64, resetsAt time.Time, capturedAt time.Time, now time.Time, staleAfter time.Duration) (colorHex string, countdown string) {
	if fivePct == nil || math.IsNaN(*fivePct) {
		return "", ""
	}
	used := math.Floor(*fivePct)

	stale := staleAfter > 0 && !capturedAt.IsZero() && now.Sub(capturedAt) > staleAfter
	red, yellow := fiveHourRed, fiveHourYellow
	if stale {
		red, yellow = fiveHourRedStale, fiveHourYellowStale
	}

	var remSecs int64 = -1
	if !resetsAt.IsZero() {
		remSecs = int64(resetsAt.Sub(now).Seconds())
	}

	if used >= 80 {
		if remSecs > 0 {
			h := remSecs / 3600
			m := (remSecs % 3600) / 60
			return red, fmt.Sprintf("(%dh %dm)", h, m)
		}
		return red, ""
	}

	var ptb int64 = -1
	if remSecs > 0 {
		ptb = (blockSecs - remSecs) * 100 / blockSecs
		if ptb < 0 {
			ptb = 0
		}
		if ptb > 100 {
			ptb = 100
		}
	}
	if ptb >= 0 && int64(used) > ptb {
		return yellow, ""
	}
	return "", ""
}
