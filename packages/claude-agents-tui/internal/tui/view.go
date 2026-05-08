package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/phillipgreenii/claude-agents-tui/internal/render"
	"github.com/phillipgreenii/claude-agents-tui/internal/render/wrap"
)

func (m *Model) View() string {
	// Defer rendering until the first WindowSizeMsg arrives. The View contract
	// is "every line ≤ effectiveWidth"; we cannot honor it without knowing the
	// width, and a partial frame would be fed to bubbletea's diff engine and
	// soft-wrapped by the terminal.
	if m.width == 0 {
		return "loading…"
	}
	if m.tree == nil {
		return "loading…"
	}
	if m.selected != nil {
		return wrap.Block(RenderDetails(m.selected, m.width), wrap.EffectiveWidth(m.width))
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
		Width:           m.width,
	})
	legend := render.Legend(m.width)

	var body string
	if len(m.flatRows) == 0 {
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
	joined := strings.Join([]string{header, body, legend}, "\n")
	return wrap.Block(joined, wrap.EffectiveWidth(m.width))
}

// visualLineCount returns the number of terminal lines the string will occupy
// at the given terminal width, accounting for line wrapping. Width ≤ 0 falls
// back to wrap.FallbackWidth so the count stays consistent with the rest of
// the rendering pipeline.
func visualLineCount(s string, width int) int {
	ew := wrap.EffectiveWidth(width)
	total := 0
	for line := range strings.SplitSeq(s, "\n") {
		w := lipgloss.Width(line) // strips ANSI, measures display width
		if w == 0 {
			total++
		} else {
			total += (w + ew - 1) / ew
		}
	}
	return total
}
