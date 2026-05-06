# Collapsible Path Tree Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the flat directory-per-line session list with a collapsible path tree that compresses common ancestors, shows rollup stats, and persists collapse state across restarts.

**Architecture:** New `internal/treestate` package handles collapse persistence. New `aggregate.BuildPathTree` transforms flat `[]*Directory` into a compressed trie of `PathNode`s. The TUI model holds `treeState`, `pathNodes`, and `flatRows` rebuilt on every tree update or collapse toggle. `render.FlattenPathTree` + `render.RenderWindowTree` replace the existing `FlattenRows` + `RenderWindow` call sites in `view.go`.

**Tech Stack:** Go 1.24, bubbletea, lipgloss, encoding/json for state persistence.

---

## File Map

| Action | Path | Purpose |
|--------|------|---------|
| Create | `internal/treestate/treestate.go` | Collapse state: Load/Save/Toggle/IsCollapsed |
| Create | `internal/treestate/treestate_test.go` | Tests for treestate |
| Create | `internal/aggregate/pathtree.go` | PathNode struct + BuildPathTree |
| Create | `internal/aggregate/pathtree_test.go` | Tests for BuildPathTree |
| Modify | `internal/render/rows.go` | Add PathNodeKind, new Row fields, FlattenPathTree |
| Modify | `internal/render/rows_test.go` | Tests for FlattenPathTree |
| Modify | `internal/render/tree.go` | Add RenderPathNode + nodeRollup |
| Modify | `internal/render/tree_test.go` | Tests for RenderPathNode |
| Modify | `internal/render/window.go` | Add RenderWindowTree |
| Modify | `internal/render/window_test.go` | Tests for RenderWindowTree |
| Modify | `internal/tui/model.go` | New fields, rebuildFlatRows, rowAt, clampCursor |
| Modify | `internal/tui/model_test.go` | Tests for new model behavior |
| Modify | `internal/tui/update.go` | Space/Left/Right/h/l handlers, syncScroll rewrite |
| Modify | `internal/tui/view.go` | Switch to FlattenPathTree + RenderWindowTree |
| Modify | `cmd/claude-agents-tui/main.go` | Pass CacheDir to model Options |

---

## Task 1: `internal/treestate` package

**Files:**
- Create: `internal/treestate/treestate.go`
- Create: `internal/treestate/treestate_test.go`

- [ ] **Step 1: Write failing tests**

```go
// internal/treestate/treestate_test.go
package treestate

import (
	"os"
	"testing"
)

func TestLoadEmptyOnMissingFile(t *testing.T) {
	s := Load(t.TempDir())
	if s.IsCollapsed("/any") {
		t.Error("new state should have no collapsed paths")
	}
}

func TestLoadEmptyOnEmptyCacheDir(t *testing.T) {
	s := Load("")
	if s.IsCollapsed("/any") {
		t.Error("empty cacheDir should produce empty state")
	}
}

func TestToggleAndIsCollapsed(t *testing.T) {
	s := Load(t.TempDir())
	s.Toggle("/a")
	if !s.IsCollapsed("/a") {
		t.Error("path should be collapsed after Toggle")
	}
	s.Toggle("/a")
	if s.IsCollapsed("/a") {
		t.Error("path should be expanded after second Toggle")
	}
}

func TestSaveAndLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	s := Load(dir)
	s.Toggle("/x")
	s.Toggle("/y")
	if err := s.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}
	s2 := Load(dir)
	if !s2.IsCollapsed("/x") {
		t.Error("expected /x collapsed after reload")
	}
	if !s2.IsCollapsed("/y") {
		t.Error("expected /y collapsed after reload")
	}
}

func TestLoadInvalidJSONReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/tree-state.json", []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := Load(dir)
	if s.IsCollapsed("/any") {
		t.Error("invalid JSON should produce empty state")
	}
}

func TestSaveCreatesDir(t *testing.T) {
	dir := t.TempDir() + "/nested/dir"
	s := Load(dir)
	s.Toggle("/p")
	if err := s.Save(dir); err != nil {
		t.Fatalf("Save should create directories: %v", err)
	}
	s2 := Load(dir)
	if !s2.IsCollapsed("/p") {
		t.Error("expected /p collapsed after save into new dir")
	}
}

func TestSaveEmptyCacheDirNoOp(t *testing.T) {
	s := Load("")
	s.Toggle("/p")
	if err := s.Save(""); err != nil {
		t.Errorf("Save with empty cacheDir should be no-op, got: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go test ./internal/treestate/... 2>&1
```
Expected: FAIL (package does not exist)

- [ ] **Step 3: Write implementation**

