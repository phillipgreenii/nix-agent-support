package render

import (
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/session"
	"github.com/phillipgreenii/claude-agents-tui/internal/treestate"
)

// nSessions builds a tree with one dir "/p" containing n working sessions.
func nSessions(n int) *aggregate.Tree {
	d := &aggregate.Directory{Path: "/p"}
	for i := range n {
		d.Sessions = append(d.Sessions, &aggregate.SessionView{
			Session: &session.Session{
				SessionID: fmt.Sprintf("id-%d", i),
				Status:    session.Working,
			},
		})
		d.WorkingN++
	}
	return &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
}

func TestLastVisibleIdxAllFit(t *testing.T) {
	rows := []Row{
		{Kind: SessionKind, LineCount: 1},
		{Kind: SessionKind, LineCount: 1},
		{Kind: SessionKind, LineCount: 1},
	}
	if got := LastVisibleIdx(rows, 0, 10); got != 2 {
		t.Errorf("want 2, got %d", got)
	}
}

func TestLastVisibleIdxNoneFit(t *testing.T) {
	rows := []Row{{Kind: SessionKind, LineCount: 2}}
	if got := LastVisibleIdx(rows, 0, 1); got != -1 {
		t.Errorf("want -1, got %d", got)
	}
}

func TestLastVisibleIdxPartialFit(t *testing.T) {
	rows := []Row{
		{Kind: SessionKind, LineCount: 1},
		{Kind: SessionKind, LineCount: 1},
		{Kind: SessionKind, LineCount: 1},
	}
	if got := LastVisibleIdx(rows, 0, 2); got != 1 {
		t.Errorf("want 1, got %d", got)
	}
}

func TestLastVisibleIdxWithOffset(t *testing.T) {
	rows := []Row{
		{Kind: DirHeaderKind, LineCount: 1},
		{Kind: SessionKind, LineCount: 1},
		{Kind: SessionKind, LineCount: 1},
	}
	// offset=1, budget=1 → only rows[1] fits
	if got := LastVisibleIdx(rows, 1, 1); got != 1 {
		t.Errorf("want 1, got %d", got)
	}
}

func TestRenderWindowNoOverflow(t *testing.T) {
	tree := nSessions(3)
	rows := FlattenRows(tree, TreeOpts{})
	out := RenderWindow(tree, rows, 0, 20, TreeOpts{})
	if strings.Contains(out, "↑") {
		t.Error("no top indicator expected at offset 0 with large budget")
	}
	if strings.Contains(out, "↓") {
		t.Error("no bottom indicator expected when all rows fit")
	}
	if !strings.Contains(out, "id-0") || !strings.Contains(out, "id-2") {
		t.Errorf("expected all sessions visible, got:\n%s", out)
	}
}

func TestRenderWindowBottomIndicator(t *testing.T) {
	tree := nSessions(10)
	rows := FlattenRows(tree, TreeOpts{})
	out := RenderWindow(tree, rows, 0, 4, TreeOpts{})
	if !strings.Contains(out, "↓") {
		t.Errorf("expected bottom indicator when sessions exceed budget, got:\n%s", out)
	}
	if strings.Contains(out, "↑") {
		t.Error("no top indicator expected at offset 0")
	}
}

func TestRenderWindowTopIndicator(t *testing.T) {
	tree := nSessions(10)
	rows := FlattenRows(tree, TreeOpts{})
	out := RenderWindow(tree, rows, 3, 20, TreeOpts{})
	if !strings.Contains(out, "↑") {
		t.Errorf("expected top indicator at offset 3, got:\n%s", out)
	}
}

func TestRenderWindowBothIndicators(t *testing.T) {
	tree := nSessions(15)
	rows := FlattenRows(tree, TreeOpts{})
	out := RenderWindow(tree, rows, 5, 5, TreeOpts{})
	if !strings.Contains(out, "↑") {
		t.Errorf("expected top indicator, got:\n%s", out)
	}
	if !strings.Contains(out, "↓") {
		t.Errorf("expected bottom indicator, got:\n%s", out)
	}
}

func TestRenderWindowStickyDirHeader(t *testing.T) {
	// One dir with 8 sessions; rows[0]=DirHeader, rows[1..8]=Sessions, rows[9]=Blank
	// scrollOffset=2 puts the dir header above the window.
	tree := nSessions(8)
	rows := FlattenRows(tree, TreeOpts{})
	out := RenderWindow(tree, rows, 2, 6, TreeOpts{})
	// Dir path "/p" must appear (pinned header) even though it is above the offset.
	if !strings.Contains(out, "/p") {
		t.Errorf("expected sticky dir header '/p', got:\n%s", out)
	}
}

func TestRenderWindowNoStickyWhenHeaderInWindow(t *testing.T) {
	tree := nSessions(4)
	rows := FlattenRows(tree, TreeOpts{})
	// scrollOffset=0: dir header is naturally in the window.
	// The dir path should appear exactly once.
	out := RenderWindow(tree, rows, 0, 20, TreeOpts{})
	if strings.Count(out, "/p") != 1 {
		t.Errorf("expected dir header exactly once, got:\n%s", out)
	}
}

func TestRenderWindowIndicatorSessionCount(t *testing.T) {
	// rows: [0]=DirHeader, [1]=Session(FlatIdx=0), [2]=Session(FlatIdx=1),
	//       [3]=Session(FlatIdx=2), [4]=Session(FlatIdx=3), [5]=Session(FlatIdx=4), [6]=Blank
	// scrollOffset=3 → sessions above = rows[1] and rows[2] → 2 sessions
	tree := nSessions(5)
	rows := FlattenRows(tree, TreeOpts{})
	out := RenderWindow(tree, rows, 3, 6, TreeOpts{})
	if !strings.Contains(out, "↑ 2 sessions") {
		t.Errorf("expected '↑ 2 sessions', got:\n%s", out)
	}
}

