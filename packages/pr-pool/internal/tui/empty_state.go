// Package tui implements pr-pool's operator-facing terminal UI. This file
// (Task 4.6) carries the three-way empty-state precedence: unknown
// (pre-first-poll) beats suppressed (gate-halted) beats empty
// [design: Task 4.6 Files (empty_state.go); ux-9].
package tui

import "github.com/phillipgreenii/pr-pool/internal/tui/render"

// emptyState names which of the three empty-state facts currently applies
// to a pane (or stateNotEmpty when the pane actually has rows to show).
type emptyState int

const (
	stateNotEmpty emptyState = iota
	stateUnknown
	stateSuppressed
	stateEmpty
)

// resolveEmptyState applies the fixed precedence unknown > suppressed >
// empty [design: Task 4.6 Step 6]. unknown is pre-first-poll ("loading…"
// -- in practice never true for a pane rendered from screenMain, since
// screenMain is only entered after a successful poll; kept as an explicit
// input so the ranking itself stays directly testable). suppressed applies
// ONLY to Queues/Deliveries/Activity panes when the pool is gated --
// config-derived panes (Listeners/Sources/Registry) are NEVER suppressed,
// so callers for those panes must always pass suppressed=false.
func resolveEmptyState(unknown, suppressed, empty bool) emptyState {
	switch {
	case unknown:
		return stateUnknown
	case suppressed:
		return stateSuppressed
	case empty:
		return stateEmpty
	default:
		return stateNotEmpty
	}
}

// emptyStateText renders s's placeholder line, or "" for stateNotEmpty
// (the caller has real rows to show instead). emptyMsg is the pane's own
// "nothing to show" wording (e.g. "No events queued.") used only for
// stateEmpty -- the unknown/suppressed wordings are fixed, since they name
// a TUI-level fact rather than anything pane-specific.
func emptyStateText(s emptyState, emptyMsg string) string {
	switch s {
	case stateUnknown:
		return "loading…"
	case stateSuppressed:
		return "(suppressed — dispatch halted)"
	case stateEmpty:
		return emptyMsg
	default:
		return ""
	}
}

// dimIfPaused wraps content in theme's Muted style when paused is true --
// config-derived panes (Listeners/Sources/Registry) stay POPULATED while
// paused (never suppressed, per resolveEmptyState's own doc), just dimmed:
// the rows keep telling the truth about themselves even while dispatch is
// halted [design: Task 4.6 (§5 Derived health)].
func dimIfPaused(content string, paused bool, theme render.Theme) string {
	if !paused || content == "" {
		return content
	}
	return theme.Muted.Render(content)
}
