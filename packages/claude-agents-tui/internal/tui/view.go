package tui

import (
	"strings"

	"github.com/phillipgreenii/claude-agents-tui/internal/render"
	"github.com/phillipgreenii/claude-agents-tui/internal/render/wrap"
)

func (m *Model) View() string {
	if m.width == 0 {
		return "loading…"
	}
	if m.tree == nil {
		return "loading…"
	}
	if m.selected != nil {
		return wrap.Block(RenderDetails(m.selected, m.width), wrap.EffectiveWidth(m.width))
	}

	header := strings.TrimRight(render.Header(m.tree, render.HeaderOpts{
		CaffeinateOn:    m.caffeinateOn,
		ShowAll:         m.showAll,
		CostMode:        m.costMode,
		ForceID:         m.forceID,
		Theme:           m.theme,
		AutoResume:      m.autoResume,
		WindowResetsAt:  m.tree.WindowResetsAt,
		AutoResumeDelay: m.autoResumeDelay,
		Width:           m.width,
	}), "\n")
	status := strings.TrimRight(render.Legend(m.width), "\n")

	zones := []zoneSpec{
		{name: "header", content: header, dropOrder: 1},
		{
			name: "body",
			fill: true,
			renderFill: func(h int) string {
				return m.renderBody(h)
			},
		},
		{name: "status", content: status, dropOrder: 2},
	}

	return layoutZones(zones, wrap.EffectiveWidth(m.width), m.height)
}

// renderBody returns up to `height` rows of session list content.
// When height is 0 (test/headless), all rows render.
func (m *Model) renderBody(height int) string {
	if len(m.flatRows) == 0 {
		return "No active sessions."
	}
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
	if height <= 0 {
		return render.RenderWindowTree(m.pathNodes, m.flatRows, 0, 10000, opts)
	}
	return render.RenderWindowTree(m.pathNodes, m.flatRows, m.scrollOffset, height, opts)
}
