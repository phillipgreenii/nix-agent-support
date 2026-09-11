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

// modalLeftColumnMinWidth is the floor for Modal's Left column -- "most key
// combos fit" was the original fixed budget (HelpModal's own historical
// rationale for 12).
const modalLeftColumnMinWidth = 12

// modalLeftColumnGap is the minimum number of columns guaranteed between
// the widest Left value actually being rendered and the Right column that
// follows it.
const modalLeftColumnGap = 2

// modalLeftColumnWidth sizes Modal's Left column from the rows actually
// being rendered, rather than the fixed 12 the column used to be pinned
// to unconditionally [pg2-y6sy5]. lipgloss's Style.Width() word-wraps (and
// can silently clip) content wider than the width given -- it is not a
// pure padding floor -- so a fixed Width(12) truncated/collided with any
// Left value at or beyond 12 columns instead of merely under-padding it:
// the gate's old name "quota-paused" (exactly 12 columns) received ZERO gap
// and ran straight into the next field ("quota-pausedclear since - (owner:
// -)"), while "cicd-down" (9 columns) happened to get a 3-column gap
// incidentally.
// Computing the width from every row (not just the currently-visible
// slice, so the column doesn't shift as the operator scrolls) guarantees
// modalLeftColumnGap columns of real separation regardless of any
// individual Left value's length -- the same dynamic-width approach
// legendRows (below) already established for the legend's description
// column (pg2-58ecs).
func modalLeftColumnWidth(rows []ModalRow) int {
	width := modalLeftColumnMinWidth
	for _, r := range rows {
		if w := lipgloss.Width(r.Left) + modalLeftColumnGap; w > width {
			width = w
		}
	}
	return width
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

	leftColWidth := modalLeftColumnWidth(rows)
	var visibleRows []string
	if hasMoreAbove {
		visibleRows = append(visibleRows, fmt.Sprintf("↑ %d more", scroll))
	}
	for i := scroll; i < end; i++ {
		r := rows[i]
		// Left column right-padded so right column starts at a fixed offset,
		// sized by modalLeftColumnWidth (computed from ALL rows, not just this
		// visible slice, so the column doesn't shift width as the operator
		// scrolls).
		leftCol := lipgloss.NewStyle().Width(leftColWidth).Render(r.Left)
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
			// future-proofing we could route through Line.
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

// legendEntry is one row of the hand-curated symbol legend, one entry per
// DefaultGlyphs entry — pg-router's own health grammar (see glyphs.go). Label
// and Description are kept separate (rather than one pre-padded string) so
// legendRows can compute the gap between them from the actual longest
// label, instead of a gap hardcoded to fit only today's labels.
type legendEntry struct {
	Glyph       string
	Label       string
	Description string
}

var legendEntries = []legendEntry{
	{Glyph: DefaultGlyphs.OK, Label: "ok", Description: "actively producing output"},
	{Glyph: DefaultGlyphs.Cooling, Label: "cooling N s", Description: "backing off after a failure; resumes in N s"},
	{Glyph: DefaultGlyphs.Failing, Label: "failing ×N", Description: "failed N times in a row (see status)"},
	{Glyph: DefaultGlyphs.Paused, Label: "paused", Description: "the operator paused this listener/source"},
	{Glyph: DefaultGlyphs.Disabled, Label: "disabled", Description: "disabled at the configuration level"},
	{Glyph: DefaultGlyphs.Excluded, Label: "excluded", Description: "excluded from this run by a selector"},
	{Glyph: DefaultGlyphs.Stale, Label: "stale", Description: "hasn't ticked recently (see last tick)"},
}

// legendRowGap is the minimum number of columns kept between the longest
// label and the description column.
const legendRowGap = 2

// legendRows builds the ModalRow entries LegendModal renders. Each label is
// right-padded to the width of the longest label in legendEntries plus
// legendRowGap, so the description column lines up on every row regardless
// of any individual label's length — a fixed gap baked into the row text
// itself (the previous approach) misaligns as soon as a label like
// "cooling N s" or "failing ×N" runs longer than the gap sized for shorter
// labels such as "paused" or "stale".
func legendRows() []ModalRow {
	width := 0
	for _, e := range legendEntries {
		if w := lipgloss.Width(e.Label); w > width {
			width = w
		}
	}
	rows := make([]ModalRow, len(legendEntries))
	for i, e := range legendEntries {
		label := lipgloss.NewStyle().Width(width + legendRowGap).Render(e.Label)
		rows[i] = ModalRow{Left: e.Glyph, Right: label + e.Description}
	}
	return rows
}

// LegendModal renders the hand-curated symbol legend.
func LegendModal(width, height, scroll int) string {
	return Modal("Legend — symbols", legendRows(), "", width, height, scroll)
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
