// Package tui implements pr-pool's operator-facing terminal UI. This file
// (Task 4.8) wires keybindings.go's Bindings table into the [?] help
// modal, and is the general modal dispatcher View (model.go) calls into
// while m.screen == screenModal: help.go's own HelpModal, render.
// LegendModal (Task 4.3's, reused directly), and gates.go's
// renderGatesModal.
package tui

import (
	"fmt"
	"strings"

	"github.com/phillipgreenii/pr-pool/internal/tui/render"
)

// bindingsToHelpRows converts Bindings into the (Keys, Description) pairs
// the [?] help modal renders. Keys are " | "-joined for display -- pa-
// monitor's own pattern (packages/pa-monitor/internal/tui/view.go),
// reused verbatim.
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

// renderModal dispatches on the active modal kind. Called only from View
// (model.go) while m.screen == screenModal; ModalNone (unreachable in
// that state, but handled defensively) renders nothing.
func (m *Model) renderModal() string {
	switch m.activeModal {
	case ModalHelp:
		return render.HelpModal(bindingsToHelpRows(), m.helpFooter(), m.width, m.height, m.modalScrollOffset)
	case ModalLegend:
		return render.LegendModal(m.width, m.height, m.modalScrollOffset)
	case ModalGates:
		return m.renderGatesModal()
	default:
		return ""
	}
}

// helpFooter is spec §6's "? ... footer carries the version pair +
// error-log path": this TUI client's own build identifier alongside the
// core's last-reported CoreInfo.Version, plus where the ErrorLogger is
// writing. An empty cacheDir renders no error-log line -- there is
// nowhere to point to (Options.CacheDir unset).
func (m *Model) helpFooter() string {
	coreVersion := m.reply.Core.Version
	if coreVersion == "" {
		coreVersion = "(no core seen yet)"
	}
	lines := []string{fmt.Sprintf("pr-pool %s (TUI) ⇄ %s (core)", m.clientVersion, coreVersion)}
	if m.cacheDir != "" {
		lines = append(lines, "Errors logged to: "+errorLogPath(m.cacheDir))
	}
	return strings.Join(lines, "\n")
}
