package render

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ModalRow is one (left, right) pair displayed as a single line inside the modal.
type ModalRow struct {
	Left  string
	Right string
}

// HelpRow is a {keys, description} pair the help modal renders.
type HelpRow struct {
	Keys        string
	Description string
}

// Modal renders a centered, bordered, scrollable popup. The popup occupies
// the full screen as a "full-screen takeover" frame; the bordered box sits
// centered inside.
//
// Returns exactly `height` newline-separated lines, each clipped to `width`.
//
// scroll skips the first `scroll` content rows. Indicators appear on the
// box's first/last visible content line when content extends above/below
// the visible window:
//
//	↑ N more
//	↓ N more
//
// The box's footer always shows: "[esc] close   [↑↓] scroll".
func Modal(title string, rows []ModalRow, extraFooter string, width, height, scroll int) string {
	if width <= 0 || height <= 0 {
		return ""
	}

	// Box dimensions: ~80% of available, with a minimum.
	boxWidth := width - 4
	if boxWidth > 80 {
		boxWidth = 80
	}
	if boxWidth < 20 {
		boxWidth = width
	}
	boxHeight := height - 4
	if boxHeight < 5 {
		boxHeight = height
	}

	// Inner area (inside border): width-2, height-2 for borders + 2 reserved
	// rows (title + footer hint).
	contentWidth := boxWidth - 2
	contentHeight := boxHeight - 4 // top border + title + bottom border + footer hint
	if contentHeight < 1 {
		contentHeight = 1
	}

	footerLines := 0
	if extraFooter != "" {
		footerLines = len(wrapFooterLine(extraFooter, contentWidth))
	}
	contentHeight -= footerLines
	if contentHeight < 1 {
		contentHeight = 1
	}

	// Clamp scroll.
	if scroll < 0 {
		scroll = 0
	}
	maxScroll := len(rows) - contentHeight
	if maxScroll < 0 {
		maxScroll = 0
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}

	// Reserve lines for scroll indicators when applicable; the indicators
	// occupy their own line so the row at `scroll` remains visible.
	hasMoreAbove := scroll > 0
	rowBudget := contentHeight
	if hasMoreAbove {
		rowBudget--
	}
	if rowBudget < 0 {
		rowBudget = 0
	}
	// Determine how many rows actually fit before we know whether overflow exists.
	// First pass with rowBudget assuming no below indicator:
	end := scroll + rowBudget
	if end > len(rows) {
		end = len(rows)
	}
	hasMoreBelow := end < len(rows)
	if hasMoreBelow {
		// Reserve a line for the below indicator and recompute end.
		rowBudget--
		if rowBudget < 0 {
			rowBudget = 0
		}
		end = scroll + rowBudget
		if end > len(rows) {
			end = len(rows)
		}
		hasMoreBelow = end < len(rows)
	}

	var visibleRows []string
	if hasMoreAbove {
		visibleRows = append(visibleRows, fmt.Sprintf("↑ %d more", scroll))
	}
	for i := scroll; i < end; i++ {
		r := rows[i]
		// Left column right-padded so right column starts at a fixed offset.
		// Left budget: 12 cols (most key combos fit; longer ones still render but
		// shift the right column).
		leftCol := lipgloss.NewStyle().Width(12).Render(r.Left)
		visibleRows = append(visibleRows, leftCol+r.Right)
	}
	if hasMoreBelow {
		below := len(rows) - end
		visibleRows = append(visibleRows, fmt.Sprintf("↓ %d more", below))
	}

	// Pad to contentHeight.
	for len(visibleRows) < contentHeight {
		visibleRows = append(visibleRows, "")
	}

	// Compose the box content: title + blank + rows + footer hint.
	titleStyled := lipgloss.NewStyle().Bold(true).Render(title)
	footerHint := "[esc] close   [↑↓] scroll"

	var content strings.Builder
	content.WriteString(titleStyled)
	content.WriteString("\n")
	for _, r := range visibleRows {
		// Clip each row to contentWidth to avoid overflow.
		if lipgloss.Width(r) > contentWidth {
			// ANSI-aware: Modal callers don't use ANSI in left/right today,
			// so simple rune-aware slice via lipgloss.Width is fine. For
			// future-proofing we could route through wrap.Line.
			r = r[:contentWidth]
		}
		content.WriteString(r)
		content.WriteString("\n")
	}
	if extraFooter != "" {
		for _, line := range wrapFooterLine(extraFooter, contentWidth) {
			content.WriteString(line)
			content.WriteString("\n")
		}
	}
	content.WriteString(footerHint)

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Width(contentWidth).
		Render(content.String())

	// Center the box inside the full screen.
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

// HelpModal is a thin wrapper over Modal with a "Help — keybindings" title.
// extraFooter renders as one line below the keybindings table and above the
// built-in close-hint. Empty string disables it.
func HelpModal(rows []HelpRow, extraFooter string, width, height, scroll int) string {
	mrows := make([]ModalRow, len(rows))
	for i, r := range rows {
		mrows[i] = ModalRow{Left: r.Keys, Right: r.Description}
	}
	return Modal("Help — keybindings", mrows, extraFooter, width, height, scroll)
}

// legendRows is the hand-curated symbol list shown by LegendModal.
var legendRows = []ModalRow{
	{Left: "●", Right: "working    actively producing output"},
	{Left: "○", Right: "idle       waiting for input"},
	{Left: "◐", Right: "blocked    has work but can't proceed (see status)"},
	{Left: "⏸", Right: "paused     blocked on a usage/rate limit"},
	{Left: "?", Right: "awaiting   blocked on human input"},
	{Left: "☾", Right: "dormant    idle 20m+ (resumable)"},
	{Left: "⊘", Right: "auth       authentication failure — run /login"},
	{Left: "⚠", Right: "error      retryable error (auto-resuming)"},
	{Left: "✗", Right: "error      non-retryable error"},
	{Left: "🤖", Right: "subagents  count of subagent tool uses"},
	{Left: "🐚", Right: "shells     count of subshell tool uses"},
}

// LegendModal renders the hand-curated symbol legend.
func LegendModal(width, height, scroll int) string {
	return Modal("Legend — symbols", legendRows, "", width, height, scroll)
}

// wrapFooterLine renders a footer string as 1+ lines that each fit within
// width visible columns:
//
//   - If the entire line fits, returns one entry.
//   - Else if a single-space break exists where the left half fits, splits
//     there: head on the first line, tail wrapped on subsequent lines.
//   - Else char-wraps the entire line by runes.
//
// width <= 0 is treated as no-op (returns the input as a single entry).
func wrapFooterLine(line string, width int) []string {
	if width <= 0 || lipgloss.Width(line) <= width {
		return []string{line}
	}
	// Try a single break at the last space within the first `width` runes that
	// produces a head fitting in width.
	runes := []rune(line)
	breakAt := -1
	for i := width; i >= 0 && i < len(runes); i-- {
		if runes[i] == ' ' && lipgloss.Width(string(runes[:i])) <= width {
			breakAt = i
			break
		}
	}
	if breakAt > 0 {
		head := string(runes[:breakAt])
		tail := string(runes[breakAt+1:])
		return append([]string{head}, charWrap(tail, width)...)
	}
	return charWrap(line, width)
}

// charWrap chunks s into width-wide rune slices.
func charWrap(s string, width int) []string {
	runes := []rune(s)
	if width <= 0 || len(runes) == 0 {
		return []string{s}
	}
	var out []string
	for len(runes) > 0 {
		if len(runes) <= width {
			out = append(out, string(runes))
			return out
		}
		out = append(out, string(runes[:width]))
		runes = runes[width:]
	}
	return out
}
