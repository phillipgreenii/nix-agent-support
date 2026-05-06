package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/render"
)

type TreeUpdatedMsg struct{ Tree *aggregate.Tree }

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if isQuit(msg) {
			return m, tea.Quit
		}
		switch msg.String() {
		case "t":
			m.costMode = !m.costMode
		case "a":
			m.showAll = !m.showAll
			m.rebuildFlatRows()
			m.clampCursor()
			m.syncScroll()
		case "n":
			m.forceID = !m.forceID
		case "C":
			m.caffeinateOn = !m.caffeinateOn
		case "down", "j":
			m.cursor++
			m.clampCursor()
			m.syncScroll()
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
			m.syncScroll()
		case " ":
			if row, ok := m.rowAt(m.cursor); ok && row.Kind == render.PathNodeKind {
				m.treeState.Toggle(row.NodePath)
				if m.cacheDir != "" {
					_ = m.treeState.Save(m.cacheDir)
				}
				m.rebuildFlatRows()
				m.clampCursor()
				m.syncScroll()
			}
		case "left", "h":
			if row, ok := m.rowAt(m.cursor); ok && row.Kind == render.PathNodeKind && !row.Collapsed {
				m.treeState.Toggle(row.NodePath)
				if m.cacheDir != "" {
					_ = m.treeState.Save(m.cacheDir)
				}
				m.rebuildFlatRows()
				m.clampCursor()
				m.syncScroll()
			}
		case "right", "l":
			if row, ok := m.rowAt(m.cursor); ok && row.Kind == render.PathNodeKind && row.Collapsed {
				m.treeState.Toggle(row.NodePath)
				if m.cacheDir != "" {
					_ = m.treeState.Save(m.cacheDir)
				}
				m.rebuildFlatRows()
				m.clampCursor()
				m.syncScroll()
			}
		case "enter":
			if row, ok := m.rowAt(m.cursor); ok && row.Kind == render.SessionKind {
				m.selected = row.Session
			}
		case "esc":
			m.selected = nil
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case pollTickMsg:
		if m.polling {
			return m, tickCmd(m.interval)
		}
		m.polling = true
		return m, tea.Batch(m.pollNow(), tickCmd(m.interval))
	case pollResultMsg:
		m.polling = false
		m.tree = msg.tree
		m.anyWorking = msg.anyWorking
		if m.caffeinate != nil {
			m.caffeinate.SetToggle(m.caffeinateOn)
			m.caffeinate.Tick(msg.anyWorking)
		}
		m.rebuildFlatRows()
		m.clampCursor()
		m.syncScroll()
		if m.tree.CCUsageProbed && m.tree.ActiveBlock == nil && m.tree.CCUsageErr == nil {
			m.cursor = 0
			m.selected = nil
			m.scrollOffset = 0
		}
	case pollErrMsg:
		m.polling = false
		m.lastErr = msg.err
	case TreeUpdatedMsg:
		m.tree = msg.Tree
		m.rebuildFlatRows()
		m.clampCursor()
		m.syncScroll()
	}
	return m, nil
}

// syncScroll adjusts scrollOffset so the cursor row is within the visible window.
func (m *Model) syncScroll() {
	if m.height == 0 || len(m.flatRows) == 0 {
		return
	}
	bodyHeight := max(m.height-8, 3)
	cursorIdx := m.cursor
	if cursorIdx < 0 || cursorIdx >= len(m.flatRows) {
		return
	}
	if cursorIdx < m.scrollOffset {
		for m.scrollOffset > 0 && render.LastVisibleIdx(m.flatRows, m.scrollOffset-1, bodyHeight) >= cursorIdx {
			m.scrollOffset--
		}
		return
	}
	for m.scrollOffset < len(m.flatRows) && render.LastVisibleIdx(m.flatRows, m.scrollOffset, bodyHeight) < cursorIdx {
		m.scrollOffset++
	}
}
