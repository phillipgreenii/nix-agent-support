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
func CmuxFiveHourColor(fivePct *float64, resetsAt time.Time, now time.Time) (colorHex string, countdown string) {
	if fivePct == nil || math.IsNaN(*fivePct) {
		return "", ""
	}
	used := math.Floor(*fivePct)

	var remSecs int64 = -1
	if !resetsAt.IsZero() {
		remSecs = int64(resetsAt.Sub(now).Seconds())
	}

	if used >= 80 {
		if remSecs > 0 {
			h := remSecs / 3600
			m := (remSecs % 3600) / 60
			return fiveHourRed, fmt.Sprintf("(%dh %dm)", h, m)
		}
		return fiveHourRed, ""
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
		return fiveHourYellow, ""
	}
	return "", ""
}
