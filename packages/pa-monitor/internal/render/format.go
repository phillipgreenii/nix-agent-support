package render

import (
	"fmt"
	"strings"
)

// progressBar renders an N-cell horizontal bar at the given percent (0–100).
func progressBar(pct float64, width int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := int(pct / 100 * float64(width))
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

// fmtK formats a value as thousands. "1234" -> "1".
func fmtK(v float64) string {
	return fmt.Sprintf("%.0f", v/1000)
}
