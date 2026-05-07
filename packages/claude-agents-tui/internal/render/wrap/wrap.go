// Package wrap provides ANSI-aware width clamping for TUI output.
//
// The TUI contract: every line returned from View() satisfies
// lipgloss.Width(line) <= EffectiveWidth(termWidth). Block enforces this at
// the View boundary; tier-aware renderers produce information-preserving
// output before the boundary clip catches anything they miss.
package wrap

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// FallbackWidth is the assumed terminal width when the caller has none yet
// (e.g. tests, or before the first tea.WindowSizeMsg).
const FallbackWidth = 80

// Tier classifications for width-aware content selection.
const (
	TierTiny   = 0 // < 80
	TierNarrow = 1 // 80–119
	TierWide   = 2 // ≥ 120
)

// EffectiveWidth returns w when positive, FallbackWidth otherwise.
func EffectiveWidth(w int) int {
	if w > 0 {
		return w
	}
	return FallbackWidth
}

// Tier classifies a width into TierTiny / TierNarrow / TierWide. Width 0 is
// treated as FallbackWidth (TierNarrow).
func Tier(w int) int {
	ew := EffectiveWidth(w)
	switch {
	case ew >= 120:
		return TierWide
	case ew >= 80:
		return TierNarrow
	default:
		return TierTiny
	}
}

// Line truncates s to at most width visible columns, preserving ANSI/OSC-8
// sequences and adding "…" when content was cut. No-op when s already fits or
// when width <= 0.
func Line(s string, width int) string {
	if width <= 0 {
		return s
	}
	return ansi.Truncate(s, width, "…")
}

// Block splits s on "\n", clips each line via Line, rejoins. Width <= 0 is
// treated as no-op (callers should pass through EffectiveWidth first when they
// want fallback behavior).
func Block(s string, width int) string {
	if width <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = Line(line, width)
	}
	return strings.Join(lines, "\n")
}
