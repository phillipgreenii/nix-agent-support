package render

import (
	"fmt"
	"strings"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
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

// EffectiveLastVis returns the last row index that will be visible when
// RenderWindowTree is called with the given scrollOffset and bodyHeight. It
// accounts for the rows reserved by the top ("↑ N sessions") and bottom
// ("↓ N sessions") scroll indicators. Use this from scroll-sync code so the
// visible-row math matches what the renderer actually shows.
func EffectiveLastVis(rows []Row, scrollOffset, bodyHeight int) int {
	if len(rows) == 0 || bodyHeight <= 0 {
		return scrollOffset - 1
	}
	budget := bodyHeight
	if scrollOffset > 0 {
		budget--
	}
	lastVis := LastVisibleIdx(rows, scrollOffset, budget)
	if lastVis < len(rows)-1 {
		budget--
		lastVis = LastVisibleIdx(rows, scrollOffset, budget)
	}
	return lastVis
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
			prefix := "├─"
			if row.IsLastInGroup {
				prefix = "└─"
			}
			indent := strings.Repeat("  ", row.Depth)
			selected := opts.HasCursor && i == opts.Cursor
			// Reduce reported width by indent size so labelStyle computes the
			// correct label column — prefixCols assumes no indentation.
			sessionOpts := opts
			if sessionOpts.Width > 0 {
				sessionOpts.Width -= 2 * row.Depth
			}
			sb.WriteString(renderSession(row.Session, sessionOpts, indent+prefix, selected))
		}
	}

	if botInd {
		n := countSessionRows(rows, lastVis+1, len(rows))
		sb.WriteString(opts.Theme.Prompt.Render(fmt.Sprintf("  ↓ %s", pluralSession(n))))
		sb.WriteString("\n")
	}

	return sb.String()
}
