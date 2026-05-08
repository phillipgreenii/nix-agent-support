package tui

import (
	"time"

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

	now := time.Now()
	controls := render.Controls(render.ControlsOpts{
		CaffeinateOn: m.caffeinateOn,
		ShowAll:      m.showAll,
		CostMode:     m.costMode,
		ForceID:      m.forceID,
		AutoResume:   m.autoResume,
		Theme:        m.theme,
		Width:        m.width,
	})
	blockRow := render.BlockRow(m.tree, render.BlockRowOpts{Width: m.width, Now: now})
	alerts := render.Alerts(m.tree, render.AlertsOpts{
		Now:             now,
		Width:           m.width,
		AutoResume:      m.autoResume,
		WindowResetsAt:  m.tree.WindowResetsAt,
		AutoResumeDelay: m.autoResumeDelay,
	})
	footer := render.Footer(m.width, now)

	zones := []zoneSpec{
		{name: "controls", content: controls, dropOrder: 1},
		{name: "block", content: blockRow, dropOrder: 2},
	}
	if alerts != "" {
		zones = append(zones, zoneSpec{name: "alert", content: alerts, dropOrder: 3})
	}
	zones = append(zones,
		zoneSpec{
			name: "body",
			fill: true,
			renderFill: func(h int) string {
				return m.renderBody(h)
			},
		},
		zoneSpec{name: "footer", content: footer, dropOrder: 4},
	)

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
