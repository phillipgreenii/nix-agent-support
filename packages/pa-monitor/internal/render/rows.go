package render

import (
	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/treestate"
)

// RowKind identifies what kind of content a Row represents in the session list.
type RowKind int

const (
	SessionKind  RowKind = iota
	BlankKind            // blank separator line after each directory group
	PathNodeKind         // collapsible path tree node
)

// Row is one logical element in the rendered session list.
type Row struct {
	Kind    RowKind
	FlatIdx int // SessionKind: global session index matching TreeOpts.Cursor

	// Path-tree mode fields (set by FlattenPathTree)
	NodePath      string                 // PathNodeKind: full path (collapse state key)
	Depth         int                    // PathNodeKind: indent level; SessionKind: parent node depth
	Collapsed     bool                   // PathNodeKind: current collapse state
	IsLastInGroup bool                   // SessionKind: last visible session in its parent node
	Session       *aggregate.SessionView // SessionKind: direct session pointer (path-tree mode)
	Node          *aggregate.PathNode    // PathNodeKind: direct node pointer

	LineCount int // terminal lines this row occupies (currently always 1)
}

// FlattenPathTree converts a PathNode tree into an ordered slice of Rows.
// Collapsed nodes omit all descendant sessions and children.
// BlankKind rows separate top-level nodes.
// subtreeHasVisible reports whether n or any descendant has at least one
// session visible under the current filter. Used to skip parent nodes that
// would otherwise render as an empty directory with a dead collapse toggle in
// "active" view (all their sessions dormant/hidden). A no-op when showAll.
func subtreeHasVisible(n *aggregate.PathNode, showAll bool) bool {
	if len(visibleSessions(n.DirectSessions, showAll)) > 0 {
		return true
	}
	for _, c := range n.Children {
		if subtreeHasVisible(c, showAll) {
			return true
		}
	}
	return false
}

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
			rows = append(rows, Row{
				Kind:          SessionKind,
				Depth:         n.Depth,
				FlatIdx:       flatIdx,
				IsLastInGroup: i == len(visible)-1,
				Session:       s,
				LineCount:     1,
			})
			flatIdx++
		}
		for _, child := range n.Children {
			if subtreeHasVisible(child, opts.ShowAll) {
				walk(child)
			}
		}
	}
	for _, n := range nodes {
		if !subtreeHasVisible(n, opts.ShowAll) {
			continue
		}
		walk(n)
		rows = append(rows, Row{Kind: BlankKind, LineCount: 1})
	}
	return rows
}
