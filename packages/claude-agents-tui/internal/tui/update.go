package tui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/claude-agents-tui/internal/core/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/render"
	"github.com/phillipgreenii/claude-agents-tui/internal/core/session"
	"github.com/phillipgreenii/claude-agents-tui/internal/signal"
)

type TreeUpdatedMsg struct{ Tree *aggregate.Tree }

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if isQuit(msg) {
			m.reporter.Clear()
			return m, tea.Quit
		}
		s := msg.String()
		for _, b := range Bindings {
			for _, k := range b.Keys {
				if k == s {
					if cmd := b.Handle(m); cmd != nil {
						return m, cmd
					}
					return m, nil
				}
			}
		}
		// No matching binding — no-op fall-through.
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
		m.tickCount++
		n := m.sidebarIntervalTicks
		if n <= 0 {
			n = 1
		}
		if m.tickCount%n == 0 {
			m.reporter.Push(m.buildSidebarSnapshot())
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
		n := m.signalNonWorkingAndCount("auto-resume")
		m.reporter.Notify("claude-agents-tui",
			fmt.Sprintf("5h window reset. Nudged %d idle session(s) to continue.", n))
		m.autoResumeFired = true
		m.countdownTick = false
		// Shallow-copy to avoid mutating the shared tree pointer.
		t := *m.tree
		t.WindowResetsAt = time.Time{}
		m.tree = &t
	case TreeUpdatedMsg:
		m.tree = msg.Tree
		m.rebuildFlatRows()
		m.clampCursor()
		m.syncScroll()
	}
	return m, nil
}

// signalNonWorkingAndCount mirrors signalNonWorking but returns the number of
// non-Working sessions iterated. Used by the auto-resume notification body so
// the count of nudged sessions can be reported to the user.
func (m *Model) signalNonWorkingAndCount(label string) int {
	if m.tree == nil {
		return 0
	}
	count := 0
	for _, d := range m.tree.Dirs {
		for _, sv := range d.Sessions {
			if sv.Status == session.Working {
				continue
			}
			count++
			sig := signal.ResolveSignaler(m.signalers, sv.PID)
			if sig == nil {
				m.signalLog(fmt.Sprintf("%s: no signaler for pid %d", label, sv.PID))
				continue
			}
			if err := sig.Send(sv.PID, m.autoResumeMessage); err != nil {
				m.signalLog(fmt.Sprintf("%s: send failed pid %d: %v", label, sv.PID, err))
			}
		}
	}
	return count
}

// signalNonWorking sends m.autoResumeMessage to every non-Working session via
// the resolved signaler. label is the log-prefix used for stderr diagnostics
// ("auto-resume" or "manual-resume"). No-op when m.tree is nil.
func (m *Model) signalNonWorking(label string) {
	if m.tree == nil {
		return
	}
	for _, d := range m.tree.Dirs {
		for _, sv := range d.Sessions {
			if sv.Status == session.Working {
				continue
			}
			sig := signal.ResolveSignaler(m.signalers, sv.PID)
			if sig == nil {
				m.signalLog(fmt.Sprintf("%s: no signaler for pid %d", label, sv.PID))
				continue
			}
			if err := sig.Send(sv.PID, m.autoResumeMessage); err != nil {
				m.signalLog(fmt.Sprintf("%s: send failed pid %d: %v", label, sv.PID, err))
			}
		}
	}
}

// syncScroll adjusts scrollOffset so the cursor row is within the visible window.
func (m *Model) syncScroll() {
	if m.height == 0 || len(m.flatRows) == 0 {
		return
	}
	bodyHeight := max(m.height-4, 3)
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
