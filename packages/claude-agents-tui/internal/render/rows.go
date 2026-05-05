package render

import "github.com/phillipgreenii/claude-agents-tui/internal/aggregate"

// RowKind identifies what kind of content a Row represents in the session list.
type RowKind int

const (
	DirHeaderKind RowKind = iota
	SessionKind
	BlankKind // blank separator line after each directory group
)

// Row is one logical element in the rendered session list.
type Row struct {
	Kind      RowKind
	DirIdx    int // index into tree.Dirs
	SessIdx   int // SessionKind only: index within dir's visible sessions
	FlatIdx   int // SessionKind only: global session index matching TreeOpts.Cursor
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
