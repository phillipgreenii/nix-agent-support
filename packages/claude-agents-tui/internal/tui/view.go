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
	legend := "● working  ○ idle  ? awaiting  ✕ dormant   🤖 subagents  🐚 shells  🌿 branch       [↑↓] nav  [enter] details"

	var body string
	noBlock := m.tree.CCUsageProbed && m.tree.ActiveBlock == nil && m.tree.CCUsageErr == nil
	if noBlock {
		body = "Sessions not shown — no active block.\n"
	} else {
		visibleCount := 0
		for _, d := range m.tree.Dirs {
			visibleCount += d.WorkingN + d.IdleN
			if m.showAll {
				visibleCount += d.DormantN
			}
		}
		if visibleCount == 0 {
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
			rows := render.FlattenRows(m.tree, opts)
			if m.height > 0 {
				headerLines := strings.Count(header, "\n")
				bodyHeight := max(m.height-headerLines-1, 1) // 1 for legend
				body = render.RenderWindow(m.tree, rows, m.scrollOffset, bodyHeight, opts)
			} else {
				body = render.Tree(m.tree, opts)
			}
		}
	}
	return strings.Join([]string{header, body, legend}, "\n")
}
