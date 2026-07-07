package nudger

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

// Signaler delivers a nudge text to a process by PID. Wraps the existing
// signal layer (tmux/cmux/ghostty/vscode).
type Signaler interface {
	Send(pid int, text string) error
}

// Recorder receives observability + persistence signals from the
// dispatcher. Concrete impls live in the daemon wiring.
type Recorder interface {
	RecordSuppressed(sid string, sources []Source, cause string)
	RecordSent(sid string, sources []Source, errorKind string, escalated bool)
	// UpdateWatermarks records the fact that sources fired for sid at now.
	// sources is the slice of every Source delivered in the single Signaler.Send
	// (multiple intents can coalesce per session). The recorder is responsible
	// for persisting LastNudgedAt + LastNudgeSources alongside the disrupt
	// watermarks driven by cause.
	UpdateWatermarks(sid string, now time.Time, sources []Source, cause *transcript.ErrorRecord, escalated bool)
	// AdvanceWindowResetFiredFor records that the window-reset nudge for
	// WindowResetsAt=at fired this tick. Called by the dispatcher exactly
	// once per tick when any session with SourceWindowReset is dispatched.
	AdvanceWindowResetFiredFor(at time.Time)
	// RecordDisruptAttempt persists the LastDisruptAttemptAt watermark for a
	// disrupt nudge ATTEMPT — called on BOTH the success and failure path so
	// the D5 error keep-awake releases after the first attempt (even a failed
	// one). Only called for groups carrying a SourceDisrupted intent.
	RecordDisruptAttempt(sid string, at time.Time)
	// RecordSendFailed reports that Signaler.Send returned an error for sid,
	// so the failed delivery is observable (OTel counter + log) instead of
	// being silently swallowed. errorKind is the disrupt cause's kind (empty
	// for non-disrupt nudges); errText is the signaler error string.
	RecordSendFailed(sid string, sources []Source, errorKind, errText string)
	// RecordQueued increments pa_monitor.nudge.queued_total once for each
	// newly-added intent. Called by Nudger.Reconcile after diffing the
	// pre/post pending-store key sets.
	RecordQueued(sid string, source Source)
}

// NudgeRecorder is the persistence hook for the dispatcher. The daemon
// wires a WriteService-backed implementation; the nudger itself doesn't
// know about SQLite.
type NudgeRecorder interface {
	Record(ctx context.Context, ev RecordEvent) error
}

// RecordEvent carries the data for one dispatched (or suppressed) nudge
// that should be persisted to the nudge_history table.
type RecordEvent struct {
	SessionID       string // string id from session.json; recorder maps to surrogate
	Text            string
	Result          string // 'sent' | 'failed' | 'suppressed' | 'escalated'
	ErrorText       string
	CausedByErrorAt *time.Time
	Escalated       bool
	FiredAt         time.Time
	Sources         []string
}

// Dispatcher fires nudges based on the pending store, performs the
// active-session suppression check, and clears intents after success or
// suppression. Send failures leave intents for the next tick.
type Dispatcher struct {
	Signaler      Signaler
	Recorder      Recorder
	NudgeRecorder NudgeRecorder
	// HistoryErrLog, if non-nil, receives a one-line message whenever
	// NudgeRecorder.Record returns an error. The nudge_history write is the
	// export-INDEPENDENT capture sink (the OTel counter/log path is separate
	// and may be silently failing to export); when it too fails the row is
	// lost, so this hook surfaces the failure to a durable local sink instead
	// of discarding the error. nil in tests / early-startup paths.
	HistoryErrLog func(msg string)
}

