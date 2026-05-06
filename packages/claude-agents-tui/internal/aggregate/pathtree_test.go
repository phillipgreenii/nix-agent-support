package aggregate

import (
	"testing"

	"github.com/phillipgreenii/claude-agents-tui/internal/session"
)

func TestBuildPathTreeEmpty(t *testing.T) {
	if nodes := BuildPathTree(nil); len(nodes) != 0 {
		t.Errorf("want 0 nodes for nil dirs, got %d", len(nodes))
	}
	if nodes := BuildPathTree([]*Directory{}); len(nodes) != 0 {
		t.Errorf("want 0 nodes for empty dirs, got %d", len(nodes))
	}
}

func TestBuildPathTreeSingleDir(t *testing.T) {
	d := &Directory{
		Path:     "/home/user/proj",
		Sessions: []*SessionView{{Session: &session.Session{Status: session.Working}}},
	}
	nodes := BuildPathTree([]*Directory{d})
	if len(nodes) != 1 {
		t.Fatalf("want 1 root node, got %d", len(nodes))
	}
	n := nodes[0]
	if n.FullPath != "/home/user/proj" {
		t.Errorf("FullPath: want /home/user/proj, got %s", n.FullPath)
	}
	if n.DisplayPath != "/home/user/proj" {
		t.Errorf("DisplayPath: root should equal FullPath, got %s", n.DisplayPath)
	}
	if n.Depth != 0 {
		t.Errorf("Depth: want 0, got %d", n.Depth)
	}
	if len(n.DirectSessions) != 1 {
		t.Errorf("want 1 direct session, got %d", len(n.DirectSessions))
	}
	if len(n.Children) != 0 {
		t.Errorf("want no children, got %d", len(n.Children))
	}
}

func TestBuildPathTreeCompressesIntermediateNodes(t *testing.T) {
	d := &Directory{
		Path:     "/a/b/c",
		Sessions: []*SessionView{{Session: &session.Session{Status: session.Idle}}},
	}
	nodes := BuildPathTree([]*Directory{d})
	if len(nodes) != 1 {
		t.Fatalf("want 1 compressed root, got %d", len(nodes))
	}
	if nodes[0].FullPath != "/a/b/c" {
		t.Errorf("want compressed root /a/b/c, got %s", nodes[0].FullPath)
	}
	if len(nodes[0].Children) != 0 {
		t.Errorf("want no children, got %d", len(nodes[0].Children))
	}
}

func TestBuildPathTreeParentWithChildCompressesIntermediate(t *testing.T) {
	d1 := &Directory{
		Path:     "/mono",
		Sessions: []*SessionView{{Session: &session.Session{Status: session.Working}}},
	}
	d2 := &Directory{
		Path:     "/mono/fin/part",
		Sessions: []*SessionView{{Session: &session.Session{Status: session.Idle}}},
	}
	nodes := BuildPathTree([]*Directory{d1, d2})
	if len(nodes) != 1 {
		t.Fatalf("want 1 root, got %d", len(nodes))
	}
	root := nodes[0]
	if root.FullPath != "/mono" {
		t.Errorf("root.FullPath: want /mono, got %s", root.FullPath)
	}
	if len(root.Children) != 1 {
		t.Fatalf("want 1 child, got %d", len(root.Children))
	}
	child := root.Children[0]
	if child.FullPath != "/mono/fin/part" {
		t.Errorf("child.FullPath: want /mono/fin/part, got %s", child.FullPath)
	}
	if child.DisplayPath != "fin/part" {
		t.Errorf("child.DisplayPath: want fin/part, got %s", child.DisplayPath)
	}
	if child.Depth != 1 {
		t.Errorf("child.Depth: want 1, got %d", child.Depth)
	}
}

func TestBuildPathTreeBranchPointKept(t *testing.T) {
	d1 := &Directory{
		Path:     "/a/b1",
		Sessions: []*SessionView{{Session: &session.Session{Status: session.Working}}},
	}
	d2 := &Directory{
		Path:     "/a/b2",
		Sessions: []*SessionView{{Session: &session.Session{Status: session.Idle}}},
	}
	nodes := BuildPathTree([]*Directory{d1, d2})
	if len(nodes) != 1 {
		t.Fatalf("want 1 root (branch /a), got %d", len(nodes))
	}
	root := nodes[0]
	if root.FullPath != "/a" {
		t.Errorf("root should be branch point /a, got %s", root.FullPath)
	}
	if len(root.DirectSessions) != 0 {
		t.Error("branch point should have no direct sessions")
	}
	if len(root.Children) != 2 {
		t.Errorf("branch point should have 2 children, got %d", len(root.Children))
	}
}

func TestBuildPathTreeRollupStats(t *testing.T) {
	d1 := &Directory{
		Path: "/mono",
		Sessions: []*SessionView{{
			Session:           &session.Session{Status: session.Working},
			SessionEnrichment: SessionEnrichment{SessionTokens: 100, CostUSD: 0.5, BurnRateShort: 10},
		}},
	}
	d2 := &Directory{
		Path: "/mono/sub",
		Sessions: []*SessionView{{
			Session:           &session.Session{Status: session.Idle},
			SessionEnrichment: SessionEnrichment{SessionTokens: 200, CostUSD: 1.0, BurnRateShort: 20},
		}},
	}
	nodes := BuildPathTree([]*Directory{d1, d2})
	root := nodes[0]
	if root.WorkingN != 1 || root.IdleN != 1 {
		t.Errorf("rollup working/idle: want 1/1, got %d/%d", root.WorkingN, root.IdleN)
	}
	if root.TotalTokens != 300 {
		t.Errorf("rollup tokens: want 300, got %d", root.TotalTokens)
	}
	if root.BurnRateSum != 30 {
		t.Errorf("rollup burnrate: want 30, got %.0f", root.BurnRateSum)
	}
	child := root.Children[0]
	if child.WorkingN != 0 || child.IdleN != 1 {
		t.Errorf("child rollup: want working=0 idle=1, got %d/%d", child.WorkingN, child.IdleN)
	}
}

func TestBuildPathTreeSortedChildren(t *testing.T) {
	d1 := &Directory{Path: "/a/z", Sessions: []*SessionView{{Session: &session.Session{Status: session.Working}}}}
	d2 := &Directory{Path: "/a/a", Sessions: []*SessionView{{Session: &session.Session{Status: session.Working}}}}
	nodes := BuildPathTree([]*Directory{d1, d2})
	if len(nodes) != 1 || len(nodes[0].Children) != 2 {
		t.Fatalf("unexpected shape: %+v", nodes)
	}
	if nodes[0].Children[0].FullPath != "/a/a" {
		t.Errorf("want /a/a first, got %s", nodes[0].Children[0].FullPath)
	}
}
