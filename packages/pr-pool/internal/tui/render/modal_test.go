package render

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestModal_ZeroDimensionsReturnEmpty(t *testing.T) {
	if out := Modal("t", []ModalRow{{Left: "a", Right: "b"}}, "", 0, 20, 0); out != "" {
		t.Errorf("width<=0 must return empty string, got %q", out)
	}
	if out := Modal("t", []ModalRow{{Left: "a", Right: "b"}}, "", 20, 0, 0); out != "" {
		t.Errorf("height<=0 must return empty string, got %q", out)
	}
}

func TestModal_NarrowWidthFallsBackToFullWidth(t *testing.T) {
	// width=10 -> boxWidth=6, below the 20-col floor, so Modal falls back to
	// the full requested width rather than a useless sliver.
	out := Modal("t", []ModalRow{{Left: "a", Right: "b"}}, "", 10, 20, 0)
	if out == "" {
		t.Fatal("expected non-empty output for a narrow-but-positive width")
	}
	for i, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if w := lipgloss.Width(l); w > 10 {
			t.Errorf("line %d width %d > 10: %q", i, w, l)
		}
	}
}

func TestModal_ShortHeightClampsContentAndFooter(t *testing.T) {
	// height=4 -> boxHeight=0, below the 5-row floor, so Modal falls back to
	// the full requested height; contentHeight then clamps to >=1 both before
	// and after the extraFooter's lines are subtracted (a multi-line footer
	// can still push the rendered box taller than the requested height, since
	// Modal clamps content rows, not the footer itself).
	longFooter := strings.Repeat("word ", 20) // forces a multi-line footer wrap
	out := Modal("t", []ModalRow{{Left: "a", Right: "b"}}, longFooter, 40, 4, 0)
	if out == "" {
		t.Fatal("expected non-empty output even at a degenerately short height")
	}
	if !strings.Contains(out, "word") {
		t.Errorf("expected the footer text to survive the clamp, got:\n%s", out)
	}
}

func TestModal_NegativeScrollClampsToZero(t *testing.T) {
	rows := make([]ModalRow, 5)
	for i := range rows {
		rows[i] = ModalRow{Left: fmt.Sprintf("k%d", i), Right: fmt.Sprintf("d%d", i)}
	}
	withNegative := Modal("t", rows, "", 80, 20, -10)
	withZero := Modal("t", rows, "", 80, 20, 0)
	if withNegative != withZero {
		t.Errorf("scroll=-10 must clamp identically to scroll=0:\ngot:\n%s\nwant:\n%s", withNegative, withZero)
	}
}

func TestModal_ScrollBeyondMaxClampsToMaxScroll(t *testing.T) {
	rows := make([]ModalRow, 50)
	for i := range rows {
		rows[i] = ModalRow{Left: fmt.Sprintf("k%d", i), Right: fmt.Sprintf("d%d", i)}
	}
	huge := Modal("t", rows, "", 80, 15, 9999)
	// Any scroll past the true maximum clamps to the same maxScroll, so two
	// different out-of-range values must render identically.
	alsoHuge := Modal("t", rows, "", 80, 15, 1_000_000)
	if huge != alsoHuge {
		t.Errorf("two different out-of-range scroll values must clamp identically:\na:\n%s\nb:\n%s", huge, alsoHuge)
	}
}

func TestModal_ClipsOverlongRowContent(t *testing.T) {
	rows := []ModalRow{{Left: "k", Right: strings.Repeat("x", 100)}}
	out := Modal("t", rows, "", 30, 20, 0)
	for i, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if w := lipgloss.Width(l); w > 30 {
			t.Errorf("line %d width %d > 30 after clipping overlong content: %q", i, w, l)
		}
	}
}

