package render

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/session"
)

// tailRegex matches the optional " <count>🤖" / " <count>🐚" tails appended to
// session rows. Used by alignment tests to strip tails before measuring the
// burn-column right edge.
var tailRegex = regexp.MustCompile(` \d+(🤖|🐚)`)

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

// TestTreeColumnAlignment asserts that all stats-bearing rows in the tree
// agree on the right edge of each of the five stat columns.
func TestTreeColumnAlignment(t *testing.T) {
	d := &aggregate.Directory{
		Path:        "/p",
		TotalTokens: 600,
		BurnRateSum: 30,
		Sessions: []*aggregate.SessionView{
			{
				Session: &session.Session{Name: "a", SessionID: "id-a", Status: session.Working},
				SessionEnrichment: aggregate.SessionEnrichment{
					Model: "claude-opus-4-7", SessionTokens: 200, BurnRateShort: 10,
				},
			},
			{
				Session: &session.Session{Name: "b", SessionID: "id-b", Status: session.Working},
				SessionEnrichment: aggregate.SessionEnrichment{
					Model: "claude-sonnet-4-6", SessionTokens: 400, BurnRateShort: 20,
				},
			},
		},
		WorkingN: 2,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	out := Tree(tree, TreeOpts{TotalSessionTokens: 1000, Width: 120})

	// Find the right edge of the burn column on every non-empty line that
	// looks like a stats row. With renderStatsBlock the line ends with the
	// burn column (modulo any trailing tail). We assert all stats rows agree.
	rows := strings.Split(strings.TrimRight(out, "\n"), "\n")
	var edges []int
	for _, r := range rows {
		// Skip continuation rows ("    ↳ ...") and blank rows.
		if r == "" || strings.Contains(r, "    ↳ ") {
			continue
		}
		// The burn column ends just before any tail glyphs (🤖/🐚) or end-of-line.
		// Strip any " <count>🤖" / " <count>🐚" tails, then measure visible width.
		core := tailRegex.ReplaceAllString(r, "")
		edges = append(edges, lipgloss.Width(core))
	}

	if len(edges) < 2 {
		t.Fatalf("expected at least 2 stats rows, got %d:\n%s", len(edges), out)
	}
	for i := 1; i < len(edges); i++ {
		if edges[i] != edges[0] {
			t.Errorf("row %d burn-column edge = %d, want %d (matching row 0); rows:\n%s", i, edges[i], edges[0], out)
		}
	}
}

// TestSessionRowOmitsFirstPromptContinuation verifies the per-session prompt
// continuation row was removed in theme D. The FirstPrompt is now shown only
// in the footer's selection-status column.
func TestSessionRowOmitsFirstPromptContinuation(t *testing.T) {
	s := &aggregate.SessionView{
		Session: &session.Session{Name: "n", SessionID: "id", Status: session.Working},
		SessionEnrichment: aggregate.SessionEnrichment{
			FirstPrompt:   "this prompt should NOT appear under the row anymore",
			SessionTokens: 100,
			Model:         "claude-opus-4-7",
		},
	}
	d := &aggregate.Directory{Path: "/p", Sessions: []*aggregate.SessionView{s}, WorkingN: 1, TotalTokens: 100}
	out := Tree(&aggregate.Tree{Dirs: []*aggregate.Directory{d}}, TreeOpts{TotalSessionTokens: 100, Width: 120})

	if strings.Contains(out, "↳") {
		t.Errorf("session row should no longer emit the ↳ continuation; got:\n%s", out)
	}
	if strings.Contains(out, "this prompt should NOT appear") {
		t.Errorf("FirstPrompt content leaked into the body:\n%s", out)
	}
}

// TestTreeRollupShowsAggregatePct verifies that the directory rollup's % column
// equals the directory's share of total session tokens.
func TestTreeRollupShowsAggregatePct(t *testing.T) {
	d := &aggregate.Directory{
		Path:        "/p",
		TotalTokens: 600,
		Sessions: []*aggregate.SessionView{
			{
				Session: &session.Session{Name: "a", SessionID: "id-a", Status: session.Working},
				SessionEnrichment: aggregate.SessionEnrichment{
					Model: "claude-opus-4-7", SessionTokens: 600,
				},
			},
		},
		WorkingN: 1,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	out := Tree(tree, TreeOpts{TotalSessionTokens: 1000})
	if !strings.Contains(out, "60%") {
		t.Errorf("expected '60%%' on rollup row (600/1000), got:\n%s", out)
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

// TestTreeTokensColumnPopulatedInTokenMode verifies that the tokens-or-cost
// column shows a token figure (e.g., "342.0k") when CostMode is false, not
// blank space.
func TestTreeTokensColumnPopulatedInTokenMode(t *testing.T) {
	d := &aggregate.Directory{
		Path:        "/p",
		TotalTokens: 342_000,
		Sessions: []*aggregate.SessionView{
			{
				Session: &session.Session{Name: "a", SessionID: "id-a", Status: session.Working},
				SessionEnrichment: aggregate.SessionEnrichment{
					Model: "claude-opus-4-7", SessionTokens: 342_000,
				},
			},
		},
		WorkingN: 1,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	out := Tree(tree, TreeOpts{TotalSessionTokens: 342_000, CostMode: false})
	if !strings.Contains(out, "342.0k") {
		t.Errorf("expected '342.0k' in tokens column, got:\n%s", out)
	}
}

// TestCountsColumnFitsAtMaxBuckets verifies that col1 accommodates the worst-case
// counts string ("99● 99○ 99✕") without lipgloss wrapping it onto a second line.
func TestCountsColumnFitsAtMaxBuckets(t *testing.T) {
	d := &aggregate.Directory{
		Path:        "/p",
		WorkingN:    99,
		IdleN:       99,
		DormantN:    99,
		TotalTokens: 1000,
		Sessions: []*aggregate.SessionView{
			{
				Session: &session.Session{Name: "a", SessionID: "id-a", Status: session.Working},
				SessionEnrichment: aggregate.SessionEnrichment{
					Model: "claude-opus-4-7", SessionTokens: 1000,
				},
			},
		},
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	out := Tree(tree, TreeOpts{ShowAll: true, TotalSessionTokens: 1000, Width: 120})

	// The dir-rollup row is the first non-empty line of output. Confirm it
	// renders as a single line (no embedded newlines from lipgloss wrapping
	// an over-budget col1).
	rows := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(rows) == 0 {
		t.Fatal("no output")
	}
	dirRow := rows[0]
	if strings.Contains(dirRow, "\n") {
		t.Errorf("dir row wrapped to multiple lines (col1 overflow):\n%s", dirRow)
	}
	// All three counts must appear on the same line.
	for _, want := range []string{"99●", "99○", "99✕"} {
		if !strings.Contains(dirRow, want) {
			t.Errorf("missing %q in dir row: %q", want, dirRow)
		}
	}
}

// TestTreeColumnAlignmentWithTails reruns the alignment invariant with
// subagent and subshell tails present, ensuring the trim logic correctly
// strips them so the burn-column right edge still matches across rows.
func TestTreeColumnAlignmentWithTails(t *testing.T) {
	d := &aggregate.Directory{
		Path:        "/p",
		TotalTokens: 600,
		BurnRateSum: 30,
		Sessions: []*aggregate.SessionView{
			{
				Session: &session.Session{Name: "a", SessionID: "id-a", Status: session.Working},
				SessionEnrichment: aggregate.SessionEnrichment{
					Model: "claude-opus-4-7", SessionTokens: 200, BurnRateShort: 10,
					SubagentCount: 2, SubshellCount: 1,
				},
			},
			{
				Session: &session.Session{Name: "b", SessionID: "id-b", Status: session.Working},
				SessionEnrichment: aggregate.SessionEnrichment{
					Model: "claude-sonnet-4-6", SessionTokens: 400, BurnRateShort: 20,
				},
			},
		},
		WorkingN: 2,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	out := Tree(tree, TreeOpts{TotalSessionTokens: 1000, Width: 120})

	rows := strings.Split(strings.TrimRight(out, "\n"), "\n")
	var edges []int
	for _, r := range rows {
		if r == "" || strings.Contains(r, "    ↳ ") {
			continue
		}
		core := tailRegex.ReplaceAllString(r, "")
		edges = append(edges, lipgloss.Width(core))
	}

	if len(edges) < 2 {
		t.Fatalf("expected at least 2 stats rows, got %d:\n%s", len(edges), out)
	}
	for i := 1; i < len(edges); i++ {
		if edges[i] != edges[0] {
			t.Errorf("row %d burn-column edge = %d, want %d (matching row 0); rows:\n%s", i, edges[i], edges[0], out)
		}
	}
}
