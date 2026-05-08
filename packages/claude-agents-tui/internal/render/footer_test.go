package render

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func TestFooterUpdatedRightAligned(t *testing.T) {
	updated := time.Date(2026, 5, 8, 21, 5, 43, 0, time.UTC)
	out := Footer(140, "", updated)
	if !strings.Contains(out, "Updated 21:05:43") {
		t.Errorf("expected 'Updated 21:05:43' at WIDE, got: %q", out)
	}
	if w := lipgloss.Width(out); w != 140 {
		t.Errorf("Footer(140) width = %d, want 140; got %q", w, out)
	}
	if !strings.HasSuffix(out, "Updated 21:05:43") {
		t.Errorf("Updated must hug right edge, got: %q", out)
	}
}

func TestFooterShortUpdatedAtNarrow(t *testing.T) {
	updated := time.Date(2026, 5, 8, 21, 5, 43, 0, time.UTC)
	out := Footer(100, "", updated)
	if !strings.Contains(out, "21:05:43") {
		t.Errorf("expected '21:05:43' at NARROW, got: %q", out)
	}
	if strings.Contains(out, "Updated 21:05:43") {
		t.Errorf("at NARROW should drop 'Updated' prefix, got: %q", out)
	}
	if !strings.HasSuffix(out, "21:05:43") {
		t.Errorf("clock must hug right edge, got: %q", out)
	}
}

func TestFooterDropsUpdatedAtTiny(t *testing.T) {
	updated := time.Date(2026, 5, 8, 21, 5, 43, 0, time.UTC)
	out := Footer(60, "", updated)
	if strings.Contains(out, "21:") {
		t.Errorf("at TINY should drop Updated entirely, got: %q", out)
	}
}

func TestFooterSingleLine(t *testing.T) {
	out := Footer(120, "", time.Now())
	if strings.Contains(out, "\n") {
		t.Errorf("Footer must be single line, got: %q", out)
	}
}

func TestFooterWidthInvariant(t *testing.T) {
	updated := time.Date(2026, 5, 8, 21, 5, 43, 0, time.UTC)
	for _, w := range []int{60, 80, 100, 119, 120, 140, 200} {
		got := Footer(w, "", updated)
		if width := lipgloss.Width(got); width != w {
			t.Errorf("Footer(%d) width = %d, want %d; got %q", w, width, w, got)
		}
	}
}

func TestFooterStatusFillsLeftColumn(t *testing.T) {
	updated := time.Date(2026, 5, 8, 21, 5, 43, 0, time.UTC)
	out := Footer(140, "fake-prompt-text", updated)
	if !strings.Contains(out, "fake-prompt-text") {
		t.Errorf("expected status in footer, got: %q", out)
	}
	if !strings.HasSuffix(out, "Updated 21:05:43") {
		t.Errorf("clock should hug right edge: %q", out)
	}
	if w := lipgloss.Width(out); w != 140 {
		t.Errorf("width = %d, want 140", w)
	}
}

func TestFooterEmptyStatusKeepsClockAtRight(t *testing.T) {
	updated := time.Date(2026, 5, 8, 21, 5, 43, 0, time.UTC)
	out := Footer(140, "", updated)
	if !strings.HasSuffix(out, "Updated 21:05:43") {
		t.Errorf("clock should hug right edge with empty status: %q", out)
	}
	if w := lipgloss.Width(out); w != 140 {
		t.Errorf("width = %d, want 140", w)
	}
}

func TestFooterStatusAtTinyWithoutClock(t *testing.T) {
	out := Footer(60, "fake-prompt-text", time.Now())
	if strings.Contains(out, "21:") || strings.Contains(out, "Updated") {
		t.Errorf("TINY should drop clock: %q", out)
	}
	if !strings.Contains(out, "fake-prompt-text") {
		t.Errorf("expected status in TINY output: %q", out)
	}
}

func TestFooterLeftWidth(t *testing.T) {
	cases := []struct {
		width, want int
	}{
		{200, 200 - 16 - 2}, // WIDE
		{120, 120 - 16 - 2}, // WIDE floor
		{100, 100 - 8 - 2},  // NARROW
		{80, 80 - 8 - 2},    // NARROW floor
		{60, 60},            // TINY: full
	}
	for _, c := range cases {
		if got := FooterLeftWidth(c.width); got != c.want {
			t.Errorf("FooterLeftWidth(%d) = %d, want %d", c.width, got, c.want)
		}
	}
}
