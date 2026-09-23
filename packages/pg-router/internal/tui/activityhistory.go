// Package tui implements pg-router's operator-facing terminal UI. This file
// (bead pg2-gb21e) makes the Activity pane focusable/openable: the "a" key
// opens a dedicated Activity History modal listing more entries than the
// main pane's activityDisplayCap (8) -- up to the activity ring's own
// defaultReadWindow (64, internal/activity/ring.go), sourced from a
// DEDICATED Snapshot(ctx, activityHistoryReadSince) call rather than the
// regular polling loop's own m.sinceCursor, which after the very first poll
// only ever asks for what is new since last time (poll.go's pollNow/
// advanceSinceCursor) -- exactly the narrowing this bead's fix must bypass.
//
// model.go's paneActivity sentinel (never part of the tab/shift+tab
// pane-focus cycle) and drilldown.go's focusableRowKind (no per-row cursor
// for Activity) are both left exactly as they were: this file gives
// Activity its OWN dedicated key/screen instead, the design's own freedom
// boundary [bd show pg2-gb21e]. It reuses the existing openModal/esc/
// ModalKind machinery (keybindings.go's handleEsc already restores
// m.preModalScreen -- "esc returns to main" falls out of that unchanged)
// rather than inventing a new screen enum value.
package tui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/pg-router/internal/textsafe"
	"github.com/phillipgreenii/pg-router/internal/tui/render"
)

// activityHistoryReadSince is the cursor the "a" key's dedicated Snapshot
// call always passes: 0, the ring's own "no prior cursor" request shape
// (internal/activity/ring.go's Read doc: "since == 0 ... returns the
// newest min(defaultReadWindow, held) entries"). Deliberately NEVER
// m.sinceCursor -- the regular polling loop (poll.go) advances that every
// tick so it only ever asks for what's new; a wider history needs its own,
// separately-sourced request. Named at its one call site rather than a
// bare literal 0.
const activityHistoryReadSince = 0

// activityHistoryTimeout bounds the "a" key's dedicated Snapshot round
// trip, mirroring pollTimeout (poll.go) / gateToggleTimeout (gates.go)'s
// identical safety-net role above the Poller's own internal RPC deadline.
const activityHistoryTimeout = 10 * time.Second

// activityHistoryResultMsg/activityHistoryErrMsg carry the dedicated
// Snapshot(ctx, activityHistoryReadSince) call's outcome back to Update.
// Deliberately NOT pollResultMsg/pollErrMsg: routing this reply through
// applyPollResult would reset m.reply wholesale and advance m.sinceCursor
// from THIS reply's own Activity slice (model.go's advanceSinceCursor),
// corrupting the regular polling loop's own since-cursor bookkeeping --
// this is a side-channel fetch for one modal's content only.
type (
	activityHistoryResultMsg struct {
		entries []ActivityEntry
		dropped bool
	}
	activityHistoryErrMsg struct{ err error }
)

// handleOpenActivityHistory implements the "a" key: opens the Activity
// History modal immediately -- renderActivityHistoryModal's own fallback
// (m.activityBuffer, then m.reply.Activity) means the screen never renders
// empty while the wider fetch is in flight -- and, when a real Poller is
// wired, kicks off the dedicated wider-window fetch. A nil Poller (a test
// driving Update by hand, or polling disabled) is not an error here, the
// same no-op contract startGateToggle (gates.go) already establishes for
// its own RPC: there is simply nothing wider to fetch, so the modal shows
// exactly what the main pane already showed.
func handleOpenActivityHistory(m *Model) tea.Cmd {
	m.openModal(ModalActivityHistory)
	if m.poller == nil {
		return nil
	}
	poller := m.poller
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), activityHistoryTimeout)
		defer cancel()
		reply, err := poller.Snapshot(ctx, activityHistoryReadSince)
		if err != nil {
			return activityHistoryErrMsg{err: err}
		}
		return activityHistoryResultMsg{entries: reply.Activity, dropped: reply.ActivityDropped}
	}
}

// applyActivityHistoryResult is Update's activityHistoryResultMsg handler:
// stores the wider fetch's entries for renderActivityHistoryModal. Applied
// unconditionally regardless of which modal/screen is current by the time
// it arrives (mirrors applyGateToggleResult's identical choice, gates.go) --
// a stale result from a modal the operator already closed is harmless to
// store, and only matters if the operator reopens this one.
func (m *Model) applyActivityHistoryResult(msg activityHistoryResultMsg) {
	m.activityHistory = msg.entries
	m.activityHistoryDropped = msg.dropped
}

// applyActivityHistoryErr is Update's activityHistoryErrMsg handler: logs
// the failure and leaves m.activityHistory exactly where it was -- never
// blanks a screen the operator already has open over one transient poll
// failure.
func (m *Model) applyActivityHistoryErr(msg activityHistoryErrMsg) {
	m.errorLogger.LogString("activity history fetch failed: " + msg.err.Error())
}

// renderActivityHistoryModal renders the Activity History modal: every
// buffered/fetched entry, newest first (matching renderActivityPane's own
// convention, panes.go), each as one ModalRow -- Left the relative
// timestamp, Right the type + outcome -- reusing formatCoarse/
// renderActivityOutcome (panes.go) so this view can never render a fact
// differently than the pane row it was opened from.
//
// Falls back to m.activityBuffer (or m.reply.Activity -- panes.go's
// renderActivityZoneContent's own identical fallback order) until the
// dedicated wider fetch (handleOpenActivityHistory) actually lands:
// m.activityHistory is nil until then, so this always renders SOMETHING.
func (m *Model) renderActivityHistoryModal() string {
	entries := m.activityHistory
	if entries == nil {
		entries = m.activityBuffer
		if entries == nil {
			entries = m.reply.Activity
		}
	}

	rows := make([]render.ModalRow, 0, len(entries)+1)
	if m.activityHistoryDropped {
		rows = append(rows, render.ModalRow{Right: "(older entries dropped -- ring capacity exceeded)"})
	}
	now := time.Now()
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		ts := "-"
		if !e.StartedAt.IsZero() {
			ts = formatCoarse(now.Sub(e.StartedAt)) + " ago"
		}
		right := textsafe.Sanitize(e.Type)
		if e.Outcome != "" {
			right += " → " + renderActivityOutcome(e.Outcome, m.theme)
		}
		rows = append(rows, render.ModalRow{Left: ts, Right: right})
	}

	footer := fmt.Sprintf("%d entries shown (main pane shows only the most recent %d)", len(entries), activityDisplayCap)
	return render.Modal("Activity History", rows, footer, m.width, m.height, m.modalScrollOffset)
}
