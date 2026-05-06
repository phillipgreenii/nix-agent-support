package aggregate

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/phillipgreenii/claude-agents-tui/internal/session"
)

// PathNode is one node in the compressed path tree built from a flat []*Directory.
type PathNode struct {
	FullPath       string
	DisplayPath    string // full path for roots; relative suffix for children
	Depth          int
	DirectSessions []*SessionView
	Children       []*PathNode
	// Rollup stats aggregated from this node and all descendants
	WorkingN     int
	IdleN        int
	DormantN     int
	TotalTokens  int
	TotalCostUSD float64
	BurnRateSum  float64
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
		case session.Working:
			n.WorkingN++
		case session.Idle:
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
