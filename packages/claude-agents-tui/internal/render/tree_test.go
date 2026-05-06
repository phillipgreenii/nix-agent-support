package render

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/session"
)

func TestTreeRendersSymbolsAndNames(t *testing.T) {
	d := &aggregate.Directory{
		Path: "/tmp/demo",
		Sessions: []*aggregate.SessionView{
			{
				Session: &session.Session{Name: "my-branch", SessionID: "abcdefgh-x", Status: session.Working, Kind: "interactive"},
				SessionEnrichment: aggregate.SessionEnrichment{
					ContextTokens: 50_000,
					Model:         "claude-opus-4-7",
					FirstPrompt:   "fix things",
					SubagentCount: 2,
					SubshellCount: 1,
					BurnRateShort: 25_000,
				},
			},
		},
		WorkingN: 1,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	out := Tree(tree, TreeOpts{ShowAll: false, ForceID: false, CostMode: false})
	if !strings.Contains(out, "/tmp/demo") {
		t.Errorf("expected dir path, got:\n%s", out)
	}
	if !strings.Contains(out, "●") {
		t.Errorf("expected working symbol, got:\n%s", out)
	}
	if !strings.Contains(out, "my-branch") {
		t.Errorf("expected name, got:\n%s", out)
	}
	if !strings.Contains(out, "fix things") {
		t.Errorf("expected first prompt, got:\n%s", out)
	}
	if !strings.Contains(out, "2🤖") {
		t.Errorf("expected subagent count, got:\n%s", out)
	}
	if !strings.Contains(out, "1🐚") {
		t.Errorf("expected subshell count, got:\n%s", out)
	}
}

func TestTreeForceIDHidesName(t *testing.T) {
	d := &aggregate.Directory{
		Path: "/p",
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{Name: "x", SessionID: "id-123", Status: session.Working}},
		},
		WorkingN: 1,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	out := Tree(tree, TreeOpts{ForceID: true})
	if strings.Contains(out, "x") && !strings.Contains(out, "id-123") {
		t.Errorf("ForceID should show id and hide name, got:\n%s", out)
	}
}

// sessionRows returns lines that contain a tree branch prefix (├─ or └─).
// Works whether or not the cursor prefix ("  " or "> ") is present.
func sessionRows(out string) []string {
	var rows []string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "├─") || strings.Contains(line, "└─") {
			rows = append(rows, line)
		}
	}
	return rows
}

func TestTreeLastSessionUsesBottomAngleConnector(t *testing.T) {
	d := &aggregate.Directory{
		Path: "/p",
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{Name: "alpha", SessionID: "id-a", Status: session.Working},
				SessionEnrichment: aggregate.SessionEnrichment{Model: "claude-opus-4-7", FirstPrompt: "alpha prompt"}},
			{Session: &session.Session{Name: "beta", SessionID: "id-b", Status: session.Working},
				SessionEnrichment: aggregate.SessionEnrichment{Model: "claude-sonnet-4-6", FirstPrompt: "beta prompt"}},
		},
		WorkingN: 2,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	out := Tree(tree, TreeOpts{})

	rows := sessionRows(out)
	if len(rows) != 2 {
		t.Fatalf("expected 2 session rows, got %d:\n%s", len(rows), out)
	}
	if !strings.Contains(rows[0], "├─") {
		t.Errorf("first row should contain ├─, got: %q", rows[0])
	}
	if !strings.Contains(rows[1], "└─") {
		t.Errorf("last row should contain └─, got: %q", rows[1])
	}
	// The last session's continuation line must not start with an orphan '│'.
	// It should instead start with a space (aligned under └─).
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "↳ \"beta prompt\"") {
			if strings.HasPrefix(line, "│") {
				t.Errorf("last session continuation line has orphan │: %q", line)
			}
			return
		}
	}
	t.Errorf("expected continuation line for beta prompt, got:\n%s", out)
}

