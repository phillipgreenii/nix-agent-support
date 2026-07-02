package render

import (
	"fmt"
	"time"
)

// RateLimitUsageLabel renders the authoritative status-line rate_limits window
// value (ADR 0021 §1/§5) for one window (5h or 7d).
//
//   - Unknown value (nil pct) or unknown capture time (zero capturedAt) -> "".
//     The caller hides the segment or falls back. A zero capturedAt MUST render as
//     unknown, never as a 1970-derived stale age (the timeFromTS 1970 trap).
//   - A capture older than staleAfter -> "stale (age)" with a compact age.
//   - Otherwise -> "NN%" (a real 0% reading renders as "0%", not unknown).
func RateLimitUsageLabel(pct *float64, capturedAt, now time.Time, staleAfter time.Duration) string {
	if pct == nil || capturedAt.IsZero() {
		return ""
	}
	age := now.Sub(capturedAt)
	if staleAfter > 0 && age > staleAfter {
		return fmt.Sprintf("stale (%s)", compactAge(age))
	}
	return fmt.Sprintf("%.0f%%", *pct)
}

// compactAge formats a positive age as the single largest unit: "45s", "11m",
// "2h", or "3d". Sub-second / negative ages clamp to "0s".
func compactAge(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
