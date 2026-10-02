package tui

import (
	"strings"
	"unicode/utf8"

	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/textsafe"
	"github.com/phillipgreenii/pg-router/internal/tui/render"
)

// This file surfaces the event-log size limit's LIMITED-CAPABILITY states (bead
// pg2-5d3ui). The soft step is not a gate, so neither the gates line nor the
// PAUSED banner says anything about it; this zone (and the Problems modal) is
// where an operator learns what is HALTED, what still RUNS, why, and what to do.
// The wording comes from core.LogLimitNotice, the same function `pg-router status`
// renders, so the two surfaces cannot drift.

// logLimitDropOrder places the zone above the unfocused panes (drop orders 1-3) so
// it is the last non-pinned zone to go under height pressure: an operator who has
// lost the Queues pane should still be told the daemon is refusing events.
const logLimitDropOrder = 4

// view converts l for the shared operator-notice wording.
func (l QueueLog) view() core.LogLimitView {
	return core.LogLimitView{
		Bytes: l.Bytes, LimitBytes: l.LimitBytes, SoftBytes: l.SoftBytes, State: l.State,
		RejectedLogFull: l.Rejected.LogFull, RejectedLogUnwritable: l.Rejected.LogUnwritable, Detail: l.Detail,
	}
}

// logLimitNoticeLines returns the notice for l word-wrapped to width columns, or
// nil when the log is healthy. Each logical line starts at column 0 (the headline
// with "! "), continuations are indented two columns.
func logLimitNoticeLines(l QueueLog, width int) []string {
	notice := core.LogLimitNotice(l.view())
	if len(notice) == 0 {
		return nil
	}
	var out []string
	for i, line := range notice {
		prefix := "  "
		if i == 0 {
			prefix = "! "
		}
		out = append(out, wrapWords(textsafe.Sanitize(prefix+line), width, "  ")...)
	}
	return out
}

// logLimitZone renders the limited-capability zone: empty when the log is healthy
// (the caller then omits the zone), else the wrapped notice in the failing style.
func logLimitZone(l QueueLog, width int, theme render.Theme) string {
	lines := logLimitNoticeLines(l, render.EffectiveWidth(width))
	if len(lines) == 0 {
		return ""
	}
	for i, line := range lines {
		lines[i] = theme.Failing.Render(line)
	}
	return strings.Join(lines, "\n")
}

// logLimitRows renders the same notice as Problems-modal rows (the first row
// carries the "log limit" label; with a healthy log a single explicit row reads
// as a confirmed fact, like the other sections of that modal).
func (m *Model) logLimitRows() []render.ModalRow {
	width := m.width - 24
	if width < 20 {
		width = 20
	}
	lines := logLimitNoticeLines(m.reply.QueueLog, width)
	if len(lines) == 0 {
		return []render.ModalRow{{Left: "log limit", Right: "ok"}}
	}
	rows := make([]render.ModalRow, len(lines))
	for i, line := range lines {
		left := ""
		if i == 0 {
			left = "log limit"
		}
		rows[i] = render.ModalRow{Left: left, Right: line}
	}
	return rows
}

// wrapWords word-wraps s to width columns (runes), indenting continuation lines
// with indent. A single word longer than the width is left whole (the renderer
// clips it). width <= 0 disables wrapping.
func wrapWords(s string, width int, indent string) []string {
	if width <= 0 || utf8.RuneCountInString(s) <= width {
		return []string{s}
	}
	var lines []string
	cur := ""
	for _, w := range strings.Fields(s) {
		switch {
		case cur == "":
			cur = w
		case utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(w) <= width:
			cur += " " + w
		default:
			lines = append(lines, cur)
			cur = indent + w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}