```go
// internal/treestate/treestate.go
package treestate

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// State holds the set of collapsed path tree nodes.
// Load from disk; Toggle to flip a path; Save to persist.
type State struct {
	collapsed map[string]bool
}

type stateJSON struct {
	CollapsedPaths []string `json:"collapsedPaths"`
}

// NewState returns an empty in-memory State (for testing).
func NewState() *State {
	return &State{collapsed: make(map[string]bool)}
}

// Load reads collapse state from cacheDir/tree-state.json.
// Returns an empty State on any error or when cacheDir is "".
func Load(cacheDir string) *State {
	s := &State{collapsed: make(map[string]bool)}
	if cacheDir == "" {
		return s
	}
	data, err := os.ReadFile(filepath.Join(cacheDir, "tree-state.json"))
	if err != nil {
		return s
	}
	var raw stateJSON
	if json.Unmarshal(data, &raw) != nil {
		return s
	}
	for _, p := range raw.CollapsedPaths {
		s.collapsed[p] = true
	}
	return s
}

// IsCollapsed reports whether the given path is collapsed.
func (s *State) IsCollapsed(path string) bool {
	return s.collapsed[path]
}

// Toggle flips the collapsed state for path.
func (s *State) Toggle(path string) {
	if s.collapsed[path] {
		delete(s.collapsed, path)
	} else {
		s.collapsed[path] = true
	}
}

// Save writes collapse state to cacheDir/tree-state.json.
// No-op when cacheDir is "". Creates cacheDir if it does not exist.
func (s *State) Save(cacheDir string) error {
	if cacheDir == "" {
		return nil
	}
	raw := stateJSON{}
	for p := range s.collapsed {
		raw.CollapsedPaths = append(raw.CollapsedPaths, p)
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(cacheDir, "tree-state.json"), data, 0o644)
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go test ./internal/treestate/... -v 2>&1
```
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/internal/treestate/
git commit -m "feat(claude-agents-tui): add treestate package for collapse persistence"
```

---

## Task 2: `aggregate.PathNode` + `BuildPathTree`

**Files:**
- Create: `internal/aggregate/pathtree.go`
- Create: `internal/aggregate/pathtree_test.go`

- [ ] **Step 1: Write failing tests**

```go
// internal/aggregate/pathtree_test.go
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
	// /a has no sessions, /a/b has no sessions, /a/b/c has sessions.
	// All intermediate nodes are single-child+no-session → compressed to one root /a/b/c.
	d := &Directory{
		Path:    "/a/b/c",
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
	// /mono has sessions; /mono/fin/part has sessions; /mono/fin has none.
	// /mono/fin should compress away → child DisplayPath = "fin/part".
	d1 := &Directory{
		Path:    "/mono",
		Sessions: []*SessionView{{Session: &session.Session{Status: session.Working}}},
	}
	d2 := &Directory{
		Path:    "/mono/fin/part",
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
	// /a/b1 and /a/b2 both have sessions; /a has none and has 2 children → kept.
	d1 := &Directory{
		Path:    "/a/b1",
		Sessions: []*SessionView{{Session: &session.Session{Status: session.Working}}},
	}
	d2 := &Directory{
		Path:    "/a/b2",
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
	// Children should be sorted lexicographically by FullPath.
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
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go test ./internal/aggregate/... 2>&1 | grep -E "FAIL|pathtree"
```
Expected: build error (BuildPathTree undefined)

- [ ] **Step 3: Write implementation**

```go
// internal/aggregate/pathtree.go
package aggregate

import (
	"path/filepath"
	"sort"
	"strings"
)

// PathNode is one node in the compressed path tree built from a flat []*Directory.
// Root nodes have Depth 0; children increase by 1 per level.
type PathNode struct {
	FullPath       string
	DisplayPath    string // full path for roots; relative suffix for children
	Depth          int
	DirectSessions []*SessionView
	Children       []*PathNode
	// Rollup stats (aggregated from this node and all descendants)
	WorkingN     int
	IdleN        int
	DormantN     int
	TotalTokens  int
	TotalCostUSD float64
	BurnRateSum  float64 // sum of BurnRateShort across all descendant sessions
}

type trieNode struct {
	fullPath string
	dir      *Directory
	children map[string]*trieNode
}

func newTrieNode(fullPath string) *trieNode {
	return &trieNode{fullPath: fullPath, children: make(map[string]*trieNode)}
}

// BuildPathTree builds a compressed path trie from a flat list of directories.
// Single-child nodes with no sessions are compressed (merged with their child).
// Nodes with no sessions and 2+ children are kept as branch points.
func BuildPathTree(dirs []*Directory) []*PathNode {
	if len(dirs) == 0 {
		return nil
	}
	root := newTrieNode("")
	for _, d := range dirs {
		insertTrie(root, d)
	}
	var roots []*PathNode
	for _, child := range sortedTrieChildren(root) {
		roots = append(roots, compressAndBuild(child, "", 0)...)
	}
	for _, r := range roots {
		computeRollup(r)
	}
	return roots
}

func insertTrie(root *trieNode, d *Directory) {
	segs := strings.Split(strings.TrimPrefix(filepath.Clean(d.Path), "/"), "/")
	cur := root
	built := ""
	for _, seg := range segs {
		if seg == "" {
			continue
		}
		if built == "" {
			built = "/" + seg
		} else {
			built = built + "/" + seg
		}
		if _, ok := cur.children[seg]; !ok {
			cur.children[seg] = newTrieNode(built)
		}
		cur = cur.children[seg]
	}
	cur.dir = d
}

// compressAndBuild converts a trie subtree into PathNodes, compressing
// single-child, no-session nodes by merging them with their child.
func compressAndBuild(n *trieNode, parentFullPath string, depth int) []*PathNode {
	cur := n
	for cur.dir == nil && len(cur.children) == 1 {
		for _, c := range cur.children {
			cur = c
		}
	}

	pn := &PathNode{FullPath: cur.fullPath, Depth: depth}
	if parentFullPath == "" {
		pn.DisplayPath = cur.fullPath
	} else {
		pn.DisplayPath = strings.TrimPrefix(cur.fullPath, parentFullPath+"/")
	}
	if cur.dir != nil {
		pn.DirectSessions = cur.dir.Sessions
	}
	for _, child := range sortedTrieChildren(cur) {
		pn.Children = append(pn.Children, compressAndBuild(child, cur.fullPath, depth+1)...)
	}
	return []*PathNode{pn}
}

func computeRollup(n *PathNode) {
	for _, s := range n.DirectSessions {
		n.TotalTokens += s.SessionEnrichment.SessionTokens
		n.TotalCostUSD += s.SessionEnrichment.CostUSD
		n.BurnRateSum += s.SessionEnrichment.BurnRateShort
		switch s.Status {
		case Working:
			n.WorkingN++
		case Idle:
			n.IdleN++
		default:
			n.DormantN++
		}
	}
	for _, child := range n.Children {
		computeRollup(child)
		n.WorkingN += child.WorkingN
		n.IdleN += child.IdleN
		n.DormantN += child.DormantN
		n.TotalTokens += child.TotalTokens
		n.TotalCostUSD += child.TotalCostUSD
		n.BurnRateSum += child.BurnRateSum
	}
}

func sortedTrieChildren(n *trieNode) []*trieNode {
	result := make([]*trieNode, 0, len(n.children))
	for _, c := range n.children {
		result = append(result, c)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].fullPath < result[j].fullPath
	})
	return result
}
```

- [ ] **Step 4: Run tests**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go test ./internal/aggregate/... -v 2>&1 | tail -20
```
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/internal/aggregate/pathtree.go \
        packages/claude-agents-tui/internal/aggregate/pathtree_test.go
git commit -m "feat(claude-agents-tui): add PathNode and BuildPathTree for compressed path trie"
```

---

## Task 3: Extend `render.Row` + add `FlattenPathTree`

**Files:**
- Modify: `internal/render/rows.go`
- Modify: `internal/render/rows_test.go`

- [ ] **Step 1: Write failing tests** (append to existing rows_test.go)

```go
// Append to internal/render/rows_test.go

func TestFlattenPathTreeEmpty(t *testing.T) {
	state := treestate.NewState()
	rows := FlattenPathTree(nil, state, TreeOpts{})
	if len(rows) != 0 {
		t.Errorf("want empty rows, got %d", len(rows))
	}
}

func TestFlattenPathTreeSingleNodeExpanded(t *testing.T) {
	n := &aggregate.PathNode{
		FullPath:    "/p",
		DisplayPath: "/p",
		DirectSessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "a", Status: session.Working}},
		},
	}
	state := treestate.NewState()
	rows := FlattenPathTree([]*aggregate.PathNode{n}, state, TreeOpts{})
	// Expected: PathNodeKind, SessionKind, BlankKind
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d: %+v", len(rows), rows)
	}
	if rows[0].Kind != PathNodeKind {
		t.Errorf("rows[0] should be PathNodeKind, got %v", rows[0].Kind)
	}
	if rows[0].NodePath != "/p" {
		t.Errorf("rows[0].NodePath: want /p, got %s", rows[0].NodePath)
	}
	if rows[1].Kind != SessionKind {
		t.Errorf("rows[1] should be SessionKind, got %v", rows[1].Kind)
	}
	if rows[1].Session == nil {
		t.Error("rows[1].Session should not be nil")
	}
	if rows[2].Kind != BlankKind {
		t.Errorf("rows[2] should be BlankKind, got %v", rows[2].Kind)
	}
}

