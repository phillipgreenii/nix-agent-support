package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/render"
)

// Binding registers one keybinding with its display description and handler.
// All TUI dispatch flows through Bindings — adding a new key means appending
// here, and the [?] help modal picks it up automatically.
type Binding struct {
	Keys        []string             // bubbletea key strings; e.g. ["down", "j"], ["ctrl+c", "q"]
	Description string               // shown in [?] modal
	Handle      func(*Model) tea.Cmd // returns a tea.Cmd if any (nil otherwise)
}

// Bindings is the canonical, ordered keybinding list. Order matters: first
// match wins in dispatch, and rows render in this order in the help modal.
var Bindings = []Binding{
	{Keys: []string{"?"}, Description: "Help", Handle: handleOpenHelp},
	{Keys: []string{"l"}, Description: "Legend", Handle: handleOpenLegend},
	{Keys: []string{"q", "ctrl+c"}, Description: "Quit", Handle: handleQuit},
	{Keys: []string{"down", "j"}, Description: "Cursor / scroll down", Handle: handleDown},
	{Keys: []string{"up", "k"}, Description: "Cursor / scroll up", Handle: handleUp},
	{Keys: []string{"enter"}, Description: "Open session details", Handle: handleEnter},
	{Keys: []string{" ", "right"}, Description: "Toggle path-tree collapse", Handle: handleExpandToggle},
	{Keys: []string{"left", "h"}, Description: "Collapse current path node", Handle: handleCollapse},
	{Keys: []string{"esc"}, Description: "Close detail panel / modal", Handle: handleEsc},
	{Keys: []string{"t"}, Description: "Toggle tokens / cost", Handle: handleToggleCost},
	{Keys: []string{"a"}, Description: "Toggle active / all", Handle: handleToggleAll},
	{Keys: []string{"n"}, Description: "Toggle name / id", Handle: handleToggleID},
	{Keys: []string{"C"}, Description: "Toggle caffeinate", Handle: handleToggleCaffeinate},
	{Keys: []string{"R"}, Description: "Toggle auto-resume", Handle: handleToggleAutoResume},
	{Keys: []string{"M"}, Description: "Manually fire resume", Handle: handleManualResume},
}

// --- Handlers (one per Binding) ---

func handleOpenHelp(m *Model) tea.Cmd {
	m.activeModal = ModalHelp
	m.modalScrollOffset = 0
	return nil
}

func handleOpenLegend(m *Model) tea.Cmd {
	m.activeModal = ModalLegend
	m.modalScrollOffset = 0
	return nil
}

func handleQuit(m *Model) tea.Cmd {
	return tea.Quit
}

func handleDown(m *Model) tea.Cmd {
	if m.activeModal != ModalNone {
		m.modalScrollOffset++
		// Caller (Modal renderer) clamps; the model's offset is allowed to
		// overshoot temporarily.
		return nil
	}
	start := m.cursor + 1
	if start < len(m.flatRows) {
		m.cursor = nextSelectable(m.flatRows, start, +1)
	}
	m.clampCursor()
	m.syncScroll()
	return nil
}

func handleUp(m *Model) tea.Cmd {
	if m.activeModal != ModalNone {
		if m.modalScrollOffset > 0 {
			m.modalScrollOffset--
		}
		return nil
	}
	start := m.cursor - 1
	if start >= 0 {
		m.cursor = nextSelectable(m.flatRows, start, -1)
	}
	m.clampCursor()
	m.syncScroll()
	return nil
}

func handleEnter(m *Model) tea.Cmd {
	if row, ok := m.rowAt(m.cursor); ok && row.Kind == render.SessionKind {
		m.selected = row.Session
	}
	return nil
}

func handleExpandToggle(m *Model) tea.Cmd {
	if row, ok := m.rowAt(m.cursor); ok && row.Kind == render.PathNodeKind {
		m.treeState.Toggle(row.NodePath)
		if m.cacheDir != "" {
			_ = m.treeState.Save(m.cacheDir)
		}
		m.rebuildFlatRows()
		m.clampCursor()
		m.syncScroll()
	}
	return nil
}

