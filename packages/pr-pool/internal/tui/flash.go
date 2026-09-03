// Package tui implements pr-pool's operator-facing terminal UI. This file
// (Task 4.8) carries the TTL footer flash: info/warn levels, a newer flash
// extends (never shortens) the visible window, clipped to footer width.
// Mirrors pa-monitor's own nudge-flash mechanics (packages/pa-monitor/
// internal/tui/{model,update}.go's nudgeFlash*) under exported names, per
// this packet's own Contract.
package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/pr-pool/internal/tui/render"
)

// flashTTL bounds how long a footer flash stays visible -- pa-monitor's
// own nudgeFlashTTL constant, reused verbatim [design: Task 4.8 Step 4].
const flashTTL = 5 * time.Second

// FlashLevel selects a flash's render style [design: Task 4.8 Interfaces].
// FlashInfo is a neutral confirmation (e.g. a successful gate toggle);
// FlashWarn draws attention to a failure or an outcome the operator
// likely did not intend.
type FlashLevel int

const (
	FlashInfo FlashLevel = iota
	FlashWarn
)

// flashClearMsg is delivered by the tick flashClearCmd schedules; Update's
// flashClearMsg case (model.go) clears the flash once flashUntil has
// elapsed. A newer flash set after this tick was scheduled pushes
// flashUntil forward, so a stale tick landing is a no-op -- this is what
// makes a flash's TTL EXTEND, never shorten, across repeated sets
// [design: Task 4.8 Step 4]. Mirrors pa-monitor's own nudgeFlashClearMsg
// exactly.
type flashClearMsg struct{}

// flashClearCmd schedules the tick that will clear the CURRENTLY-active
// flash once its TTL elapses (subject to applyFlashClear's own
// re-armed-since-scheduling check).
func (m *Model) flashClearCmd() tea.Cmd {
	return tea.Tick(flashTTL, func(time.Time) tea.Msg { return flashClearMsg{} })
}

// setFlash records a transient footer message with the given level and
// (re)arms its expiry window flashTTL out from now [design: Task 4.8
// Interfaces]. Because flashUntil is always `now + flashTTL` and `now`
// only ever advances between calls, a later call can never produce an
// EARLIER flashUntil than a still-active earlier call -- the TTL can only
// ever extend, never shorten [design: Task 4.8 Step 4]: a flash set at t0
// is gone by t0+5s+eps; a second flash set at t0+3s extends (never
// shortens) the visible window past what the first alone would have
// given.
func (m *Model) setFlash(msg string, level FlashLevel) {
	m.flash = msg
	m.flashLevel = level
	m.flashUntil = time.Now().Add(flashTTL)
}

// applyFlashClear implements Update's flashClearMsg case: only clear if
// the active flash has actually expired -- a newer flash (set after this
// tick was scheduled) must survive its own TTL rather than being wiped by
// a stale tick from an earlier one.
func (m *Model) applyFlashClear() {
	if !time.Now().Before(m.flashUntil) {
		m.flash = ""
	}
}

// flashText returns the current flash text clipped to width visible
// columns (render.Line), or "" when no flash is active or it has already
// expired [design: Task 4.8 Files: "clipped to footer width"]. The main
// screen's own footer rendering (Task 4.6, out of scope here) calls this
// once it exists; provided now as a stable, independently-tested seam.
func (m *Model) flashText(width int) string {
	if m.flash == "" || !time.Now().Before(m.flashUntil) {
		return ""
	}
	return render.Line(m.flash, width)
}
