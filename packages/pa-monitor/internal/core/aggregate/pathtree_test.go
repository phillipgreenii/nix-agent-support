package aggregate

import (
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

func TestBuildPathTreeEmpty(t *testing.T) {
	if nodes := BuildPathTree(nil); len(nodes) != 0 {
		t.Errorf("want 0 nodes for nil dirs, got %d", len(nodes))
	}
	if nodes := BuildPathTree([]*Directory{}); len(nodes) != 0 {
		t.Errorf("want 0 nodes for empty dirs, got %d", len(nodes))
	}
}

// TestBuildPathTreeCarriesBranchAndPRInfo pins F2: the compressed PathNode must
// carry the directory's Branch and PRInfo so the TUI can render the branch and
// the clickable PR link (both were dropped before Phase 4).
func TestBuildPathTreeCarriesBranchAndPRInfo(t *testing.T) {
	pr := &session.PRInfo{Number: 42, State: "OPEN", URL: "https://example.com/pull/42"}
	d := &Directory{
		Path:     "/home/user/proj",
		Branch:   "feat/x",
		PRInfo:   pr,
		Sessions: []*SessionView{{Session: &session.Session{Status: session.Working}}},
	}
	nodes := BuildPathTree([]*Directory{d})
	if len(nodes) != 1 {
		t.Fatalf("want 1 node, got %d", len(nodes))
	}
	n := nodes[0]
	if n.Branch != "feat/x" {
		t.Errorf("PathNode.Branch = %q, want feat/x", n.Branch)
	}
	if n.PRInfo != pr {
		t.Errorf("PathNode.PRInfo = %v, want the directory's PRInfo pointer", n.PRInfo)
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

// TestBuildPathTreeEmptyParentRollupRecursion covers computeRollup's
// child-recursion when the PARENT node has NO direct sessions of its own. A
// branch point kept because it has 2+ children (see TestBuildPathTreeBranchPointKept)
// must still aggregate the full working/blocked/idle counts and token/cost/burn
// totals from its descendants. TestBuildPathTreeRollupStats only exercises a
// parent that ALSO has a direct session, so the pure empty-parent recursion —
// every rollup value sourced solely from the child loop — was untested.
func TestBuildPathTreeEmptyParentRollupRecursion(t *testing.T) {
	b1 := &Directory{
		Path: "/a/b1",
		Sessions: []*SessionView{{
			Session:           &session.Session{Status: session.Working},
			SessionEnrichment: SessionEnrichment{SessionTokens: 100, CostUSD: 0.5, BurnRateShort: 10},
		}},
	}
	b2 := &Directory{
		Path: "/a/b2",
		Sessions: []*SessionView{
			{
				Session:           &session.Session{Status: session.Idle},
				SessionEnrichment: SessionEnrichment{SessionTokens: 200, CostUSD: 1.0, BurnRateShort: 20},
			},
			{
				Session:           &session.Session{Status: session.Blocked},
				SessionEnrichment: SessionEnrichment{SessionTokens: 300, CostUSD: 2.0, BurnRateShort: 30},
			},
		},
	}
	nodes := BuildPathTree([]*Directory{b1, b2})
	if len(nodes) != 1 {
		t.Fatalf("want 1 root (branch /a), got %d", len(nodes))
	}
	root := nodes[0]
	if root.FullPath != "/a" || len(root.DirectSessions) != 0 {
		t.Fatalf("want empty branch parent /a with no direct sessions, got path=%q direct=%d",
			root.FullPath, len(root.DirectSessions))
	}
	if len(root.Children) != 2 {
		t.Fatalf("want 2 children under the empty parent, got %d", len(root.Children))
	}
	// Every value below is produced purely by the child-recursion branch of
	// computeRollup, since the parent contributes no direct sessions.
	if root.WorkingN != 1 || root.IdleN != 1 || root.BlockedN != 1 {
		t.Errorf("empty-parent rollup counts: want working/idle/blocked = 1/1/1, got %d/%d/%d",
			root.WorkingN, root.IdleN, root.BlockedN)
	}
	if root.TotalTokens != 600 {
		t.Errorf("empty-parent rollup tokens: want 600, got %d", root.TotalTokens)
	}
	if root.TotalCostUSD != 3.5 {
		t.Errorf("empty-parent rollup cost: want 3.5, got %v", root.TotalCostUSD)
	}
	if root.BurnRateSum != 60 {
		t.Errorf("empty-parent rollup burnrate: want 60, got %v", root.BurnRateSum)
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