func TestRenderWindowSingularSession(t *testing.T) {
	// 2 sessions; scrollOffset=2 → 1 session above → "↑ 1 session" (not "sessions")
	tree := nSessions(2)
	rows := FlattenRows(tree, TreeOpts{})
	// rows: [0]=DirHeader, [1]=Session(FlatIdx=0), [2]=Session(FlatIdx=1), [3]=Blank
	// scrollOffset=2: 1 session above (rows[1])
	out := RenderWindow(tree, rows, 2, 6, TreeOpts{})
	if !strings.Contains(out, "↑ 1 session") || strings.Contains(out, "↑ 1 sessions") {
		t.Errorf("expected '↑ 1 session' (singular), got:\n%s", out)
	}
}

func TestRenderWindowEmpty(t *testing.T) {
	out := RenderWindow(&aggregate.Tree{}, []Row{}, 0, 20, TreeOpts{})
	if out != "" {
		t.Errorf("expected empty output for empty rows, got: %q", out)
	}
}

func TestRenderWindowZeroHeight(t *testing.T) {
	tree := nSessions(3)
	rows := FlattenRows(tree, TreeOpts{})
	out := RenderWindow(tree, rows, 0, 0, TreeOpts{})
	if out != "" {
		t.Errorf("expected empty output for zero bodyHeight, got: %q", out)
	}
}

func TestRenderWindowCursorSelected(t *testing.T) {
	tree := nSessions(3)
	rows := FlattenRows(tree, TreeOpts{})
	// Cursor=1 (second session), HasCursor=true
	out := RenderWindow(tree, rows, 0, 20, TreeOpts{HasCursor: true, Cursor: 1})
	lines := strings.Split(out, "\n")
	var sessionLines []string
	for _, l := range lines {
		if strings.Contains(l, "├─") || strings.Contains(l, "└─") {
			sessionLines = append(sessionLines, l)
		}
	}
	if len(sessionLines) < 2 {
		t.Fatalf("expected session lines, got:\n%s", out)
	}
	if !strings.HasPrefix(sessionLines[1], "> ") {
		t.Errorf("cursor=1: second session should start with '> ', got %q", sessionLines[1])
	}
}

func treeNodes(paths ...string) []*aggregate.PathNode {
	var nodes []*aggregate.PathNode
	for _, p := range paths {
		nodes = append(nodes, &aggregate.PathNode{
			FullPath:    p,
			DisplayPath: p,
			DirectSessions: []*aggregate.SessionView{
				{Session: &session.Session{SessionID: p, Status: session.Working}},
			},
			WorkingN: 1,
		})
	}
	return nodes
}

func TestRenderWindowTreeEmpty(t *testing.T) {
	out := RenderWindowTree(nil, nil, 0, 20, TreeOpts{})
	if out != "" {
		t.Errorf("empty tree: want empty output, got %q", out)
	}
}

func TestRenderWindowTreeRendersPathNode(t *testing.T) {
	nodes := treeNodes("/p")
	state := treestate.NewState()
	rows := FlattenPathTree(nodes, state, TreeOpts{})
	out := RenderWindowTree(nodes, rows, 0, 20, TreeOpts{})
	if !strings.Contains(out, "/p") {
		t.Errorf("expected path in output, got:\n%s", out)
	}
	if !strings.Contains(out, "▼") {
		t.Errorf("expected expanded glyph ▼, got:\n%s", out)
	}
}

func TestRenderWindowTreeCollapsedNodeHidesSession(t *testing.T) {
	nodes := treeNodes("/p")
	state := treestate.NewState()
	state.Toggle("/p")
	rows := FlattenPathTree(nodes, state, TreeOpts{})
	out := RenderWindowTree(nodes, rows, 0, 20, TreeOpts{})
	if !strings.Contains(out, "▶") {
		t.Errorf("expected collapsed glyph ▶, got:\n%s", out)
	}
	if strings.Contains(out, "├─") || strings.Contains(out, "└─") {
		t.Errorf("collapsed node should not render session connectors, got:\n%s", out)
	}
}

func TestRenderWindowTreeCursorOnPathNode(t *testing.T) {
	nodes := treeNodes("/p")
	state := treestate.NewState()
	rows := FlattenPathTree(nodes, state, TreeOpts{})
	// rows[0] = PathNodeKind; cursor=0 should select it
	out := RenderWindowTree(nodes, rows, 0, 20, TreeOpts{HasCursor: true, Cursor: 0})
	lines := strings.Split(out, "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "> ") {
		t.Errorf("cursor=0 on path node should start with '> ', got:\n%s", out)
	}
}

func TestRenderWindowTreeScrollIndicators(t *testing.T) {
	nodes := treeNodes("/a", "/b", "/c", "/d", "/e", "/f", "/g", "/h", "/i", "/j")
	state := treestate.NewState()
	rows := FlattenPathTree(nodes, state, TreeOpts{})
	out := RenderWindowTree(nodes, rows, 0, 4, TreeOpts{})
	if !strings.Contains(out, "↓") {
		t.Errorf("expected bottom indicator, got:\n%s", out)
	}
	out2 := RenderWindowTree(nodes, rows, 5, 20, TreeOpts{})
	if !strings.Contains(out2, "↑") {
		t.Errorf("expected top indicator at offset 5, got:\n%s", out2)
	}
}
