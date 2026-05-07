package render

import (
	"fmt"
	"strings"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
)

// LastVisibleIdx returns the index of the last row from rows[offset:] that
// fits within budget terminal lines. Returns offset-1 when nothing fits.
func LastVisibleIdx(rows []Row, offset, budget int) int {
	last := offset - 1
	for i := offset; i < len(rows); i++ {
		if budget < rows[i].LineCount {
			break
		}
		budget -= rows[i].LineCount
		last = i
	}
	return last
}

// stickyDir returns the DirIdx whose header is above scrollOffset but whose
// sessions are within the visible window. Returns -1 when no pin is needed.
func stickyDir(rows []Row, scrollOffset int) int {
	if scrollOffset == 0 {
		return -1
	}
	for i := scrollOffset; i < len(rows); i++ {
		switch rows[i].Kind {
		case BlankKind:
			continue
		case DirHeaderKind:
			return -1 // dir header is in the window — no pin needed
		case SessionKind:
			dirIdx := rows[i].DirIdx
			for j := i - 1; j >= 0; j-- {
				if rows[j].Kind == DirHeaderKind && rows[j].DirIdx == dirIdx {
					if j < scrollOffset {
						return dirIdx
					}
					return -1
				}
			}
			return -1
		}
	}
	return -1
}

func countSessionRows(rows []Row, from, to int) int {
	n := 0
	for i := from; i < to && i < len(rows); i++ {
		if rows[i].Kind == SessionKind {
			n++
		}
	}
	return n
}

func pluralSession(n int) string {
	if n == 1 {
		return fmt.Sprintf("%d session", n)
	}
	return fmt.Sprintf("%d sessions", n)
}

// RenderWindow renders a height-bounded window of rows with scroll indicators
// and a pinned dir header when its sessions have scrolled past the top edge.
func RenderWindow(tree *aggregate.Tree, rows []Row, scrollOffset, bodyHeight int, opts TreeOpts) string {
	if len(rows) == 0 || bodyHeight <= 0 {
		return ""
	}

	budget := bodyHeight

	topInd := scrollOffset > 0
	if topInd {
		budget--
	}

	sticky := stickyDir(rows, scrollOffset)
	if sticky >= 0 {
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

	if sticky >= 0 {
		sb.WriteString(renderDirRow(tree.Dirs[sticky], opts))
	}

	cachedDirIdx := -1
	var visible []*aggregate.SessionView
	for i := scrollOffset; i <= lastVis; i++ {
		row := rows[i]
		switch row.Kind {
		case DirHeaderKind:
			sb.WriteString(renderDirRow(tree.Dirs[row.DirIdx], opts))
		case BlankKind:
			sb.WriteString("\n")
		case SessionKind:
			if row.DirIdx != cachedDirIdx {
				cachedDirIdx = row.DirIdx
				visible = visibleSessions(tree.Dirs[row.DirIdx].Sessions, opts.ShowAll)
			}
			s := visible[row.SessIdx]
			isLast := row.SessIdx == len(visible)-1
			prefix, cont := "├─", "│"
			if isLast {
				prefix, cont = "└─", " "
			}
			selected := opts.HasCursor && row.FlatIdx == opts.Cursor
			sb.WriteString(renderSession(s, opts, prefix, cont, selected))
		}
	}

	if botInd {
		n := countSessionRows(rows, lastVis+1, len(rows))
		sb.WriteString(opts.Theme.Prompt.Render(fmt.Sprintf("  ↓ %s", pluralSession(n))))
		sb.WriteString("\n")
	}

	return sb.String()
}

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
			// Reduce reported width by indent size so labelStyle computes the
			// correct label column — prefixCols assumes no indentation.
			sessionOpts := opts
			if sessionOpts.Width > 0 {
				sessionOpts.Width -= 2 * row.Depth
			}
			// Pass indent+cont so the ↳ continuation line aligns with the session prefix.
			sb.WriteString(renderSession(row.Session, sessionOpts, indent+prefix, indent+cont, selected))
		}
	}

	if botInd {
		n := countSessionRows(rows, lastVis+1, len(rows))
		sb.WriteString(opts.Theme.Prompt.Render(fmt.Sprintf("  ↓ %s", pluralSession(n))))
		sb.WriteString("\n")
	}

	return sb.String()
}
