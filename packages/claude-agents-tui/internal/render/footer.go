package render

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/phillipgreenii/claude-agents-tui/internal/render/wrap"
)

// Footer renders the bottom row of the TUI: legend on the left, Updated clock
// on the right. At TINY the clock is dropped and the full row is legend.
//
//	WIDE   ≥120: <legend, padded to width-18>  Updated 21:05:43
//	NARROW 80–119: <legend, padded to width-10>  21:05:43
//	TINY   <80:   <legend>
func Footer(width int, updatedAt time.Time) string {
	tier := wrap.Tier(width)
	if tier == wrap.TierTiny {
		return lipgloss.NewStyle().Width(width).Align(lipgloss.Left).Render(Legend(width))
	}

	var rightLabel string
	switch tier {
	case wrap.TierWide:
		rightLabel = "Updated " + updatedAt.Format("15:04:05") // 16 cols
	default: // TierNarrow
		rightLabel = updatedAt.Format("15:04:05") // 8 cols
	}
	rightWidth := lipgloss.Width(rightLabel)
	gap := 2
	legendWidth := width - rightWidth - gap
	if legendWidth < 1 {
		// Fall back: full-width legend, no clock.
		return Legend(width)
	}

	leftStyled := lipgloss.NewStyle().Width(legendWidth).Align(lipgloss.Left).Render(Legend(legendWidth))
	rightStyled := lipgloss.NewStyle().Width(rightWidth).Align(lipgloss.Right).Render(rightLabel)
	return leftStyled + strings.Repeat(" ", gap) + rightStyled
}
