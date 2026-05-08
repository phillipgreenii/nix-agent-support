package render

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestModalRendersTitleAndContent(t *testing.T) {
	rows := []ModalRow{{Left: "?", Right: "Help"}, {Left: "q", Right: "Quit"}}
	out := Modal("Test Title", rows, 80, 30, 0)
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
	out := Modal("t", rows, 80, 15, 5)
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
	out := Modal("t", rows, 80, 15, 5)
	if !strings.Contains(out, "↑") {
		t.Errorf("expected '↑' indicator at scroll=5, got:\n%s", out)
	}
	if !strings.Contains(out, "↓") {
		t.Errorf("expected '↓' indicator with overflow, got:\n%s", out)
	}
}

func TestModalDimensionsClampToTerminal(t *testing.T) {
	out := Modal("t", []ModalRow{{Left: "x", Right: "y"}}, 60, 20, 0)
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
	for _, sym := range []string{"●", "○", "⏸", "?", "✕", "🤖", "🐚", "🌿"} {
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
	out := HelpModal(rows, 120, 40, 0)
	for _, r := range rows {
		if !strings.Contains(out, r.Keys) {
			t.Errorf("missing keys %q in output:\n%s", r.Keys, out)
		}
		if !strings.Contains(out, r.Description) {
			t.Errorf("missing description %q in output:\n%s", r.Description, out)
		}
	}
}