func TestModal_RendersTitleAndContent(t *testing.T) {
	rows := []ModalRow{{Left: "?", Right: "Help"}, {Left: "q", Right: "Quit"}}
	out := Modal("Test Title", rows, "", 80, 30, 0)
	if !strings.Contains(out, "Test Title") {
		t.Errorf("expected title in output, got:\n%s", out)
	}
	for _, r := range rows {
		if !strings.Contains(out, r.Right) {
			t.Errorf("expected %q in output, got:\n%s", r.Right, out)
		}
	}
	if !strings.Contains(out, "[esc] close") {
		t.Errorf("expected esc hint in output, got:\n%s", out)
	}
}

func TestModal_ScrollOffsetSkipsRows(t *testing.T) {
	rows := make([]ModalRow, 50)
	for i := range rows {
		rows[i] = ModalRow{Left: fmt.Sprintf("k%d", i), Right: fmt.Sprintf("desc%d", i)}
	}
	out := Modal("t", rows, "", 80, 15, 5)
	if strings.Contains(out, "k0") {
		t.Errorf("k0 should be scrolled past, got:\n%s", out)
	}
	if !strings.Contains(out, "k5") {
		t.Errorf("k5 should be visible after scroll=5, got:\n%s", out)
	}
}

func TestModal_ClipsToWidthHeight(t *testing.T) {
	rows := make([]ModalRow, 50)
	for i := range rows {
		rows[i] = ModalRow{Left: "k", Right: "d"}
	}
	out := Modal("t", rows, "", 80, 15, 5)
	if !strings.Contains(out, "↑") {
		t.Errorf("expected '↑' indicator at scroll=5, got:\n%s", out)
	}
	if !strings.Contains(out, "↓") {
		t.Errorf("expected '↓' indicator with overflow, got:\n%s", out)
	}

	out2 := Modal("t", []ModalRow{{Left: "x", Right: "y"}}, "", 60, 20, 0)
	lines := strings.Split(strings.TrimRight(out2, "\n"), "\n")
	if len(lines) != 20 {
		t.Errorf("expected 20 lines (height), got %d", len(lines))
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > 60 {
			t.Errorf("line %d width %d > 60: %q", i, w, l)
		}
	}
}

func TestLegendModal_ContainsAllGlyphs(t *testing.T) {
	out := LegendModal(120, 40, 0)
	for _, sym := range []string{
		DefaultGlyphs.OK,
		DefaultGlyphs.Cooling,
		DefaultGlyphs.Failing,
		DefaultGlyphs.Paused,
		DefaultGlyphs.Disabled,
		DefaultGlyphs.Excluded,
		DefaultGlyphs.Stale,
	} {
		if !strings.Contains(out, sym) {
			t.Errorf("legend modal missing %q; got:\n%s", sym, out)
		}
	}
}

func TestHelpModal_RendersGivenRows(t *testing.T) {
	rows := []HelpRow{
		{Keys: "down | j", Description: "Cursor down"},
		{Keys: "esc", Description: "Close"},
	}
	out := HelpModal(rows, "", 120, 40, 0)
	for _, r := range rows {
		if !strings.Contains(out, r.Keys) {
			t.Errorf("missing keys %q in output:\n%s", r.Keys, out)
		}
		if !strings.Contains(out, r.Description) {
			t.Errorf("missing description %q in output:\n%s", r.Description, out)
		}
	}
}

func TestHelpModal_RendersExtraFooter(t *testing.T) {
	rows := []HelpRow{{Keys: "q", Description: "quit"}}
	out := HelpModal(rows, "Signal errors logged to: /tmp/foo.log", 100, 20, 0)
	if !strings.Contains(out, "Signal errors logged to: /tmp/foo.log") {
		t.Errorf("output missing extraFooter line:\n%s", out)
	}
}

func TestHelpModal_OmitsExtraFooterWhenEmpty(t *testing.T) {
	rows := []HelpRow{{Keys: "q", Description: "quit"}}
	out := HelpModal(rows, "", 100, 20, 0)
	if strings.Contains(out, "Signal errors logged to") {
		t.Errorf("output unexpectedly contains the footer pattern:\n%s", out)
	}
}

