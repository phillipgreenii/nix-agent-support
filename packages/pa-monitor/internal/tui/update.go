package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/render"
)

// nudgeFlashTTL bounds how long the manual-nudge outcome stays on the footer.
const nudgeFlashTTL = 5 * time.Second

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

// NudgeResultMsg carries the outcome of a manual nudge (N key) back to the
// Update loop so it can be surfaced in the footer. Cancel distinguishes the
// NudgeCancel path (toggle-off) from NudgeQueue; the slices mirror the RPC
// response fields. Previously the N handler fired-and-forgot the RPC, so a
// no-match / working-suppressed / already-queued outcome was a silent no-op
// (pg2-0cmq).
type NudgeResultMsg struct {
	Cancel    bool
	Queued    []string
	Already   []string
	Cancelled []string
}

// NudgeErrMsg is dispatched when the NudgeQueue/NudgeCancel RPC fails. Logged
// via the shared ErrorLogger and surfaced as a warning flash.
type NudgeErrMsg struct {
	Err error
}

// nudgeFlashClearMsg is delivered by the tick scheduled when a flash is set;
// it clears the flash once nudgeFlashUntil has elapsed. A newer flash (set
// after this tick was scheduled) pushes nudgeFlashUntil forward, so a stale
// tick is a no-op.
type nudgeFlashClearMsg struct{}

func nudgeFlashClearCmd() tea.Cmd {
	return tea.Tick(nudgeFlashTTL, func(time.Time) tea.Msg { return nudgeFlashClearMsg{} })
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
		// Clear any prior error so a single transient poll failure cannot latch
		// the offline screen (view.go gates it on tree==nil && lastErr!=nil) once
		// the daemon is reachable again.
		m.lastErr = nil
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
			m.caffeinateOn = msg.meta.CaffeinateMode
		}
		// The PROCESS indicator + grace countdown are display-only and not
		// user-controlled (C only toggles the MODE), so adopt them unguarded.
		m.caffeinateProcess = msg.meta.CaffeinateProcess
		m.caffeinateGraceRemaining = msg.meta.CaffeinateGraceRemaining
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
		// After daemonVersion is refreshed, decide whether to self-restart. A
		// tea.Quit here hands control to runTUIRemote, which performs the exec.
		if cmd := m.evalReexec(); cmd != nil {
			return m, cmd
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
	case NudgeResultMsg:
		m.nudgePending = false
		text, level := m.formatNudgeResult(msg)
		m.setNudgeFlash(text, level)
		return m, nudgeFlashClearCmd()
	case NudgeErrMsg:
		m.nudgePending = false
		m.signalLog("nudge RPC failed: " + msg.Err.Error())
		m.setNudgeFlash("nudge failed: "+msg.Err.Error(), flashWarn)
		return m, nudgeFlashClearCmd()
	case nudgeFlashClearMsg:
		// Only clear if the active flash has actually expired; a newer flash
		// (set after this tick was scheduled) must survive its own TTL.
		if !time.Now().Before(m.nudgeFlashUntil) {
			m.nudgeFlash = ""
		}
	}
	return m, nil
}

// setNudgeFlash records a transient footer message with the given level and
// (re)arms its expiry window.
func (m *Model) setNudgeFlash(text string, level flashLevel) {
	m.nudgeFlash = text
	m.nudgeFlashLevel = level
	m.nudgeFlashUntil = time.Now().Add(nudgeFlashTTL)
}

// formatNudgeResult renders a NudgeResultMsg into a footer line plus its
// render level. A queued nudge against a session the live snapshot reports as
// Working or WaitingForHuman is warned about explicitly: the daemon dispatcher
// suppresses both symmetrically (session_active / waiting_for_human) and drops
// such intents at dispatch time, so implying delivery would repeat the
// pg2-0cmq silent no-op (see internal/daemon/nudger/dispatcher.go).
func (m *Model) formatNudgeResult(r NudgeResultMsg) (string, flashLevel) {
	if r.Cancel {
		if len(r.Cancelled) == 0 {
			return "nudge: nothing queued to cancel", flashInfo
		}
		return fmt.Sprintf("nudge cancelled for %d session(s)", len(r.Cancelled)), flashInfo
	}
	if len(r.Queued) == 0 && len(r.Already) == 0 {
		return "nudge: no sessions matched", flashWarn
	}
	var parts []string
	if len(r.Queued) > 0 {
		parts = append(parts, fmt.Sprintf("queued for %d session(s)", len(r.Queued)))
	}
	if len(r.Already) > 0 {
		parts = append(parts, fmt.Sprintf("%d already queued", len(r.Already)))
	}
	text := "nudge " + strings.Join(parts, ", ")
	// Count queued targets the snapshot shows as suppressible at the next
	// dispatch tick: Working → session_active, WaitingForHuman →
	// waiting_for_human. Both are dropped by the dispatcher.
	working, waiting := 0, 0
	for _, sid := range r.Queued {
		switch {
		case m.sessionStatusWorking(sid):
			working++
		case m.sessionStatusWaitingForHuman(sid):
			waiting++
		}
	}
	if working+waiting > 0 {
		var supp []string
		if working > 0 {
			supp = append(supp, fmt.Sprintf("%d working", working))
		}
		if waiting > 0 {
			supp = append(supp, fmt.Sprintf("%d waiting for human", waiting))
		}
		return fmt.Sprintf("%s — %s, suppressed until idle", text, strings.Join(supp, ", ")), flashWarn
	}
	return text, flashInfo
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