func handleCollapse(m *Model) tea.Cmd {
	if row, ok := m.rowAt(m.cursor); ok && row.Kind == render.PathNodeKind && !row.Collapsed {
		m.treeState.Toggle(row.NodePath)
		if m.cacheDir != "" {
			_ = m.treeState.Save(m.cacheDir)
		}
		m.rebuildFlatRows()
		m.clampCursor()
		m.syncScroll()
	}
	return nil
}

func handleEsc(m *Model) tea.Cmd {
	if m.activeModal != ModalNone {
		m.activeModal = ModalNone
		return nil
	}
	m.selected = nil
	return nil
}

func handleToggleCost(m *Model) tea.Cmd {
	m.costMode = !m.costMode
	return nil
}

func handleToggleAll(m *Model) tea.Cmd {
	m.showAll = !m.showAll
	m.rebuildFlatRows()
	m.clampCursor()
	m.syncScroll()
	return nil
}

func handleToggleID(m *Model) tea.Cmd {
	m.forceID = !m.forceID
	return nil
}

func handleToggleCaffeinate(m *Model) tea.Cmd {
	m.caffeinateOn = !m.caffeinateOn
	if m.onCaffeinateToggle != nil {
		// In --remote mode this dispatches the Caffeinate RPC to the
		// daemon so the *daemon's* caffeinate manager actually runs.
		m.onCaffeinateToggle(m.caffeinateOn)
	}
	m.reporter.Push(m.buildSidebarSnapshot())
	return nil
}

func handleToggleAutoResume(m *Model) tea.Cmd {
	want := !m.autoResumeEnabled
	// Optimistic update; daemon confirmation arrives on the next TreeUpdatedMsg.
	m.autoResumeEnabled = want
	if m.onToggleAutoResume != nil {
		m.onToggleAutoResume(want)
	}
	return nil
}

// cursorScopeSelector returns the nudge selector and the set of session IDs
// in scope based on the current cursor position.
//
//   - Cursor on a SessionKind row → "session:<id>", [sid]
//   - Cursor on a PathNodeKind row → "path:<dir>", all sid's under that node
//   - No recognisable row / empty list → "", nil
func (m *Model) cursorScopeSelector() (sids []string, selector string) {
	row, ok := m.rowAt(m.cursor)
	if !ok {
		return nil, ""
	}
	switch row.Kind {
	case render.SessionKind:
		if row.Session == nil || row.Session.Session == nil {
			return nil, ""
		}
		sid := row.Session.SessionID
		return []string{sid}, "session:" + sid
	case render.PathNodeKind:
		if row.Node == nil {
			return nil, ""
		}
		sids = allSessionIDsUnderNode(row.Node)
		if len(sids) == 0 {
			return nil, ""
		}
		return sids, "path:" + row.Node.FullPath
	}
	return nil, ""
}

// allSessionIDsUnderNode collects SessionIDs from a PathNode and all its
// descendants recursively.
func allSessionIDsUnderNode(n *aggregate.PathNode) []string {
	var out []string
	for _, sv := range n.DirectSessions {
		if sv != nil && sv.Session != nil {
			out = append(out, sv.SessionID)
		}
	}
	for _, child := range n.Children {
		out = append(out, allSessionIDsUnderNode(child)...)
	}
	return out
}

// sessionHasPendingManual returns true when the session identified by sid has
// a "manual" source in its PendingNudge.
func (m *Model) sessionHasPendingManual(sid string) bool {
	if m.tree == nil {
		return false
	}
	for _, d := range m.tree.Dirs {
		for _, sv := range d.Sessions {
			if sv.Session == nil || sv.SessionID != sid {
				continue
			}
			if sv.SessionEnrichment.PendingNudge == nil {
				return false
			}
			for _, src := range sv.SessionEnrichment.PendingNudge.Sources {
				if src == "manual" {
					return true
				}
			}
			return false
		}
	}
	return false
}

func handleManualResume(m *Model) tea.Cmd {
	sids, selector := m.cursorScopeSelector()
	if len(sids) == 0 || m.onManualNudge == nil {
		return nil
	}
	// Toggle semantics: if ALL selected sessions already have a manual nudge
	// pending, cancel; otherwise queue.
	allPending := true
	for _, sid := range sids {
		if !m.sessionHasPendingManual(sid) {
			allPending = false
			break
		}
	}
	m.onManualNudge(selector, allPending)
	return nil
}