func TestHelpModal_FooterFitsOnOneLineWhenShort(t *testing.T) {
	rows := []HelpRow{{Keys: "q", Description: "quit"}}
	// Whole footer fits in contentWidth=78 (width=100, boxWidth=80, content=78).
	out := HelpModal(rows, "Signal errors logged to: /tmp/foo.log", 100, 20, 0)
	if !strings.Contains(out, "Signal errors logged to: /tmp/foo.log") {
		t.Errorf("output missing single-line footer:\n%s", out)
	}
}

func TestHelpModal_FooterBreaksAtSpaceWhenTooLong(t *testing.T) {
	rows := []HelpRow{{Keys: "q", Description: "quit"}}
	// Long path forces break at " " between label and value.
	longPath := "/Users/phillipg/some/very/long/cache/dir/path/that/exceeds/modal/width/signal-errors.log"
	footer := "Signal errors logged to: " + longPath
	out := HelpModal(rows, footer, 100, 30, 0)
	// Label must appear by itself on a line (no path right after it).
	if !strings.Contains(out, "Signal errors logged to:") {
		t.Errorf("output missing label line:\n%s", out)
	}
	// The label line and path should not be concatenated together. The exact
	// concatenation (label + " " + path) must NOT appear in the output.
	if strings.Contains(out, "Signal errors logged to: "+longPath[:10]) {
		t.Errorf("label+path appeared on the same line -- should have wrapped:\n%s", out)
	}
}

func TestHelpModal_FooterCharWrapsLongPath(t *testing.T) {
	rows := []HelpRow{{Keys: "q", Description: "quit"}}
	// Path longer than contentWidth -- must char-wrap across multiple lines.
	longPath := strings.Repeat("a", 200)
	footer := "Signal errors logged to: " + longPath
	out := HelpModal(rows, footer, 100, 40, 0)
	// All 200 path chars must be present somewhere in the output. Because
	// lipgloss box borders interrupt runs of the same character across lines,
	// we count 'a' occurrences per-line and sum them up. We subtract the one
	// 'a' in "Signal" (the label text) to get the path-only count.
	aInLabel := strings.Count("Signal errors logged to:", "a") // 1 ("Signal")
	totalA := 0
	for _, line := range strings.Split(out, "\n") {
		totalA += strings.Count(line, "a")
	}
	pathA := totalA - aInLabel
	if pathA < 200 {
		t.Errorf("char-wrap dropped path bytes: found %d 'a' chars from path, want 200", pathA)
	}
	// Verify it actually wraps -- the path must span multiple lines (not just one).
	pathLines := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.Count(line, "a")-strings.Count("Signal errors logged to:", "a") > 0 {
			pathLines++
		}
	}
	if pathLines < 2 {
		t.Errorf("expected path to span multiple lines for char-wrap, got %d lines with path chars", pathLines)
	}
}

func TestWrapFooterLine_NoSpaceFallsBackToCharWrap(t *testing.T) {
	// No space anywhere in the line, so the space-break search never finds a
	// candidate and wrapFooterLine must fall back to charWrap wholesale.
	out := wrapFooterLine(strings.Repeat("a", 20), 5)
	if len(out) < 4 {
		t.Fatalf("expected the 20-char no-space line to char-wrap into >=4 chunks of <=5, got %d: %v", len(out), out)
	}
	for _, chunk := range out {
		if lipgloss.Width(chunk) > 5 {
			t.Errorf("chunk %q exceeds width 5", chunk)
		}
	}
}

func TestCharWrap_NoOpOnZeroWidthOrEmptyInput(t *testing.T) {
	if got := charWrap("abc", 0); len(got) != 1 || got[0] != "abc" {
		t.Errorf("charWrap(_, 0) must be a single-entry no-op, got %v", got)
	}
	if got := charWrap("", 5); len(got) != 1 || got[0] != "" {
		t.Errorf("charWrap(\"\", _) must be a single-entry no-op, got %v", got)
	}
}
