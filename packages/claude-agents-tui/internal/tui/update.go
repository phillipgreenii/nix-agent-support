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
		case "enter":
			m.selected = m.sessionAt(m.cursor)
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
		m.clampCursor()
		m.syncScroll()
	}
	return m, nil
}

// syncScroll adjusts scrollOffset so that the cursor's row is within the
// visible window. Uses a conservative body-height estimate when the terminal
// size is not yet known exactly (before the first WindowSizeMsg).
func (m *Model) syncScroll() {
	if m.tree == nil || m.height == 0 {
		return
	}
	opts := render.TreeOpts{ShowAll: m.showAll}
	rows := render.FlattenRows(m.tree, opts)
	if len(rows) == 0 {
		return
	}
	// Subtract max header lines (7) + legend (1) for a conservative estimate.
	bodyHeight := max(m.height-8, 3)
	cursorRowIdx := -1
	for i, r := range rows {
		if r.Kind == render.SessionKind && r.FlatIdx == m.cursor {
			cursorRowIdx = i
			break
		}
	}
	if cursorRowIdx < 0 {
		return
	}
	if cursorRowIdx < m.scrollOffset {
		// Scroll back: find the largest offset where the cursor row is still visible.
		// Walk backward from cursorRowIdx until LastVisibleIdx covers cursorRowIdx.
		for m.scrollOffset > 0 && render.LastVisibleIdx(rows, m.scrollOffset-1, bodyHeight) >= cursorRowIdx {
			m.scrollOffset--
		}
		return
	}
	for m.scrollOffset < len(rows) && render.LastVisibleIdx(rows, m.scrollOffset, bodyHeight) < cursorRowIdx {
		m.scrollOffset++
	}
}
