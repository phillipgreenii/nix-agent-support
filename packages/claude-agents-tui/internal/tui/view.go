package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
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
		CaffeinateOn:    m.caffeinateOn,
		ShowAll:         m.showAll,
		CostMode:        m.costMode,
		ForceID:         m.forceID,
		Theme:           m.theme,
		AutoResume:      m.autoResume,
		WindowResetsAt:  m.tree.WindowResetsAt,
		AutoResumeDelay: m.autoResumeDelay,
	})
	legend := "● working  ○ idle  ⏸ paused  ? awaiting  ✕ dormant   🤖 subagents  🐚 shells  🌿 branch       [↑↓jk] nav  [space/←→/hl] collapse  [enter] details"

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
			headerLines := visualLineCount(header, m.width)
			bodyHeight := max(m.height-headerLines-1, 1) // 1 for legend
			body = render.RenderWindowTree(m.pathNodes, m.flatRows, m.scrollOffset, bodyHeight, opts)
		} else {
			body = render.RenderWindowTree(m.pathNodes, m.flatRows, 0, 10000, opts)
		}
	}
	return strings.Join([]string{header, body, legend}, "\n")
}

// visualLineCount returns the number of terminal lines the string will occupy
// at the given terminal width, accounting for line wrapping. Width ≤ 0 falls
// back to counting newlines.
func visualLineCount(s string, width int) int {
	total := 0
	for line := range strings.SplitSeq(s, "\n") {
		w := lipgloss.Width(line) // strips ANSI, measures display width
		if width <= 0 || w == 0 {
			total++
		} else {
			total += (w + width - 1) / width
		}
	}
	return total
}
