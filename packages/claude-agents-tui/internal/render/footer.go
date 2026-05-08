package render

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/phillipgreenii/claude-agents-tui/internal/render/wrap"
)

// FooterLeftWidth returns the available column count for the footer's left
// column (status string) at the given terminal width.
//
//	WIDE   ≥120  width − 16 (Updated HH:MM:SS) − 2 (gap)
//	NARROW 80–119 width − 8 (HH:MM:SS) − 2 (gap)
//	TINY   <80   width  (no clock)
func FooterLeftWidth(width int) int {
	switch wrap.Tier(width) {
	case wrap.TierWide:
		return width - 16 - 2
	case wrap.TierNarrow:
		return width - 8 - 2
	default:
		return width
	}
}

// Footer renders the bottom row of the TUI: status (caller-supplied,
// already-styled, already-clipped) on the left, Updated clock on the right.
// At TINY the clock is dropped and the status is given the full row.
//
// status may be the empty string; the left column is then a blank styled span.
//
//	WIDE   ≥120  <status, padded to width-18>  Updated 21:05:43
//	NARROW 80–119 <status, padded to width-10>  21:05:43
//	TINY   <80   <status, full width>
func Footer(width int, status string, updatedAt time.Time) string {
	tier := wrap.Tier(width)
	if tier == wrap.TierTiny {
		return lipgloss.NewStyle().Width(width).Align(lipgloss.Left).Render(status)
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
	leftWidth := width - rightWidth - gap
	if leftWidth < 1 {
		// Fall back: full-width status, no clock.
		return lipgloss.NewStyle().Width(width).Align(lipgloss.Left).Render(status)
	}

	leftStyled := lipgloss.NewStyle().Width(leftWidth).Align(lipgloss.Left).Render(status)
	rightStyled := lipgloss.NewStyle().Width(rightWidth).Align(lipgloss.Right).Render(rightLabel)
	return leftStyled + strings.Repeat(" ", gap) + rightStyled
}
