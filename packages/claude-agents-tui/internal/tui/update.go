package tui

import (
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/render"
	"github.com/phillipgreenii/claude-agents-tui/internal/session"
	"github.com/phillipgreenii/claude-agents-tui/internal/signal"
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
		case "R":
			m.autoResume = !m.autoResume
			if m.autoResume && !m.tree.WindowResetsAt.IsZero() && !m.autoResumeFired && !m.countdownTick {
				m.countdownTick = true
				return m, countdownTickCmd()
			}
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
			// PathNodeKind → no-op
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
		if m.autoResumeFired && msg.tree.WindowResetsAt.IsZero() {
			m.autoResumeFired = false
		}
		if m.autoResume && !msg.tree.WindowResetsAt.IsZero() && !m.autoResumeFired {
			fireAt := msg.tree.WindowResetsAt.Add(m.autoResumeDelay)
			cmds := []tea.Cmd{autoResumeFireCmd(fireAt)}
			if !m.countdownTick {
				m.countdownTick = true
				cmds = append(cmds, countdownTickCmd())
			}
			return m, tea.Batch(cmds...)
		}
	case pollErrMsg:
		m.polling = false
		m.lastErr = msg.err
	case countdownTickMsg:
		if m.autoResume && m.tree != nil && !m.tree.WindowResetsAt.IsZero() && !m.autoResumeFired {
			return m, countdownTickCmd()
		}
		m.countdownTick = false

	case autoResumeFireMsg:
		if !m.autoResume || m.tree == nil || m.tree.WindowResetsAt.IsZero() || m.autoResumeFired {
			return m, nil
		}
		fireAt := m.tree.WindowResetsAt.Add(m.autoResumeDelay)
		if time.Now().Before(fireAt) {
			return m, nil
		}
		for _, d := range m.tree.Dirs {
			for _, sv := range d.Sessions {
				if sv.Status == session.Working {
					continue
				}
				sig := signal.ResolveSignaler(m.signalers, sv.PID)
				if sig == nil {
					fmt.Fprintf(os.Stderr, "auto-resume: no signaler for pid %d\n", sv.PID)
					continue
				}
				if err := sig.Send(sv.PID, m.autoResumeMessage); err != nil {
					fmt.Fprintf(os.Stderr, "auto-resume: send failed pid %d: %v\n", sv.PID, err)
				}
			}
		}
		m.autoResumeFired = true
		m.countdownTick = false
		m.tree.WindowResetsAt = time.Time{}
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