func TestFlattenPathTreeCollapsedSkipsContents(t *testing.T) {
	n := &aggregate.PathNode{
		FullPath:    "/p",
		DisplayPath: "/p",
		DirectSessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "a", Status: session.Working}},
		},
	}
	state := treestate.NewState()
	state.Toggle("/p") // collapse it
	rows := FlattenPathTree([]*aggregate.PathNode{n}, state, TreeOpts{})
	// Expected: PathNodeKind, BlankKind (sessions skipped)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows (collapsed), got %d: %+v", len(rows), rows)
	}
	if rows[0].Collapsed != true {
		t.Error("collapsed node row should have Collapsed=true")
	}
	if rows[1].Kind != BlankKind {
		t.Errorf("rows[1] should be BlankKind, got %v", rows[1].Kind)
	}
}

func TestFlattenPathTreeIsLastInGroup(t *testing.T) {
	n := &aggregate.PathNode{
		FullPath:    "/p",
		DisplayPath: "/p",
		DirectSessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "a", Status: session.Working}},
			{Session: &session.Session{SessionID: "b", Status: session.Working}},
		},
	}
	state := treestate.NewState()
	rows := FlattenPathTree([]*aggregate.PathNode{n}, state, TreeOpts{})
	var sessRows []Row
	for _, r := range rows {
		if r.Kind == SessionKind {
			sessRows = append(sessRows, r)
		}
	}
	if len(sessRows) != 2 {
		t.Fatalf("want 2 session rows, got %d", len(sessRows))
	}
	if sessRows[0].IsLastInGroup {
		t.Error("first session should not be last in group")
	}
	if !sessRows[1].IsLastInGroup {
		t.Error("second session should be last in group")
	}
}

func TestFlattenPathTreeFiltersDormant(t *testing.T) {
	n := &aggregate.PathNode{
		FullPath:    "/p",
		DisplayPath: "/p",
		DirectSessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "a", Status: session.Working}},
			{Session: &session.Session{SessionID: "b", Status: session.Dormant}},
		},
	}
	state := treestate.NewState()
	rows := FlattenPathTree([]*aggregate.PathNode{n}, state, TreeOpts{ShowAll: false})
	sess := 0
	for _, r := range rows {
		if r.Kind == SessionKind {
			sess++
		}
	}
	if sess != 1 {
		t.Errorf("dormant filtered: want 1 session row, got %d", sess)
	}
}

