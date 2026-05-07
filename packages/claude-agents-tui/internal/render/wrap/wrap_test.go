package wrap

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestEffectiveWidth(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{0, FallbackWidth},
		{-5, FallbackWidth},
		{1, 1},
		{80, 80},
		{200, 200},
	}
	for _, c := range cases {
		if got := EffectiveWidth(c.in); got != c.want {
			t.Errorf("EffectiveWidth(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestTier(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{0, TierNarrow}, // fallback to 80
		{30, TierTiny},
		{79, TierTiny},
		{80, TierNarrow},
		{119, TierNarrow},
		{120, TierWide},
		{200, TierWide},
	}
	for _, c := range cases {
		if got := Tier(c.in); got != c.want {
			t.Errorf("Tier(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestLineNoOpWhenFits(t *testing.T) {
	s := "hello"
	if got := Line(s, 10); got != s {
		t.Errorf("Line(%q, 10) = %q, want identity", s, got)
	}
}

func TestLinePreservesSGRAcrossCut(t *testing.T) {
	// Red "helloworld" reset, clipped to 5 cols. Visible width must be <= 5
	// and the SGR pair must remain balanced (color does not bleed past reset).
	in := "\x1b[31mhelloworld\x1b[0m"
	out := Line(in, 5)
	if w := lipgloss.Width(out); w > 5 {
		t.Errorf("Line width = %d, want <= 5; got %q", w, out)
	}
	if !strings.Contains(out, "\x1b[31m") {
		t.Errorf("missing opening SGR in %q", out)
	}
	if !strings.Contains(out, "\x1b[0m") {
		t.Errorf("missing reset SGR in %q", out)
	}
	if !strings.Contains(out, "…") {
		t.Errorf("expected ellipsis when cut, got %q", out)
	}
}

func TestLinePreservesOSC8Link(t *testing.T) {
	// OSC 8 hyperlink wrapping "longtext"; cut to 4 cols.
	link := "\x1b]8;;https://example.com\x1b\\longtext\x1b]8;;\x1b\\"
	out := Line(link, 4)
	if w := lipgloss.Width(out); w > 4 {
		t.Errorf("Line width = %d, want <= 4; got %q", w, out)
	}
	// Both opener and closer must be present so the hyperlink stays balanced.
	if !strings.Contains(out, "\x1b]8;;https://example.com\x1b\\") {
		t.Errorf("missing OSC 8 opener: %q", out)
	}
	if !strings.Contains(out, "\x1b]8;;\x1b\\") {
		t.Errorf("missing OSC 8 closer: %q", out)
	}
}

func TestLineCJKWidth(t *testing.T) {
	// Each CJK glyph is 2 cols. "日本" = 4 cols, fits in 5.
	in := "日本"
	if got := Line(in, 5); got != in {
		t.Errorf("Line(%q, 5) = %q, want identity (4 cols fits)", in, got)
	}
	// "日本語テスト" = 12 cols, clip to 5 cols → 2 glyphs + "…" = 5 cols.
	clipped := Line("日本語テスト", 5)
	if w := lipgloss.Width(clipped); w > 5 {
		t.Errorf("CJK clip width = %d, want <= 5; got %q", w, clipped)
	}
	if !strings.Contains(clipped, "…") {
		t.Errorf("expected ellipsis, got %q", clipped)
	}
}

func TestLineEllipsisBudget(t *testing.T) {
	// Cutting a length-N string to N-1 should produce an ellipsis (real cut),
	// not a bare slice.
	out := Line("abcdef", 5)
	if !strings.Contains(out, "…") {
		t.Errorf("expected ellipsis for cut, got %q", out)
	}
	if w := lipgloss.Width(out); w > 5 {
		t.Errorf("width = %d, want <= 5; got %q", w, out)
	}
}

func TestBlockClipsEachLineAndPreservesCount(t *testing.T) {
	in := "alpha-line-very-long\nshort\nbravo-line-also-long"
	out := Block(in, 8)
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("Block changed line count: got %d, want 3 — %q", len(lines), out)
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > 8 {
			t.Errorf("line %d width = %d, want <= 8; got %q", i, w, l)
		}
	}
	// "short" fits and must be untouched.
	if lines[1] != "short" {
		t.Errorf("short line mutated: got %q, want %q", lines[1], "short")
	}
}

func TestBlockNoOpAtZeroWidth(t *testing.T) {
	in := "anything goes\nand stays"
	if got := Block(in, 0); got != in {
		t.Errorf("Block(_, 0) should be no-op; got %q", got)
	}
}
