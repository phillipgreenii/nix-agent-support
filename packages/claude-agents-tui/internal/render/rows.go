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
	BlankKind    // blank separator line after each directory group
	PathNodeKind // collapsible path tree node (replaces DirHeaderKind in tree mode)
)

// Row is one logical element in the rendered session list.
type Row struct {
	Kind    RowKind
	DirIdx  int // DirHeaderKind/SessionKind (legacy): index into tree.Dirs
	SessIdx int // SessionKind (legacy): index within dir's visible sessions
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
		for sessIdx := range visible {
			rows = append(rows, Row{
				Kind:      SessionKind,
				DirIdx:    dirIdx,
				SessIdx:   sessIdx,
				FlatIdx:   flatIdx,
				LineCount: 1,
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
			walk(child)
		}
	}
	for _, n := range nodes {
		walk(n)
		rows = append(rows, Row{Kind: BlankKind, LineCount: 1})
	}
	return rows
}