func TestFlattenPathTreeFlatIdxSpansNodes(t *testing.T) {
	n1 := &aggregate.PathNode{
		FullPath:    "/p1",
		DisplayPath: "/p1",
		DirectSessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "a", Status: session.Working}},
		},
	}
	n2 := &aggregate.PathNode{
		FullPath:    "/p2",
		DisplayPath: "/p2",
		DirectSessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "b", Status: session.Working}},
		},
	}
	state := treestate.NewState()
	rows := FlattenPathTree([]*aggregate.PathNode{n1, n2}, state, TreeOpts{})
	var flatIdxs []int
	for _, r := range rows {
		if r.Kind == SessionKind {
			flatIdxs = append(flatIdxs, r.FlatIdx)
		}
	}
	if len(flatIdxs) != 2 || flatIdxs[0] != 0 || flatIdxs[1] != 1 {
		t.Errorf("FlatIdx should be 0,1 across nodes; got %v", flatIdxs)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go test ./internal/render/... 2>&1 | head -20
```
Expected: compile error (PathNodeKind, FlattenPathTree undefined)

- [ ] **Step 3: Update rows.go**

Replace the entire `internal/render/rows.go` with:

```go
package render

import (
	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/treestate"
)

// RowKind identifies what kind of content a Row represents in the session list.
type RowKind int

const (
	DirHeaderKind RowKind = iota
	SessionKind
	BlankKind       // blank separator line after each directory group
	PathNodeKind    // collapsible path tree node (replaces DirHeaderKind in tree mode)
)

// Row is one logical element in the rendered session list.
type Row struct {
	Kind      RowKind
	DirIdx    int // DirHeaderKind/SessionKind (legacy): index into tree.Dirs
	SessIdx   int // SessionKind (legacy): index within dir's visible sessions
	FlatIdx   int // SessionKind: global session index matching TreeOpts.Cursor

	// Path-tree mode fields (set by FlattenPathTree)
	NodePath     string                   // PathNodeKind: full path (collapse state key)
	Depth        int                      // PathNodeKind: indent level; SessionKind: parent node depth
	Collapsed    bool                     // PathNodeKind: current collapse state
	IsLastInGroup bool                    // SessionKind: last visible session in its parent node
	Session      *aggregate.SessionView   // SessionKind: direct session pointer (path-tree mode)
	Node         *aggregate.PathNode      // PathNodeKind: direct node pointer

	LineCount int // terminal lines this row occupies (1 or 2)
}

// FlattenRows converts a Tree into an ordered slice of Rows for window rendering.
// Empty dirs (no visible sessions under the current opts) are omitted.
func FlattenRows(tree *aggregate.Tree, opts TreeOpts) []Row {
	var rows []Row
	flatIdx := 0
	for dirIdx, d := range tree.Dirs {
		visible := visibleSessions(d.Sessions, opts.ShowAll)
		if len(visible) == 0 {
			continue
		}
		rows = append(rows, Row{Kind: DirHeaderKind, DirIdx: dirIdx, LineCount: 1})
		for sessIdx, s := range visible {
			lc := 1
			if s.SessionEnrichment.FirstPrompt != "" {
				lc = 2
			}
			rows = append(rows, Row{
				Kind:      SessionKind,
				DirIdx:    dirIdx,
				SessIdx:   sessIdx,
				FlatIdx:   flatIdx,
				LineCount: lc,
			})
			flatIdx++
		}
		rows = append(rows, Row{Kind: BlankKind, DirIdx: dirIdx, LineCount: 1})
	}
	return rows
}

// FlattenPathTree converts a PathNode tree into an ordered slice of Rows.
// Collapsed nodes omit all descendant sessions and children.
// BlankKind rows separate top-level nodes.
func FlattenPathTree(nodes []*aggregate.PathNode, state *treestate.State, opts TreeOpts) []Row {
	var rows []Row
	flatIdx := 0
	var walk func(n *aggregate.PathNode)
	walk = func(n *aggregate.PathNode) {
		collapsed := state.IsCollapsed(n.FullPath)
		rows = append(rows, Row{
			Kind:      PathNodeKind,
			NodePath:  n.FullPath,
			Depth:     n.Depth,
			Collapsed: collapsed,
			Node:      n,
			LineCount: 1,
		})
		if collapsed {
			return
		}
		visible := visibleSessions(n.DirectSessions, opts.ShowAll)
		for i, s := range visible {
			lc := 1
			if s.SessionEnrichment.FirstPrompt != "" {
				lc = 2
			}
			rows = append(rows, Row{
				Kind:          SessionKind,
				Depth:         n.Depth,
				FlatIdx:       flatIdx,
				IsLastInGroup: i == len(visible)-1,
				Session:       s,
				LineCount:     lc,
			})
			flatIdx++
		}
		for _, child := range n.Children {
			walk(child)
		}
	}
	for _, n := range nodes {
		walk(n)
		rows = append(rows, Row{Kind: BlankKind, LineCount: 1})
	}
	return rows
}
```

- [ ] **Step 4: Add the treestate import to rows_test.go**

Add this import to the existing test file (prepend to import block):

```go
import (
	"testing"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/session"
	"github.com/phillipgreenii/claude-agents-tui/internal/treestate"
)
```

- [ ] **Step 5: Run tests**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go test ./internal/render/... -run TestFlattenPathTree -v 2>&1
```
Expected: all new tests PASS; existing FlattenRows tests still PASS

```bash
go test ./internal/render/... -v 2>&1 | tail -20
```

- [ ] **Step 6: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/internal/render/rows.go \
        packages/claude-agents-tui/internal/render/rows_test.go
git commit -m "feat(claude-agents-tui): add PathNodeKind and FlattenPathTree to render/rows"
```

---

## Task 4: `RenderPathNode` + `nodeRollup` in `render/tree.go`

**Files:**
- Modify: `internal/render/tree.go`
- Modify: `internal/render/tree_test.go`

- [ ] **Step 1: Write failing tests** (append to tree_test.go)

```go
// Append to internal/render/tree_test.go

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
	// depth=1 row should have more leading whitespace than depth=0
	trimmed0 := strings.TrimLeft(out0, " ")
	trimmed1 := strings.TrimLeft(out1, " ")
	indent0 := len(out0) - len(trimmed0)
	indent1 := len(out1) - len(trimmed1)
	if indent1 <= indent0 {
		t.Errorf("depth=1 should have more leading space than depth=0: depth0=%d depth1=%d", indent0, indent1)
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
		WorkingN: 2, TotalTokens: 12_345,
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
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go test ./internal/render/... -run TestRenderPathNode 2>&1
```
Expected: compile error (RenderPathNode undefined)

- [ ] **Step 3: Add RenderPathNode and nodeRollup to tree.go**

Append the following to `internal/render/tree.go` (after the last function):

```go
// RenderPathNode renders one PathNode row with collapse glyph, indentation, and rollup stats.
// selected controls the cursor mark prefix. collapsed controls the ▶/▼ glyph.
func RenderPathNode(n *aggregate.PathNode, opts TreeOpts, selected, collapsed bool) string {
	cursorMark := "  "
	if selected {
		cursorMark = opts.Theme.Cursor.Render(">") + " "
	}
	glyph := "▼"
	if collapsed {
		glyph = "▶"
	}
	indent := strings.Repeat("  ", n.Depth)
	label := glyph + " " + indent + n.DisplayPath
	rollup := nodeRollup(n, opts)

	rowWidth := prefixCols + minLabelWidth + statsBlockCols
	if opts.Width > 0 {
		rowWidth = opts.Width
	}
	// Subtract cursor mark width (2) from available space.
	available := rowWidth - 2
	leftWidth := max(available-lipgloss.Width(rollup)-1, lipgloss.Width(label))
	pathStyle := opts.Theme.DirRow.Width(leftWidth).Align(lipgloss.Left)
	return cursorMark + pathStyle.Render(label) + " " + rollup + "\n"
}

// nodeRollup formats the rollup statistics line for a PathNode row.
func nodeRollup(n *aggregate.PathNode, opts TreeOpts) string {
	var parts []string
	if n.WorkingN > 0 {
		parts = append(parts, fmt.Sprintf("%d●", n.WorkingN))
	}
	if n.IdleN > 0 {
		parts = append(parts, fmt.Sprintf("%d○", n.IdleN))
	}
	if n.DormantN > 0 && opts.ShowAll {
		parts = append(parts, fmt.Sprintf("%d✕", n.DormantN))
	}
	if opts.CostMode {
		parts = append(parts, fmt.Sprintf("$%.2f", n.TotalCostUSD))
	} else {
		parts = append(parts, fmt.Sprintf("%s tok", FmtTok(n.TotalTokens)))
	}
	parts = append(parts, fmt.Sprintf("%sk/m", fmtK(n.BurnRateSum)))
	return strings.Join(parts, "  ")
}
```

- [ ] **Step 4: Run tests**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go test ./internal/render/... -v 2>&1 | tail -30
```
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/internal/render/tree.go \
        packages/claude-agents-tui/internal/render/tree_test.go
git commit -m "feat(claude-agents-tui): add RenderPathNode and nodeRollup to render/tree"
```

---

## Task 5: `RenderWindowTree` in `render/window.go`

**Files:**
- Modify: `internal/render/window.go`
- Modify: `internal/render/window_test.go`

- [ ] **Step 1: Write failing tests** (append to window_test.go)

```go
// Append to internal/render/window_test.go

import (
	// add at top of existing import block:
	"github.com/phillipgreenii/claude-agents-tui/internal/treestate"
)

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
	// Session ID same as path name "/p"; in collapsed mode it should not appear as a session row.
	// The path "/p" appears in the node row itself, so check for ├─ / └─ instead.
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
	// 10 nodes → many rows → bottom indicator at offset 0 with small budget
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
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go test ./internal/render/... -run TestRenderWindowTree 2>&1
```
Expected: compile error (RenderWindowTree undefined)

- [ ] **Step 3: Add RenderWindowTree to window.go**

Append the following to `internal/render/window.go`:

```go
// RenderWindowTree renders a height-bounded window of rows produced by FlattenPathTree.
// PathNodeKind rows use row.Node + row.Collapsed. SessionKind rows use row.Session directly.
func RenderWindowTree(nodes []*aggregate.PathNode, rows []Row, scrollOffset, bodyHeight int, opts TreeOpts) string {
	if len(rows) == 0 || bodyHeight <= 0 {
		return ""
	}

	budget := bodyHeight

	topInd := scrollOffset > 0
	if topInd {
		budget--
	}

	lastVis := LastVisibleIdx(rows, scrollOffset, budget)
	botInd := lastVis < len(rows)-1
	if botInd {
		budget--
		lastVis = LastVisibleIdx(rows, scrollOffset, budget)
	}

	var sb strings.Builder

	if topInd {
		n := countSessionRows(rows, 0, scrollOffset)
		sb.WriteString(opts.Theme.Prompt.Render(fmt.Sprintf("  ↑ %s", pluralSession(n))))
		sb.WriteString("\n")
	}

	for i := scrollOffset; i <= lastVis; i++ {
		row := rows[i]
		switch row.Kind {
		case PathNodeKind:
			selected := opts.HasCursor && i == opts.Cursor
			sb.WriteString(RenderPathNode(row.Node, opts, selected, row.Collapsed))
		case BlankKind:
			sb.WriteString("\n")
		case SessionKind:
			prefix, cont := "├─", "│"
			if row.IsLastInGroup {
				prefix, cont = "└─", " "
			}
			indent := strings.Repeat("  ", row.Depth)
			selected := opts.HasCursor && i == opts.Cursor
			sb.WriteString(renderSession(row.Session, opts, indent+prefix, cont, selected))
		}
	}

	if botInd {
		n := countSessionRows(rows, lastVis+1, len(rows))
		sb.WriteString(opts.Theme.Prompt.Render(fmt.Sprintf("  ↓ %s", pluralSession(n))))
		sb.WriteString("\n")
	}

	return sb.String()
}
```

- [ ] **Step 4: Add missing imports to window.go** (ensure `strings`, `fmt`, `aggregate` are imported — they already are in the existing file; `strings` may need to be added):

Check existing imports in window.go:
```go
import (
	"fmt"
	"strings"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
)
```
`strings` is already used for `strings.Builder` in `RenderWindowTree`. The existing `window.go` already imports `fmt` and `aggregate`. Add `"strings"` if not present.

- [ ] **Step 5: Run tests**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go test ./internal/render/... -v 2>&1 | tail -30
```
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/internal/render/window.go \
        packages/claude-agents-tui/internal/render/window_test.go
git commit -m "feat(claude-agents-tui): add RenderWindowTree for path-tree windowed rendering"
```

---

## Task 6: Update `internal/tui/model.go`

**Files:**
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/model_test.go`

- [ ] **Step 1: Write failing tests** (append to model_test.go)

```go
// Append to internal/tui/model_test.go

func TestRebuildFlatRowsBuildsOnPollResult(t *testing.T) {
	d := &aggregate.Directory{
		Path:     "/p",
		Sessions: []*aggregate.SessionView{{Session: &session.Session{SessionID: "a", Status: session.Working}}},
		WorkingN: 1,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	m := NewModel(Options{Tree: &aggregate.Tree{}})
	m.Update(pollResultMsg{tree: tree})
	if len(m.flatRows) == 0 {
		t.Error("flatRows should be populated after pollResultMsg")
	}
}

func TestClampCursorUsesAllRows(t *testing.T) {
	d := &aggregate.Directory{
		Path: "/p",
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "a", Status: session.Working}},
		},
		WorkingN: 1,
	}
	m := NewModel(Options{Tree: &aggregate.Tree{Dirs: []*aggregate.Directory{d}}})
	m.cursor = 999
	m.clampCursor()
	if m.cursor >= len(m.flatRows) {
		t.Errorf("cursor should be clamped to flatRows length, got %d (len=%d)", m.cursor, len(m.flatRows))
	}
}

func TestRowAtReturnsCorrectRow(t *testing.T) {
	d := &aggregate.Directory{
		Path: "/p",
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "s1", Status: session.Working}},
		},
		WorkingN: 1,
	}
	m := NewModel(Options{Tree: &aggregate.Tree{Dirs: []*aggregate.Directory{d}}})
	// flatRows: PathNodeKind(0), SessionKind(1), BlankKind(2)
	row, ok := m.rowAt(0)
	if !ok {
		t.Fatal("rowAt(0) should return a row")
	}
	if row.Kind != render.PathNodeKind {
		t.Errorf("rows[0] should be PathNodeKind, got %v", row.Kind)
	}
	if _, ok := m.rowAt(999); ok {
		t.Error("rowAt out of bounds should return ok=false")
	}
}

func TestEnterOnSessionOpensDetail(t *testing.T) {
	sess := &session.Session{SessionID: "s1", Status: session.Working}
	d := &aggregate.Directory{
		Path:     "/p",
		Sessions: []*aggregate.SessionView{{Session: sess}},
		WorkingN: 1,
	}
	m := NewModel(Options{Tree: &aggregate.Tree{Dirs: []*aggregate.Directory{d}}})
	// Navigate to the session row (index 1 after PathNodeKind).
	m.cursor = 1
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.selected == nil {
		t.Error("Enter on session row should open detail view")
	}
}

func TestEnterOnPathNodeIsNoOp(t *testing.T) {
	d := &aggregate.Directory{
		Path:     "/p",
		Sessions: []*aggregate.SessionView{{Session: &session.Session{SessionID: "s1", Status: session.Working}}},
		WorkingN: 1,
	}
	m := NewModel(Options{Tree: &aggregate.Tree{Dirs: []*aggregate.Directory{d}}})
	m.cursor = 0 // PathNodeKind row
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.selected != nil {
		t.Error("Enter on path node should not open detail view")
	}
}

func TestSpaceTogglesCollapse(t *testing.T) {
	d := &aggregate.Directory{
		Path:     "/p",
		Sessions: []*aggregate.SessionView{{Session: &session.Session{SessionID: "s1", Status: session.Working}}},
		WorkingN: 1,
	}
	m := NewModel(Options{Tree: &aggregate.Tree{Dirs: []*aggregate.Directory{d}}})
	rowsBefore := len(m.flatRows)
	m.cursor = 0 // PathNodeKind
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	// After collapse: session row and blank omitted → fewer rows
	if len(m.flatRows) >= rowsBefore {
		t.Errorf("Space on path node should collapse it (fewer rows): before=%d after=%d", rowsBefore, len(m.flatRows))
	}
	// Second Space expands again
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	if len(m.flatRows) != rowsBefore {
		t.Errorf("Second Space should expand back to original row count: want=%d got=%d", rowsBefore, len(m.flatRows))
	}
}

func TestLeftCollapsesPathNode(t *testing.T) {
	d := &aggregate.Directory{
		Path:     "/p",
		Sessions: []*aggregate.SessionView{{Session: &session.Session{SessionID: "s1", Status: session.Working}}},
		WorkingN: 1,
	}
	m := NewModel(Options{Tree: &aggregate.Tree{Dirs: []*aggregate.Directory{d}}})
	rowsBefore := len(m.flatRows)
	m.cursor = 0
	m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if len(m.flatRows) >= rowsBefore {
		t.Errorf("Left on expanded path node should collapse it")
	}
	// Second Left on already-collapsed node: no change
	countAfterFirst := len(m.flatRows)
	m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if len(m.flatRows) != countAfterFirst {
		t.Error("Left on already-collapsed node should be no-op")
	}
}

func TestRightExpandsPathNode(t *testing.T) {
	d := &aggregate.Directory{
		Path:     "/p",
		Sessions: []*aggregate.SessionView{{Session: &session.Session{SessionID: "s1", Status: session.Working}}},
		WorkingN: 1,
	}
	m := NewModel(Options{Tree: &aggregate.Tree{Dirs: []*aggregate.Directory{d}}})
	m.cursor = 0
	// Collapse first
	m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	collapsed := len(m.flatRows)
	// Right should expand
	m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if len(m.flatRows) <= collapsed {
		t.Errorf("Right on collapsed path node should expand it: collapsed=%d after=%d", collapsed, len(m.flatRows))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go test ./internal/tui/... 2>&1 | head -20
```
Expected: compile errors (flatRows, rowAt, PathNodeKind undefined)

- [ ] **Step 3: Rewrite model.go**

```go
// internal/tui/model.go
package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/caffeinate"
	"github.com/phillipgreenii/claude-agents-tui/internal/render"
	"github.com/phillipgreenii/claude-agents-tui/internal/treestate"
)

type Options struct {
	Tree       *aggregate.Tree
	Poller     Poller
	Interval   time.Duration
	Caffeinate *caffeinate.Manager
	CacheDir   string // used to load/save tree collapse state
}

type Model struct {
	tree          *aggregate.Tree
	showAll       bool
	forceID       bool
	costMode      bool
	caffeinateOn  bool
	width, height int
	selected      *aggregate.SessionView
	cursor        int
	scrollOffset  int
	theme         render.Theme

	poller     Poller
	interval   time.Duration
	caffeinate *caffeinate.Manager
	lastErr    error
	anyWorking bool
	polling    bool

	cacheDir  string
	treeState *treestate.State
	pathNodes []*aggregate.PathNode
	flatRows  []render.Row
}

func NewModel(o Options) *Model {
	m := &Model{
		tree:       o.Tree,
		poller:     o.Poller,
		interval:   o.Interval,
		caffeinate: o.Caffeinate,
		theme:      render.NewTheme(render.DetectColors()),
		cacheDir:   o.CacheDir,
		treeState:  treestate.Load(o.CacheDir),
	}
	m.rebuildFlatRows()
	return m
}

func (m *Model) Init() tea.Cmd {
	if m.poller == nil || m.interval <= 0 {
		return nil
	}
	m.polling = true
	return tea.Batch(m.pollNow(), tickCmd(m.interval))
}

// rebuildFlatRows rebuilds pathNodes and flatRows from the current tree and treeState.
// Must be called after m.tree or m.treeState changes.
func (m *Model) rebuildFlatRows() {
	if m.tree == nil {
		m.pathNodes = nil
		m.flatRows = nil
		return
	}
	opts := render.TreeOpts{ShowAll: m.showAll}
	m.pathNodes = aggregate.BuildPathTree(m.tree.Dirs)
	m.flatRows = render.FlattenPathTree(m.pathNodes, m.treeState, opts)
}

// rowAt returns the Row at index idx in flatRows, and whether it exists.
func (m *Model) rowAt(idx int) (render.Row, bool) {
	if idx < 0 || idx >= len(m.flatRows) {
		return render.Row{}, false
	}
	return m.flatRows[idx], true
}

func (m *Model) clampCursor() {
	n := len(m.flatRows)
	if n == 0 {
		m.cursor = 0
		return
	}
	if m.cursor >= n {
		m.cursor = n - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}
```

- [ ] **Step 4: Add the treestate import to model_test.go** 

Add to existing import block:
```go
"github.com/phillipgreenii/claude-agents-tui/internal/render"
"github.com/phillipgreenii/claude-agents-tui/internal/session"
```
(session is already imported in existing tests)

- [ ] **Step 5: Run tests**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go test ./internal/tui/... 2>&1 | head -30
```
Expected: some failures for update.go changes not yet made; model.go shape tests should pass

- [ ] **Step 6: Commit model.go changes**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/internal/tui/model.go \
        packages/claude-agents-tui/internal/tui/model_test.go
git commit -m "feat(claude-agents-tui): update model with flatRows, treeState, rebuildFlatRows, rowAt"
```

---

## Task 7: Update `internal/tui/update.go`

**Files:**
- Modify: `internal/tui/update.go`

- [ ] **Step 1: Rewrite update.go**

```go
// internal/tui/update.go
package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/render"
)

type TreeUpdatedMsg struct{ Tree *aggregate.Tree }

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if isQuit(msg) {
			return m, tea.Quit
		}
		switch msg.String() {
		case "t":
			m.costMode = !m.costMode
		case "a":
			m.showAll = !m.showAll
			m.rebuildFlatRows()
			m.clampCursor()
			m.syncScroll()
		case "n":
			m.forceID = !m.forceID
		case "C":
			m.caffeinateOn = !m.caffeinateOn
		case "down", "j":
			m.cursor++
			m.clampCursor()
			m.syncScroll()
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
			m.syncScroll()
		case " ":
			if row, ok := m.rowAt(m.cursor); ok && row.Kind == render.PathNodeKind {
				m.treeState.Toggle(row.NodePath)
				if m.cacheDir != "" {
					_ = m.treeState.Save(m.cacheDir)
				}
				m.rebuildFlatRows()
				m.clampCursor()
				m.syncScroll()
			}
		case "left", "h":
			if row, ok := m.rowAt(m.cursor); ok && row.Kind == render.PathNodeKind && !row.Collapsed {
				m.treeState.Toggle(row.NodePath)
				if m.cacheDir != "" {
					_ = m.treeState.Save(m.cacheDir)
				}
				m.rebuildFlatRows()
				m.clampCursor()
				m.syncScroll()
			}
		case "right", "l":
			if row, ok := m.rowAt(m.cursor); ok && row.Kind == render.PathNodeKind && row.Collapsed {
				m.treeState.Toggle(row.NodePath)
				if m.cacheDir != "" {
					_ = m.treeState.Save(m.cacheDir)
				}
				m.rebuildFlatRows()
				m.clampCursor()
				m.syncScroll()
			}
		case "enter":
			if row, ok := m.rowAt(m.cursor); ok && row.Kind == render.SessionKind {
				m.selected = row.Session
			}
			// PathNodeKind → no-op
		case "esc":
			m.selected = nil
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case pollTickMsg:
		if m.polling {
			return m, tickCmd(m.interval)
		}
		m.polling = true
		return m, tea.Batch(m.pollNow(), tickCmd(m.interval))
	case pollResultMsg:
		m.polling = false
		m.tree = msg.tree
		m.anyWorking = msg.anyWorking
		if m.caffeinate != nil {
			m.caffeinate.SetToggle(m.caffeinateOn)
			m.caffeinate.Tick(msg.anyWorking)
		}
		m.rebuildFlatRows()
		m.clampCursor()
		m.syncScroll()
		if m.tree.CCUsageProbed && m.tree.ActiveBlock == nil && m.tree.CCUsageErr == nil {
			m.cursor = 0
			m.selected = nil
			m.scrollOffset = 0
		}
	case pollErrMsg:
		m.polling = false
		m.lastErr = msg.err
	case TreeUpdatedMsg:
		m.tree = msg.Tree
		m.rebuildFlatRows()
		m.clampCursor()
		m.syncScroll()
	}
	return m, nil
}

// syncScroll adjusts scrollOffset so the cursor row is within the visible window.
func (m *Model) syncScroll() {
	if m.height == 0 || len(m.flatRows) == 0 {
		return
	}
	bodyHeight := max(m.height-8, 3)
	cursorIdx := m.cursor
	if cursorIdx < 0 || cursorIdx >= len(m.flatRows) {
		return
	}
	if cursorIdx < m.scrollOffset {
		for m.scrollOffset > 0 && render.LastVisibleIdx(m.flatRows, m.scrollOffset-1, bodyHeight) >= cursorIdx {
			m.scrollOffset--
		}
		return
	}
	for m.scrollOffset < len(m.flatRows) && render.LastVisibleIdx(m.flatRows, m.scrollOffset, bodyHeight) < cursorIdx {
		m.scrollOffset++
	}
}
```

- [ ] **Step 2: Run all tui tests**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go test ./internal/tui/... -v 2>&1 | tail -40
```
Expected: all PASS

- [ ] **Step 3: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/internal/tui/update.go
git commit -m "feat(claude-agents-tui): add Space/Left/Right/h/l collapse handlers, rewrite syncScroll"
```

---

## Task 8: Update `internal/tui/view.go`

**Files:**
- Modify: `internal/tui/view.go`

- [ ] **Step 1: Rewrite view.go**

```go
// internal/tui/view.go
package tui

import (
	"strings"

	"github.com/phillipgreenii/claude-agents-tui/internal/render"
)

func (m *Model) View() string {
	if m.tree == nil {
		return "loading…"
	}
	if m.selected != nil {
		return RenderDetails(m.selected)
	}
	header := render.Header(m.tree, render.HeaderOpts{
		CaffeinateOn: m.caffeinateOn,
		ShowAll:      m.showAll,
		CostMode:     m.costMode,
		ForceID:      m.forceID,
		Theme:        m.theme,
	})
	legend := "● working  ○ idle  ? awaiting  ✕ dormant   🤖 subagents  🐚 shells  🌿 branch       [↑↓jk] nav  [space/←→/hl] collapse  [enter] details"

	var body string
	noBlock := m.tree.CCUsageProbed && m.tree.ActiveBlock == nil && m.tree.CCUsageErr == nil
	if noBlock {
		body = "Sessions not shown — no active block.\n"
	} else if len(m.flatRows) == 0 {
		body = "No active sessions.\n"
	} else {
		totalTok := 0
		for _, d := range m.tree.Dirs {
			totalTok += d.TotalTokens
		}
		opts := render.TreeOpts{
			ShowAll:            m.showAll,
			ForceID:            m.forceID,
			CostMode:           m.costMode,
			Width:              m.width,
			Cursor:             m.cursor,
			HasCursor:          m.selected == nil,
			Theme:              m.theme,
			TotalSessionTokens: totalTok,
		}
		if m.height > 0 {
			headerLines := strings.Count(header, "\n")
			bodyHeight := max(m.height-headerLines-1, 1) // 1 for legend
			body = render.RenderWindowTree(m.pathNodes, m.flatRows, m.scrollOffset, bodyHeight, opts)
		} else {
			body = render.RenderWindowTree(m.pathNodes, m.flatRows, 0, 10000, opts)
		}
	}
	return strings.Join([]string{header, body, legend}, "\n")
}
```

- [ ] **Step 2: Run full test suite**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go test ./... 2>&1
```
Expected: all PASS

- [ ] **Step 3: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/internal/tui/view.go
git commit -m "feat(claude-agents-tui): switch view to FlattenPathTree + RenderWindowTree"
```

---

## Task 9: Wire `CacheDir` in `cmd/claude-agents-tui/main.go`

**Files:**
- Modify: `cmd/claude-agents-tui/main.go`

- [ ] **Step 1: Update model initialization in main.go**

Locate the `tui.NewModel` call (line 113) and add `CacheDir`:

```go
cacheDir := filepath.Join(home, ".cache", "claude-agents-tui")
model := tui.NewModel(tui.Options{
	Tree:       &aggregate.Tree{},
	Poller:     p,
	Interval:   cfg.RefreshInterval,
	Caffeinate: mgr,
	CacheDir:   cacheDir,
})
```

(`filepath` is already imported in main.go; `home` is already computed.)

- [ ] **Step 2: Build and verify no compile errors**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go build ./... 2>&1
```
Expected: no output (clean build)

- [ ] **Step 3: Run full test suite one final time**

```bash
go test ./... 2>&1
```
Expected: all PASS

- [ ] **Step 4: Run pre-commit hooks**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
prek run --all-files 2>&1 || pre-commit run --all-files 2>&1
```
Expected: all hooks PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/cmd/claude-agents-tui/main.go
git commit -m "feat(claude-agents-tui): pass CacheDir to model for tree-state persistence"
```

---

## Self-Review

**Spec coverage check:**
- ✅ `internal/treestate` package: Load/IsCollapsed/Toggle/Save → Task 1
- ✅ `aggregate.PathNode` + `BuildPathTree` → Task 2
- ✅ Compression: single-child no-session nodes merged → Task 2
- ✅ Branch points (no-session, 2+ children) kept → Task 2
- ✅ DisplayPath: full path for roots, relative suffix for children → Task 2
- ✅ Rollup stats bottom-up → Task 2
- ✅ `PathNodeKind` RowKind value → Task 3
- ✅ `NodePath`, `Depth`, `Collapsed` Row fields → Task 3
- ✅ `FlattenPathTree` → Task 3
- ✅ `RenderPathNode` with ▼/▶ glyph → Task 4
- ✅ Rollup respects CostMode → Task 4
- ✅ Depth indentation (depth * 2 spaces) → Task 4
- ✅ `RenderWindowTree` handles PathNodeKind + SessionKind → Task 5
- ✅ Model fields `treeState`, `pathNodes`, `flatRows` → Task 6
- ✅ `rebuildFlatRows()` → Task 6
- ✅ `rowAt()` replaces `sessionAt()` → Task 6
- ✅ `clampCursor()` uses `len(m.flatRows)` → Task 6
- ✅ Space = toggle collapse → Task 7
- ✅ Left/h = collapse if expanded → Task 7
- ✅ Right/l = expand if collapsed → Task 7
- ✅ Enter on PathNodeKind = no-op (explicit guard) → Task 7
- ✅ `syncScroll` rewritten for row-based cursor → Task 7
- ✅ `rebuildFlatRows` called on showAll toggle and pollResultMsg → Task 7
- ✅ `view.go` uses `RenderWindowTree` → Task 8
- ✅ Legend updated with new keybindings → Task 8
- ✅ `CacheDir` passed from main.go → Task 9

**No placeholders detected.**

**Type consistency:** `PathNode` defined in Task 2, used in Tasks 3–8. `Row.Node *aggregate.PathNode` set in Task 3, read in Task 5. `Row.Session *aggregate.SessionView` set in Task 3, read in Tasks 5–7. All consistent.
