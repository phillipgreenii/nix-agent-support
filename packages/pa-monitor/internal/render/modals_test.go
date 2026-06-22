package render

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestModalRendersTitleAndContent(t *testing.T) {
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

func TestModalScrollOffsetSkipsRows(t *testing.T) {
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

func TestModalShowsScrollIndicators(t *testing.T) {
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
}

func TestModalDimensionsClampToTerminal(t *testing.T) {
	out := Modal("t", []ModalRow{{Left: "x", Right: "y"}}, "", 60, 20, 0)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 20 {
		t.Errorf("expected 20 lines (height), got %d", len(lines))
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > 60 {
			t.Errorf("line %d width %d > 60: %q", i, w, l)
		}
	}
}

func TestLegendModalContainsAllSymbols(t *testing.T) {
	out := LegendModal(120, 40, 0)
	for _, sym := range []string{"●", "○", "⏸", "?", "✕", "🤖", "🐚", "🌿", "⊘", "⚠", "✗"} {
		if !strings.Contains(out, sym) {
			t.Errorf("legend modal missing %q; got:\n%s", sym, out)
		}
	}
}

func TestHelpModalRendersGivenRows(t *testing.T) {
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

func TestHelpModalRendersExtraFooter(t *testing.T) {
	rows := []HelpRow{{Keys: "q", Description: "quit"}}
	out := HelpModal(rows, "Signal errors logged to: /tmp/foo.log", 100, 20, 0)
	if !strings.Contains(out, "Signal errors logged to: /tmp/foo.log") {
		t.Errorf("output missing extraFooter line:\n%s", out)
	}
}

func TestHelpModalOmitsExtraFooterWhenEmpty(t *testing.T) {
	rows := []HelpRow{{Keys: "q", Description: "quit"}}
	out := HelpModal(rows, "", 100, 20, 0)
	if strings.Contains(out, "Signal errors logged to") {
		t.Errorf("output unexpectedly contains the footer pattern:\n%s", out)
	}
}

func TestHelpModalFooterFitsOnOneLineWhenShort(t *testing.T) {
	rows := []HelpRow{{Keys: "q", Description: "quit"}}
	// Whole footer fits in contentWidth=78 (width=100, boxWidth=80, content=78).
	out := HelpModal(rows, "Signal errors logged to: /tmp/foo.log", 100, 20, 0)
	if !strings.Contains(out, "Signal errors logged to: /tmp/foo.log") {
		t.Errorf("output missing single-line footer:\n%s", out)
	}
}

func TestHelpModalFooterBreaksAtSpaceWhenTooLong(t *testing.T) {
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
		t.Errorf("label+path appeared on the same line — should have wrapped:\n%s", out)
	}
}

func TestHelpModalFooterCharWrapsLongPath(t *testing.T) {
	rows := []HelpRow{{Keys: "q", Description: "quit"}}
	// Path longer than contentWidth — must char-wrap across multiple lines.
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
	// Verify it actually wraps — the path must span multiple lines (not just one).
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