// Dispatch iterates pending intents once, grouped by session.
func (d *Dispatcher) Dispatch(goCtx context.Context, ctx TickContext, store *PendingStore) {
	intents := store.List()
	if len(intents) == 0 {
		return
	}
	bySession := map[string][]NudgeIntent{}
	// keysBySession tracks the exact keys observed at List() time so
	// RemoveKeys can target only those keys, avoiding a TOCTOU race where a
	// concurrent NudgeQueue adds an intent between List and removal.
	keysBySession := map[string][]IntentKey{}
	for _, in := range intents {
		bySession[in.Key.SessionID] = append(bySession[in.Key.SessionID], in)
		keysBySession[in.Key.SessionID] = append(keysBySession[in.Key.SessionID], in.Key)
	}
	sids := make([]string, 0, len(bySession))
	for sid := range bySession {
		sids = append(sids, sid)
	}
	sort.Strings(sids)

	sessionsByID := indexSessions(ctx.Tree)
	windowResetDispatched := false
	for _, sid := range sids {
		group := bySession[sid]
		observedKeys := keysBySession[sid]
		sources := make([]Source, 0, len(group))
		for _, in := range group {
			sources = append(sources, in.Key.Source)
		}

		view, ok := sessionsByID[sid]
		if !ok {
			store.RemoveKeys(observedKeys)
			continue
		}

		// Extract disrupt metadata, the resolved text, and the escalation flag up
		// front so every outcome path (suppressed / failed / sent) can persist a
		// complete nudge_history row and label its observability events with the
		// cause kind. A disrupt attempt is recorded on BOTH send paths (see
		// RecordDisruptAttempt) so the D5 error keep-awake releases after the
		// first attempt, even a failed one.
		hasDisrupt := false
		var cause *transcript.ErrorRecord
		var kind string
		var causeAt *time.Time
		for _, in := range group {
			if in.Key.Source != SourceDisrupted {
				continue
			}
			hasDisrupt = true
			if cause == nil && in.Cause != nil {
				cause = in.Cause
				kind = string(in.Cause.Kind)
				t := in.Cause.At
				causeAt = &t
			}
		}
		text := resolveText(group)
		escalated := ctx.Watermarks.SessionWatermark(sid).DisruptEscalated

		if view.Status == session.Working {
			d.Recorder.RecordSuppressed(sid, sources, "session_active")
			d.recordHistory(goCtx, RecordEvent{
				SessionID: sid, Text: text, Result: "suppressed", ErrorText: "session_active",
				CausedByErrorAt: causeAt, Escalated: escalated, FiredAt: ctx.Now, Sources: sourceStrings(sources),
			})
			store.RemoveKeys(observedKeys)
			continue
		}
		// Never inject "continue" over a permission prompt / AskUserQuestion —
		// the session is blocked on a human, not on a recoverable error (§6/D3).
		if view.Status == session.WaitingForHuman {
			d.Recorder.RecordSuppressed(sid, sources, "waiting_for_human")
			d.recordHistory(goCtx, RecordEvent{
				SessionID: sid, Text: text, Result: "suppressed", ErrorText: "waiting_for_human",
				CausedByErrorAt: causeAt, Escalated: escalated, FiredAt: ctx.Now, Sources: sourceStrings(sources),
			})
			store.RemoveKeys(observedKeys)
			continue
		}

		if err := d.Signaler.Send(view.PID, text); err != nil {
			// Delivery failed. Surface it for observability (OTel counter + log)
			// — otherwise the error is swallowed and the miss is invisible —
			// persist a 'failed' nudge_history row, record the attempt, and leave
			// the intents in place to retry next tick.
			d.Recorder.RecordSendFailed(sid, sources, kind, err.Error())
			d.recordHistory(goCtx, RecordEvent{
				SessionID: sid, Text: text, Result: "failed", ErrorText: err.Error(),
				CausedByErrorAt: causeAt, Escalated: escalated, FiredAt: ctx.Now, Sources: sourceStrings(sources),
			})
			if hasDisrupt {
				d.Recorder.RecordDisruptAttempt(sid, ctx.Now)
			}
			continue
		}
		if hasDisrupt {
			d.Recorder.RecordDisruptAttempt(sid, ctx.Now)
		}
		d.Recorder.RecordSent(sid, sources, kind, escalated)
		d.Recorder.UpdateWatermarks(sid, ctx.Now, sources, cause, escalated)
		d.recordHistory(goCtx, RecordEvent{
			SessionID: sid, Text: text, Result: "sent", ErrorText: kind,
			CausedByErrorAt: causeAt, Escalated: escalated, FiredAt: ctx.Now, Sources: sourceStrings(sources),
		})
		store.RemoveKeys(observedKeys)
		for _, in := range group {
			if in.Key.Source == SourceWindowReset {
				windowResetDispatched = true
				break
			}
		}
	}
	// Advance the window-reset latch only when a SourceWindowReset actually fired.
	if windowResetDispatched && !ctx.Tree.WindowResetsAt.IsZero() {
		d.Recorder.AdvanceWindowResetFiredFor(ctx.Tree.WindowResetsAt)
	}
}

// recordHistory persists one nudge_history row via the NudgeRecorder. No-op
// when the recorder is unset (early-startup paths and tests). Used for the
// sent, failed, and suppressed outcomes so historical queries surface misses
// (failed / suppressed) as well as successful deliveries.
func (d *Dispatcher) recordHistory(goCtx context.Context, ev RecordEvent) {
	if d.NudgeRecorder == nil {
		return
	}
	if err := d.NudgeRecorder.Record(goCtx, ev); err != nil && d.HistoryErrLog != nil {
		d.HistoryErrLog(fmt.Sprintf("nudge_history write failed: session=%s result=%s: %v", ev.SessionID, ev.Result, err))
	}
}

// sourceStrings stringifies a []Source for the RecordEvent.Sources field.
func sourceStrings(sources []Source) []string {
	out := make([]string, len(sources))
	for i, s := range sources {
		out[i] = string(s)
	}
	return out
}

func indexSessions(tree *aggregate.Tree) map[string]*aggregate.SessionView {
	out := map[string]*aggregate.SessionView{}
	if tree == nil {
		return out
	}
	for _, dir := range tree.Dirs {
		for _, s := range dir.Sessions {
			out[s.SessionID] = s
		}
	}
	return out
}

// resolveText picks the text to send: manual overrides win, else the
// first non-empty text found.
func resolveText(group []NudgeIntent) string {
	for _, in := range group {
		if in.Key.Source == SourceManual && in.Text != "" {
			return in.Text
		}
	}
	for _, in := range group {
		if in.Text != "" {
			return in.Text
		}
	}
	return ""
}