func TestTreeIdleAwaitingShowsQuestionMark(t *testing.T) {
	d := &aggregate.Directory{
		Path: "/p",
		Sessions: []*aggregate.SessionView{
			{
				Session:           &session.Session{Name: "waiting", Status: session.Idle},
				SessionEnrichment: aggregate.SessionEnrichment{AwaitingInput: true},
			},
		},
		IdleN: 1,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	out := Tree(tree, TreeOpts{})
	if !strings.Contains(out, "?") {
		t.Errorf("expected '?' symbol for idle+awaiting session, got:\n%s", out)
	}
}

func TestTreeBranchShownInDirRow(t *testing.T) {
	d := &aggregate.Directory{
		Path:   "/p",
		Branch: "feat/xyz",
		Sessions: []*aggregate.SessionView{
			{
				Session:           &session.Session{Name: "n", Status: session.Working},
				SessionEnrichment: aggregate.SessionEnrichment{},
			},
		},
		WorkingN: 1,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	out := Tree(tree, TreeOpts{})
	lines := strings.Split(out, "\n")
	if len(lines) == 0 || !strings.Contains(lines[0], "feat/xyz") {
		t.Errorf("expected branch name in first (dir) row, got:\n%s", out)
	}
}

func TestTreeCursorPrefix(t *testing.T) {
	d := &aggregate.Directory{
		Path: "/p",
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{Name: "first", Status: session.Working}},
			{Session: &session.Session{Name: "second", Status: session.Working}},
		},
		WorkingN: 2,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}

	out := Tree(tree, TreeOpts{HasCursor: true, Cursor: 0})
	var sessionLines []string
	for l := range strings.SplitSeq(out, "\n") {
		if strings.Contains(l, "├─") || strings.Contains(l, "└─") {
			sessionLines = append(sessionLines, l)
		}
	}
	if len(sessionLines) < 2 {
		t.Fatalf("expected 2 session lines, got %d:\n%s", len(sessionLines), out)
	}
	if !strings.HasPrefix(sessionLines[0], "> ") {
		t.Errorf("selected row should start with '> ', got %q", sessionLines[0])
	}
	if !strings.HasPrefix(sessionLines[1], "  ") {
		t.Errorf("unselected row should start with '  ', got %q", sessionLines[1])
	}
}

func TestDirRowRollupRightAligned(t *testing.T) {
	// The directory rollup should end at the same column as a session stats row.
	short := &aggregate.SessionView{
		Session:           &session.Session{Name: "a", Status: session.Working},
		SessionEnrichment: aggregate.SessionEnrichment{Model: "claude-opus-4-7"},
	}
	d := &aggregate.Directory{Path: "/short", Sessions: []*aggregate.SessionView{short}, WorkingN: 1, TotalTokens: 12_345}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	out := Tree(tree, TreeOpts{})

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines, got:\n%s", out)
	}
	dirLine := lines[0]
	sessionLine := ""
	for _, l := range lines[1:] {
		if strings.Contains(l, "├─") || strings.Contains(l, "└─") {
			sessionLine = l
			break
		}
	}
	if sessionLine == "" {
		t.Fatalf("no session row found in:\n%s", out)
	}
	dirW := lipgloss.Width(dirLine)
	sesW := lipgloss.Width(sessionLine)
	if dirW != sesW {
		t.Errorf("dir row visual width (%d) != session row visual width (%d)\ndir:     %q\nsession: %q",
			dirW, sesW, dirLine, sessionLine)
	}
}

func TestDirRowShowsPRInfo(t *testing.T) {
	d := &aggregate.Directory{
		Path:   "/p",
		Branch: "feat/xyz",
		PRInfo: &session.PRInfo{Number: 42, Title: "Add the thing", URL: "https://github.com/owner/repo/pull/42"},
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{Name: "n", Status: session.Working}},
		},
		WorkingN: 1,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	out := Tree(tree, TreeOpts{})
	lines := strings.Split(out, "\n")
	if len(lines) == 0 {
		t.Fatal("no output")
	}
	dirLine := lines[0]
	if !strings.Contains(dirLine, "#42") {
		t.Errorf("expected '#42' in dir row, got: %q", dirLine)
	}
	if !strings.Contains(dirLine, "Add the thing") {
		t.Errorf("expected PR title in dir row, got: %q", dirLine)
	}
}

