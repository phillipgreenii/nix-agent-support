package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/render"
)

// TreeUpdatedMsg carries the latest aggregate.Tree from the daemon watcher
// plus view-state fields populated from DaemonState proto fields.
type TreeUpdatedMsg struct {
	Tree              *aggregate.Tree
	AutoResumeEnabled bool
	AutoResumeDelay   time.Duration
}

// CaffeinateResultMsg is dispatched by the Cmd returned from
// Options.OnCaffeinateToggle when the daemon's Caffeinate RPC commits.
// Carrying the post-action active state from the RPC response lets the
// TUI lock its local toggle to the daemon's truth without an optimistic
// flip + race-guard dance (which caused visible flapping on press).
type CaffeinateResultMsg struct {
	Active bool
}

// CaffeinateErrMsg is dispatched when the Caffeinate RPC fails. The TUI
// logs via the shared ErrorLogger; local toggle state is left unchanged.
type CaffeinateErrMsg struct {
	Err error
}

// AutoResumeResultMsg / AutoResumeErrMsg mirror Caffeinate{Result,Err}Msg
// for the R-keybinding's SetAutoResume RPC path.
type AutoResumeResultMsg struct {
	Enabled bool
}

type AutoResumeErrMsg struct {
	Err error
}

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
		m.daemonConnected = true
		m.tree = msg.tree
		m.anyWorking = msg.anyWorking
		m.autoResumeDelay = msg.meta.AutoResumeDelay
		m.daemonVersion = msg.meta.DaemonVersion
		// Adopt the daemon-reported toggle values only when the snapshot's
		// daemon timestamp is AFTER the user's last in-TUI toggle. An older
		// snapshot was captured before the SetAutoResume / Caffeinate RPC
		// committed, so its value is stale and would undo the optimistic
		// flip; ignore it. Zero DaemonNow (legacy / no MetaPoller) always
		// adopts so the local-poller / first-tick path keeps working.
		if msg.meta.DaemonNow.IsZero() || msg.meta.DaemonNow.After(m.autoResumeUserAt) {
			m.autoResumeEnabled = msg.meta.AutoResumeEnabled
		}
		if msg.meta.DaemonNow.IsZero() || msg.meta.DaemonNow.After(m.caffeinateUserAt) {
			m.caffeinateOn = msg.meta.CaffeinateActive
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
	case pollErrMsg:
		m.polling = false
		m.daemonConnected = false
		m.lastErr = msg.err
	case TreeUpdatedMsg:
		m.tree = msg.Tree
		m.autoResumeEnabled = msg.AutoResumeEnabled
		m.autoResumeDelay = msg.AutoResumeDelay
		m.rebuildFlatRows()
		m.clampCursor()
		m.syncScroll()
	case CaffeinateResultMsg:
		// The daemon's Caffeinate RPC committed; lock our local toggle to
		// the daemon-reported post-action state. caffeinateUserAt stays
		// stamped so any in-flight stale pollResultMsg won't undo us.
		m.caffeinateOn = msg.Active
		m.reporter.Push(m.buildSidebarSnapshot())
	case CaffeinateErrMsg:
		// RPC dial/send failed -- log for diagnostics, leave local toggle
		// unchanged. The user can press C again to retry.
		m.signalLog("Caffeinate RPC failed: " + msg.Err.Error())
	case AutoResumeResultMsg:
		m.autoResumeEnabled = msg.Enabled
	case AutoResumeErrMsg:
		m.signalLog("SetAutoResume RPC failed: " + msg.Err.Error())
	}
	return m, nil
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
		for m.scrollOffset > 0 && render.EffectiveLastVis(m.flatRows, m.scrollOffset-1, bodyHeight) >= cursorIdx {
			m.scrollOffset--
		}
		return
	}
	for m.scrollOffset < len(m.flatRows) && render.EffectiveLastVis(m.flatRows, m.scrollOffset, bodyHeight) < cursorIdx {
		m.scrollOffset++
	}
}
