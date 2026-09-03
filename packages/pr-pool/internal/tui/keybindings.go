// Package tui implements pr-pool's operator-facing terminal UI. This file
// (Task 4.8) carries the keybinding table: every key the design names
// (P, g, l, ?, tab/shift+tab, enter, [/], esc, q), plus the "R"
// sub-binding documented alongside g ("R = resume-all inside the
// modal") [design: Task 4.8 Files]. Mirrors pa-monitor's own keybindings.go (packages/pa-monitor/internal/
// tui/keybindings.go) shape exactly: a Binding table drives both Update's
// keypress dispatch (model.go) and the [?] help modal (help.go's
// bindingsToHelpRows) from one source of truth.
package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Binding registers one keybinding with its display description and
// handler. All TUI keypress dispatch flows through Bindings (model.go's
// Update) -- adding a new key means appending here, and the [?] help
// modal picks it up automatically.
type Binding struct {
	Keys        []string             // bubbletea key strings; e.g. ["q", "ctrl+c"]
	Description string               // shown in the [?] modal
	Handle      func(*Model) tea.Cmd // returns a tea.Cmd if any (nil otherwise)
}

// Bindings is the canonical, ordered keybinding list. Order matters: first
// match wins in dispatch, and rows render in this order in the help modal.
//
// tab/shift+tab remain deliberate no-op placeholders: Task 4.6 delivered
// only the RENDERING side of pane focus (Model.focusedPane), not the
// keybinding that moves it -- that wiring is a later packet's concern. The
// KEY is fully covered here (this packet's own acceptance bar), but there
// is nothing for it to DO yet. enter/[/] now delegate to drilldown.go's
// Model.enterDrillDown/stepSibling (Task 4.7).
var Bindings = []Binding{
	{Keys: []string{"P"}, Description: "Toggle the quota gate (no optimistic flip)", Handle: (*Model).handleToggleQuotaGate},
	{Keys: []string{"g"}, Description: "Gates modal", Handle: handleOpenGatesModal},
	{Keys: []string{"l"}, Description: "Legend", Handle: handleOpenLegend},
	{Keys: []string{"?"}, Description: "Help", Handle: handleOpenHelp},
	{Keys: []string{"tab"}, Description: "Focus next pane", Handle: handleFocusNext},
	{Keys: []string{"shift+tab"}, Description: "Focus previous pane", Handle: handleFocusPrev},
	{Keys: []string{"enter"}, Description: "Drill into the focused listener/source row", Handle: handleEnterDrillDown},
	{Keys: []string{"["}, Description: "Previous sibling row (drill-down)", Handle: handlePrevSibling},
	{Keys: []string{"]"}, Description: "Next sibling row (drill-down)", Handle: handleNextSibling},
	{Keys: []string{"esc"}, Description: "Close modal / back one level (never quits at root)", Handle: handleEsc},
	{Keys: []string{"q", "ctrl+c"}, Description: "Quit, even inside a modal", Handle: handleQuit},
	{Keys: []string{"R"}, Description: "Resume all (inside the Gates modal)", Handle: handleResumeAllGates},
}

// --- Handlers (one per Binding, except the gate-toggle ones in gates.go) ---

// openModal opens kind as a full-screen takeover, remembering the screen
// that was active beforehand (unless a modal is ALREADY open, in which
// case that earlier-remembered screen is still the right one to restore --
// e.g. pressing l while the help modal is open must still restore
// whatever was showing before ? was pressed, not the help modal itself).
func (m *Model) openModal(kind ModalKind) {
	if m.activeModal == ModalNone {
		m.preModalScreen = m.screen
	}
	m.activeModal = kind
	m.modalScrollOffset = 0
	m.screen = screenModal
}

func handleOpenGatesModal(m *Model) tea.Cmd {
	m.openModal(ModalGates)
	return nil
}

func handleOpenLegend(m *Model) tea.Cmd {
	m.openModal(ModalLegend)
	return nil
}

func handleOpenHelp(m *Model) tea.Cmd {
	m.openModal(ModalHelp)
	return nil
}

// handleFocusNext / handleFocusPrev step the pane focus cycle (the design's
// tab/shift+tab row). Task 4.6 has not yet defined any focusable panes;
// today these are no-op placeholders -- see the Bindings doc comment.
func handleFocusNext(m *Model) tea.Cmd { return nil }
func handleFocusPrev(m *Model) tea.Cmd { return nil }

// handleEnterDrillDown implements the design's enter row: delegates to
// Model.enterDrillDown (drilldown.go, Task 4.7), which itself no-ops
// outside screenMain or on a non-focusable row (comp-6).
func handleEnterDrillDown(m *Model) tea.Cmd { return m.enterDrillDown() }

// handlePrevSibling / handleNextSibling implement the design's "[" / "]"
// row: delegate to Model.stepSibling (drilldown.go, Task 4.7), which
// itself no-ops outside screenDrillDown.
func handlePrevSibling(m *Model) tea.Cmd { return m.stepSibling(-1) }
func handleNextSibling(m *Model) tea.Cmd { return m.stepSibling(1) }

// handleEsc closes an open modal (restoring the screen that was active
// before it opened) or, with no modal open, exits drill-down back to
// screenMain (Task 4.7's own row in the design's screen transition table:
// "drill-down ... Exited by: esc"). With neither a modal nor drill-down
// open, esc is a no-op -- it must never quit at root [design: Task 4.8
// Files; Task 4.7].
func handleEsc(m *Model) tea.Cmd {
	if m.activeModal != ModalNone {
		m.activeModal = ModalNone
		m.modalScrollOffset = 0
		m.screen = m.preModalScreen
		return nil
	}
	if m.screen == screenDrillDown {
		m.screen = screenMain
		return nil
	}
	return nil
}

// handleQuit implements the design's q row: quits unconditionally, even
// inside a modal -- Bindings dispatch (model.go's Update) reaches this
// Binding regardless of m.screen/m.activeModal, so no special-casing is
// needed here to satisfy that.
func handleQuit(m *Model) tea.Cmd {
	return tea.Quit
}
