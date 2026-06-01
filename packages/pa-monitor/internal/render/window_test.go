package render

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/treestate"
)

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
		{Kind: PathNodeKind, LineCount: 1},
		{Kind: SessionKind, LineCount: 1},
		{Kind: SessionKind, LineCount: 1},
	}
	// offset=1, budget=1 → only rows[1] fits
	if got := LastVisibleIdx(rows, 1, 1); got != 1 {
		t.Errorf("want 1, got %d", got)
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
