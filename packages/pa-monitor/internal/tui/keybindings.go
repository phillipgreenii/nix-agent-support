package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
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
	{Keys: []string{"N"}, Description: "Nudge (cursor scope; root = all sessions)", Handle: handleManualResume},
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
	if m.selected != nil {
		// Details viewport scrolls instead of moving the underlying list cursor.
		// Clamping to the actual content length happens in the renderer.
		m.detailsScrollOffset++
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
	if m.selected != nil {
		if m.detailsScrollOffset > 0 {
			m.detailsScrollOffset--
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
		m.detailsScrollOffset = 0
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
	m.detailsScrollOffset = 0
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

// handleToggleCaffeinate stamps caffeinateUserAt (for the pollResultMsg
// race guard) and returns the Cmd that performs the Caffeinate RPC. The
// Cmd emits a CaffeinateResultMsg with the daemon-reported new state,
// which Update applies to m.caffeinateOn -- no optimistic local flip,
// so the toggle can never flap on press.
//
// When no callback is wired (tests / non-remote contexts) we fall back
// to a local flip so the previous contract -- "C flips the toggle" --
// keeps holding without a daemon attached.
func handleToggleCaffeinate(m *Model) tea.Cmd {
	want := !m.caffeinateOn
	m.caffeinateUserAt = time.Now()
	if m.onCaffeinateToggle != nil {
		return m.onCaffeinateToggle(want)
	}
	m.caffeinateOn = want
	m.reporter.Push(m.buildSidebarSnapshot())
	return nil
}

// handleToggleAutoResume mirrors handleToggleCaffeinate for the R key.
// The daemon's SetAutoResume RPC returns the post-action state in its
// response; the returned Cmd dispatches an AutoResumeResultMsg from it.
func handleToggleAutoResume(m *Model) tea.Cmd {
	want := !m.autoResumeEnabled
	m.autoResumeUserAt = time.Now()
	if m.onToggleAutoResume != nil {
		return m.onToggleAutoResume(want)
	}
	m.autoResumeEnabled = want
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

// sessionStatusWorking reports whether the session identified by sid is
// Working in the current snapshot. Used to warn that a freshly-queued manual
// nudge will be suppressed (session_active) at the next dispatch tick.
func (m *Model) sessionStatusWorking(sid string) bool {
	return m.sessionHasStatus(sid, session.Working)
}

// sessionStatusWaitingForHuman reports whether the session identified by sid is
// blocked on a human (blocker ∈ human_*) in the current snapshot. The daemon
// dispatcher suppresses manual nudges for this too (waiting_for_human),
// symmetrically with Working — see internal/daemon/nudger/dispatcher.go.
// Surfacing it keeps the N feedback honest instead of implying delivery
// (pg2-0cmq / pg2-gweng; ADR 0024 R4).
func (m *Model) sessionStatusWaitingForHuman(sid string) bool {
	if m.tree == nil {
		return false
	}
	for _, d := range m.tree.Dirs {
		for _, sv := range d.Sessions {
			if sv.Session == nil || sv.SessionID != sid {
				continue
			}
			return sv.Status == session.Blocked && sv.Session.Blocker.IsHuman()
		}
	}
	return false
}

// sessionHasStatus reports whether the session identified by sid has the given
// status in the current snapshot. Shared read-side lookup (nil-guarded) for the
// suppression predicates above.
func (m *Model) sessionHasStatus(sid string, status session.Status) bool {
	if m.tree == nil {
		return false
	}
	for _, d := range m.tree.Dirs {
		for _, sv := range d.Sessions {
			if sv.Session == nil || sv.SessionID != sid {
				continue
			}
			return sv.Status == status
		}
	}
	return false
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
	if len(sids) == 0 {
		// Surface rather than silently no-op (pg2-0cmq): the cursor is on a
		// blank/empty row with no session to nudge.
		m.setNudgeFlash("nudge: no session under cursor", flashWarn)
		return nudgeFlashClearCmd()
	}
	if m.onManualNudge == nil {
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
	// The callback runs the RPC and emits a NudgeResultMsg/NudgeErrMsg the
	// Update loop turns into a footer flash. The selector carries the CURRENT
	// session id straight off the live snapshot row, so it is inherently
	// tolerant of daemon-side session-id churn.
	cmd := m.onManualNudge(selector, allPending)
	if cmd != nil {
		// Mark the nudge in flight so a self-restart quit is deferred until its
		// result arrives (cleared in the NudgeResultMsg/NudgeErrMsg cases).
		m.nudgePending = true
	}
	return cmd
}
