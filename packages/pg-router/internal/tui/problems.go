// Package tui implements pg-router's operator-facing terminal UI. This file
// (bead pg2-5l2he) carries the Problems modal ("!"): an aggregated
// diagnostics view answering "what's wrong right now" in one place, without
// leaving the TUI.
//
// It is a SUPERSET/detail view, additive to every existing problem-
// signalling mechanism -- none of them are removed or changed by this file:
//
//   - the one-line attention banner (liveness.go's attentionLine),
//   - the Gates modal ("g", gates.go),
//   - the Legend modal ("l", render.LegendModal),
//   - per-row drilldown (enter on a Listener/Source row, drilldown.go),
//   - the file-only error logger (errorlog.go's ErrorLogger), which this
//     file makes viewable from inside the TUI for the first time.
//
// The bead names three things the view must include "at minimum"; this
// implementation shows exactly those three, in this order:
//
//  1. unmatched-binding types with no single-row home of their own
//     (reply.UnmatchedBindings) -- these can never appear as a Listener/
//     Source row, since by definition no configured role matched them;
//  2. current gate state, reusing gates.go's own gateModalRow so this view
//     and the Gates modal never drift into two different renderings of the
//     same fact;
//  3. the most recent tui-errors.log lines (errorlog.go's tailErrorLog).
package tui

import (
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/textsafe"
	"github.com/phillipgreenii/pg-router/internal/tui/render"
)

// problemsErrorLogTailLines is how many of the most recent tui-errors.log
// lines the Problems modal shows -- enough to give an operator real context
// without the modal degenerating into a full log viewer (that already
// exists as the raw file itself; helpFooter and this modal's own footer
// both name its path).
const problemsErrorLogTailLines = 10

// renderProblemsModal composes the Problems modal's rows and hands them to
// render.Modal -- the same scrollable/bordered/full-screen-takeover
// presentation every other modal in this package uses (gates.go's
// renderGatesModal, render.LegendModal, render.HelpModal), so the Problems
// modal scrolls, closes on esc, and clips to width/height for free.
func (m *Model) renderProblemsModal() string {
	var rows []render.ModalRow
	rows = append(rows, m.unmatchedBindingRows()...)
	rows = append(
		rows,
		m.gateModalRow("operator-paused", core.GateOperatorPaused),
		m.gateModalRow("cicd-down", core.GateCICDDown),
	)
	rows = append(rows, m.recentErrorLogRows()...)
	return render.Modal("Problems", rows, m.problemsModalFooter(), m.width, m.height, m.modalScrollOffset)
}

// unmatchedBindingRows renders one row per entry in reply.UnmatchedBindings
// -- the one class of problem with no single-row home anywhere else in this
// TUI (an unmatched type matched no configured role, so it can never be a
// Listener/Source row of its own). A never-observed-or-currently-empty case
// renders the explicit, unambiguous "(none)" -- never a blank/absent
// section -- mirroring gateModalRow's own "not set" precedent (pg2-y6sy5):
// an empty section must read as a confirmed fact, not as missing data.
func (m *Model) unmatchedBindingRows() []render.ModalRow {
	if len(m.reply.UnmatchedBindings) == 0 {
		return []render.ModalRow{{Left: "unmatched bindings", Right: "(none)"}}
	}
	rows := make([]render.ModalRow, 0, len(m.reply.UnmatchedBindings))
	for _, t := range m.reply.UnmatchedBindings {
		rows = append(rows, render.ModalRow{Left: "unmatched binding", Right: textsafe.Sanitize(t)})
	}
	return rows
}

// recentErrorLogRows renders up to problemsErrorLogTailLines of the most
// recent tui-errors.log lines (errorlog.go's tailErrorLog), oldest of the
// tail first. Only the first row carries the "tui-errors.log" label -- the
// rest leave Left empty so render.Modal's dynamic left-column padding lines
// every log line up under it, the same continuation-row convention
// gateModalRow's sibling rows in the Gates modal do not need but legendRows
// (render/modal.go) already establishes for multi-row groupings.
func (m *Model) recentErrorLogRows() []render.ModalRow {
	lines := tailErrorLog(m.cacheDir, problemsErrorLogTailLines)
	if len(lines) == 0 {
		return []render.ModalRow{{Left: "tui-errors.log", Right: "(no errors logged yet)"}}
	}
	rows := make([]render.ModalRow, 0, len(lines))
	for i, line := range lines {
		left := ""
		if i == 0 {
			left = "tui-errors.log"
		}
		rows = append(rows, render.ModalRow{Left: left, Right: textsafe.Sanitize(line)})
	}
	return rows
}

// problemsModalFooter names the full on-disk path of tui-errors.log, the
// same path helpFooter (help.go) already names in the [?] modal -- an
// operator who wants more than the last problemsErrorLogTailLines lines
// knows exactly where to look. Empty when Options.CacheDir was never set --
// there is nowhere to point to (mirrors helpFooter's identical guard).
func (m *Model) problemsModalFooter() string {
	if m.cacheDir == "" {
		return ""
	}
	return "Full log: " + errorLogPath(m.cacheDir)
}
