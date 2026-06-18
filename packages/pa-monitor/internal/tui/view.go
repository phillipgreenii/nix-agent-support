package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/render"
	"github.com/phillipgreenii/pa-monitor/internal/render/wrap"
)

func (m *Model) View() string {
	if m.width == 0 {
		return "loading…"
	}
	if m.activeModal != ModalNone {
		return wrap.Block(m.renderModal(), wrap.EffectiveWidth(m.width))
	}
	if m.tree == nil {
		if m.lastErr != nil {
			return daemonOfflineMessage(m.clientVersion, m.lastErr)
		}
		return "loading…"
	}
	if m.selected != nil {
		return wrap.Block(RenderDetailsWindow(m.selected, m.width, m.height, m.detailsScrollOffset), wrap.EffectiveWidth(m.width))
	}

	now := time.Now()
	controls := render.Controls(render.ControlsOpts{
		CaffeinateOn:      m.caffeinateOn,
		CaffeinateProcess: m.caffeinateProcess,
		GraceRemaining:    m.caffeinateGraceRemaining,
		ShowAll:           m.showAll,
		CostMode:          m.costMode,
		ForceID:           m.forceID,
		AutoResume:        m.autoResumeEnabled,
		DaemonConnected:   m.daemonConnected,
		Theme:             m.theme,
		Width:             m.width,
	})
	blockRow := render.BlockRow(m.tree, render.BlockRowOpts{Width: m.width, Now: now})
	alerts := render.Alerts(m.tree, render.AlertsOpts{
		Now:             now,
		Width:           m.width,
		AutoResume:      m.autoResumeEnabled,
		WindowResetsAt:  m.tree.WindowResetsAt,
		AutoResumeDelay: m.autoResumeDelay,
	})
	footer := render.Footer(m.width, m.selectionStatus(), now)

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

// selectionStatus returns the dim-styled, single-line status string for the
// footer's left column. Empty when the cursor is not on a session row, or
// the selected session has no FirstPrompt.
func (m *Model) selectionStatus() string {
	if m.cursor < 0 || m.cursor >= len(m.flatRows) {
		return ""
	}
	row := m.flatRows[m.cursor]
	if row.Kind != render.SessionKind || row.Session == nil {
		return ""
	}
	fp := row.Session.SessionEnrichment.FirstPrompt
	if fp == "" {
		return ""
	}
	leftWidth := render.FooterLeftWidth(m.width)
	if leftWidth < 1 {
		return ""
	}
	text := fmt.Sprintf("%q", wrap.Line(fp, leftWidth))
	return m.theme.Prompt.Render(text)
}

// renderModal returns the full-screen modal content for the active modal.
func (m *Model) renderModal() string {
	switch m.activeModal {
	case ModalHelp:
		dv := m.daemonVersion
		if dv == "" {
			dv = "(disconnected)"
		}
		lines := []string{fmt.Sprintf("pa-monitor %s (TUI) ⇄ %s (daemon)", m.clientVersion, dv)}
		if m.cacheDir != "" {
			lines = append(lines, "Signal errors logged to: "+filepath.Join(m.cacheDir, "signal-errors.log"))
		}
		extra := strings.Join(lines, "\n")
		return render.HelpModal(bindingsToHelpRows(), extra, m.width, m.height, m.modalScrollOffset)
	case ModalLegend:
		return render.LegendModal(m.width, m.height, m.modalScrollOffset)
	}
	return ""
}

// daemonOfflineMessage renders a clear offline-state screen for the TUI's
// pre-first-tree state when polling against the daemon has failed. The TUI
// has no local fallback — the daemon owns all session state — so an empty
// tree + a pollErrMsg means "nothing to show, here's what to do about it."
func daemonOfflineMessage(clientVersion string, err error) string {
	return fmt.Sprintf(`Daemon offline.

pa-monitor %s (TUI) cannot reach the daemon.

Last error:
  %v

To start the daemon:
  launchctl kickstart -k gui/$UID/com.phillipg.pa-monitor-daemon
or run in the foreground:
  pa-monitor daemon

Press q to quit.
`, clientVersion, err)
}

// bindingsToHelpRows converts Bindings into the (Keys, Description) pairs the
// help modal renders. Keys are " | "-joined for display.
func bindingsToHelpRows() []render.HelpRow {
	out := make([]render.HelpRow, 0, len(Bindings))
	for _, b := range Bindings {
		out = append(out, render.HelpRow{
			Keys:        strings.Join(b.Keys, " | "),
			Description: b.Description,
		})
	}
	return out
}
