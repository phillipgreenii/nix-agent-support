package render

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/render/wrap"
)

// TestRenderPathNodePRHyperlink pins F2's OSC-8 render: PR#<n> is emitted as a
// balanced OSC-8 hyperlink (ST-terminated) to pr.url, the URL is zero-width
// (so the row-width math is unaffected), the branch renders, and the link stays
// balanced under wrap.Line truncation at every width.
func TestRenderPathNodePRHyperlink(t *testing.T) {
	url := "https://github.com/owner/repo/pull/42"
	n := &aggregate.PathNode{
		FullPath:    "/p",
		DisplayPath: "/p",
		Branch:      "feat/x",
		PRInfo:      &session.PRInfo{Number: 42, State: "OPEN", URL: url},
	}
	out := RenderPathNode(n, TreeOpts{Width: 200}, false, false)

	if !strings.Contains(out, "PR#42") {
		t.Errorf("want visible PR#42, got %q", out)
	}
	if !strings.Contains(out, "feat/x") {
		t.Errorf("want branch feat/x rendered, got %q", out)
	}
	opener := "\x1b]8;;" + url + "\x1b\\"
	closer := "\x1b]8;;\x1b\\"
	if !strings.Contains(out, opener) {
		t.Errorf("missing OSC 8 opener %q in %q", opener, out)
	}
	if !strings.Contains(out, closer) {
		t.Errorf("missing OSC 8 closer in %q", out)
	}
	// Exactly one opener + one closer: both start with "\x1b]8;;", so a balanced
	// link has exactly two such markers.
	if got := strings.Count(out, "\x1b]8;;"); got != 2 {
		t.Errorf("OSC 8 marker count = %d, want 2 (balanced opener+closer)", got)
	}

	// Truncation invariant: at every width the row stays <= width and the link
	// never ends up with an opener but no closer (ansi.Truncate keeps it balanced).
	full := lipgloss.Width(out)
	for w := 1; w <= full+3; w++ {
		clip := wrap.Line(out, w)
		if lw := lipgloss.Width(clip); lw > w {
			t.Fatalf("wrap.Line width = %d, want <= %d: %q", lw, w, clip)
		}
		hasOpen := strings.Contains(clip, opener)
		hasClose := strings.Contains(clip, closer)
		if hasOpen && !hasClose {
			t.Fatalf("truncation to %d left an unbalanced OSC 8 opener: %q", w, clip)
		}
	}
}

func TestRenderPathNodeExpandedGlyph(t *testing.T) {
	n := &aggregate.PathNode{
		FullPath:    "/p",
		DisplayPath: "/p",
		Depth:       0,
		WorkingN:    1,
		TotalTokens: 5000,
	}
	out := RenderPathNode(n, TreeOpts{}, false, false)
	if !strings.Contains(out, "▼") {
		t.Errorf("expanded node should contain ▼, got: %q", out)
	}
	if strings.Contains(out, "▶") {
		t.Errorf("expanded node should not contain ▶, got: %q", out)
	}
}

func TestRenderPathNodeCollapsedGlyph(t *testing.T) {
	n := &aggregate.PathNode{FullPath: "/p", DisplayPath: "/p", Depth: 0}
	out := RenderPathNode(n, TreeOpts{}, false, true)
	if !strings.Contains(out, "▶") {
		t.Errorf("collapsed node should contain ▶, got: %q", out)
	}
}

func TestRenderPathNodeCursorPrefix(t *testing.T) {
	n := &aggregate.PathNode{FullPath: "/p", DisplayPath: "/p", Depth: 0}
	selected := RenderPathNode(n, TreeOpts{HasCursor: true}, true, false)
	notSelected := RenderPathNode(n, TreeOpts{HasCursor: true}, false, false)
	if !strings.HasPrefix(selected, "> ") {
		t.Errorf("selected node should start with '> ', got %q", selected)
	}
	if !strings.HasPrefix(notSelected, "  ") {
		t.Errorf("unselected node should start with '  ', got %q", notSelected)
	}
}

func TestRenderPathNodeDepthIndentation(t *testing.T) {
	n0 := &aggregate.PathNode{FullPath: "/a", DisplayPath: "/a", Depth: 0}
	n1 := &aggregate.PathNode{FullPath: "/a/b", DisplayPath: "b", Depth: 1}
	out0 := RenderPathNode(n0, TreeOpts{}, false, false)
	out1 := RenderPathNode(n1, TreeOpts{}, false, false)
	// The label is now formatted as indent + glyph + " " + displayPath, so
	// the glyph itself sits at the right column for the node's depth. Each row
	// starts with the 2-col cursor mark "  " (no cursor selected here). After
	// the cursor mark, the glyph is preceded by 2*Depth spaces of indent.
	const cursorMark = "  "
	idx0 := strings.Index(out0, "▼")
	idx1 := strings.Index(out1, "▼")
	if idx0 < 0 || idx1 < 0 {
		t.Fatalf("could not find glyph in output: depth0=%q depth1=%q", out0, out1)
	}
	// Number of spaces between cursor mark and the glyph == 2 * Depth.
	indent0 := idx0 - len(cursorMark)
	indent1 := idx1 - len(cursorMark)
	if indent1 <= indent0 {
		t.Errorf("depth=1 should have more indentation than depth=0: depth0=%d depth1=%d", indent0, indent1)
	}
}

func TestRenderPathNodeShowsDisplayPath(t *testing.T) {
	n := &aggregate.PathNode{FullPath: "/a/b/c", DisplayPath: "b/c", Depth: 1}
	out := RenderPathNode(n, TreeOpts{}, false, false)
	if !strings.Contains(out, "b/c") {
		t.Errorf("should show DisplayPath 'b/c', got: %q", out)
	}
}

func TestRenderPathNodeRollupTokens(t *testing.T) {
	n := &aggregate.PathNode{
		FullPath: "/p", DisplayPath: "/p",
		WorkingN: 2, TotalTokens: 12345,
	}
	out := RenderPathNode(n, TreeOpts{CostMode: false}, false, false)
	if !strings.Contains(out, "2●") {
		t.Errorf("expected '2●' in rollup, got: %q", out)
	}
	// FmtTok(12345) == "12.3k". The unified column grid drops the " tok"
	// suffix the old free-form rollup string used.
	if !strings.Contains(out, "12.3k") {
		t.Errorf("expected '12.3k' in rollup amount column, got: %q", out)
	}
}

func TestRenderPathNodeRollupCost(t *testing.T) {
	n := &aggregate.PathNode{
		FullPath: "/p", DisplayPath: "/p",
		TotalCostUSD: 1.23,
	}
	out := RenderPathNode(n, TreeOpts{CostMode: true}, false, false)
	if !strings.Contains(out, "$1.23") {
		t.Errorf("expected '$1.23' in cost rollup, got: %q", out)
	}
}

// TestTreeIndentedPathNodeGlyph verifies that the ▼/▶ glyph sits after the
// depth indent on a nested PathNode row, not at column 0.
func TestTreeIndentedPathNodeGlyph(t *testing.T) {
	node := &aggregate.PathNode{
		DisplayPath: "leaf",
		Depth:       2, // indent = "    " (4 spaces)
	}
	out := RenderPathNode(node, TreeOpts{}, false, false)
	// The glyph "▼" should be preceded by 2*Depth = 4 spaces (after the
	// 2-col cursor mark "  ").
	prefix := "    " // depth indent (2 * 2 = 4 spaces)
	if !strings.HasPrefix(out, "  "+prefix+"▼ ") {
		t.Errorf("expected glyph at column 6 ('  ' cursor + '    ' indent + '▼'), got: %q", out)
	}
}
