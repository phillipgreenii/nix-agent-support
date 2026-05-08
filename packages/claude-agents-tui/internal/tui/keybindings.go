package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/claude-agents-tui/internal/render"
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
	// Stub for commit 1; commit 2 wires modal state.
	return nil
}

func handleOpenLegend(m *Model) tea.Cmd {
	// Stub for commit 1; commit 2 wires modal state.
	return nil
}

func handleQuit(m *Model) tea.Cmd {
	return tea.Quit
}

func handleDown(m *Model) tea.Cmd {
	start := m.cursor + 1
	if start < len(m.flatRows) {
		m.cursor = nextSelectable(m.flatRows, start, +1)
	}
	m.clampCursor()
	m.syncScroll()
	return nil
}

func handleUp(m *Model) tea.Cmd {
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
	return nil
}

func handleToggleAutoResume(m *Model) tea.Cmd {
	m.autoResume = !m.autoResume
	if m.autoResume && !m.tree.WindowResetsAt.IsZero() && !m.autoResumeFired {
		fireAt := m.tree.WindowResetsAt.Add(m.autoResumeDelay)
		cmds := []tea.Cmd{autoResumeFireCmd(fireAt)}
		if !m.countdownTick {
			m.countdownTick = true
			cmds = append(cmds, countdownTickCmd())
		}
		return tea.Batch(cmds...)
	}
	return nil
}

func handleManualResume(m *Model) tea.Cmd {
	m.signalNonWorking("manual-resume")
	return nil
}