func TestDirRowNoPRWhenNil(t *testing.T) {
	d := &aggregate.Directory{
		Path:   "/p",
		Branch: "feat/xyz",
		PRInfo: nil,
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{Name: "n", Status: session.Working}},
		},
		WorkingN: 1,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	out := Tree(tree, TreeOpts{})
	if strings.Contains(out, "#") {
		t.Errorf("expected no PR in dir row when PRInfo is nil, got: %q", out)
	}
}

func TestDirRowPRTitleTruncated(t *testing.T) {
	longTitle := strings.Repeat("x", 60)
	d := &aggregate.Directory{
		Path:   "/p",
		Branch: "b",
		PRInfo: &session.PRInfo{Number: 1, Title: longTitle, URL: "u"},
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{Name: "n", Status: session.Working}},
		},
		WorkingN: 1,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	out := Tree(tree, TreeOpts{})
	if strings.Contains(out, longTitle) {
		t.Errorf("expected title truncated, but full title appeared in: %q", out)
	}
}

func TestTreeStatsAreRightAligned(t *testing.T) {
	// Two sessions with labels of different widths. The right edge of each row
	// (ignoring any optional tail icons) must line up because the stats columns
	// are fixed-width and right-aligned.
	short := &aggregate.SessionView{
		Session:           &session.Session{Name: "a", SessionID: "id-a", Status: session.Working},
		SessionEnrichment: aggregate.SessionEnrichment{Model: "claude-opus-4-7", ContextTokens: 10_000, BurnRateShort: 25_000},
	}
	long := &aggregate.SessionView{
		Session:           &session.Session{Name: "longer-label-name", SessionID: "id-b", Status: session.Working},
		SessionEnrichment: aggregate.SessionEnrichment{Model: "claude-sonnet-4-6", ContextTokens: 500, BurnRateShort: 1_000},
	}
	d := &aggregate.Directory{Path: "/p", Sessions: []*aggregate.SessionView{short, long}, WorkingN: 2}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	out := Tree(tree, TreeOpts{})

	rows := sessionRows(out)
	if len(rows) != 2 {
		t.Fatalf("expected 2 session rows, got %d:\n%s", len(rows), out)
	}
	// The stats suffix ("k/m ..." — burn column and beyond, trimmed) must end
	// at the same horizontal column for both rows: i.e. equal-length rows.
	if len(rows[0]) != len(rows[1]) {
		t.Errorf("stats not right-aligned: row widths differ (%d vs %d)\nrow0=%q\nrow1=%q",
			len(rows[0]), len(rows[1]), rows[0], rows[1])
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
	// depth=1 row should have more leading whitespace between the glyph and the
	// display path than depth=0. The label is formatted as glyph + " " + indent + displayPath.
	// We find the glyph marker and measure the space between glyph and the path text.
	glyphSep := "▼ "
	_, after0, found0 := strings.Cut(out0, glyphSep)
	_, after1, found1 := strings.Cut(out1, glyphSep)
	if !found0 || !found1 {
		t.Fatalf("could not find glyph separator in output: depth0=%q depth1=%q", out0, out1)
	}
	// Count leading spaces (indentation) before the display path text.
	trimmed0 := strings.TrimLeft(after0, " ")
	trimmed1 := strings.TrimLeft(after1, " ")
	indent0 := len(after0) - len(trimmed0)
	indent1 := len(after1) - len(trimmed1)
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
	if !strings.Contains(out, "tok") {
		t.Errorf("expected 'tok' in rollup, got: %q", out)
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
