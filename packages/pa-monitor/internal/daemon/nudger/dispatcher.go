package nudger

import (
	"context"
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
		if view.Status == session.Working {
			d.Recorder.RecordSuppressed(sid, sources, "session_active")
			store.RemoveKeys(observedKeys)
			continue
		}
		text := resolveText(group)
		if err := d.Signaler.Send(view.PID, text); err != nil {
			// Leave intents in place; retry next tick.
			continue
		}
		var cause *transcript.ErrorRecord
		var kind string
		var causeAt *time.Time
		for _, in := range group {
			if in.Key.Source == SourceDisrupted && in.Cause != nil {
				cause = in.Cause
				kind = string(in.Cause.Kind)
				t := in.Cause.At
				causeAt = &t
				break
			}
		}
		wm := ctx.Watermarks.SessionWatermark(sid)
		escalated := wm.DisruptEscalated
		d.Recorder.RecordSent(sid, sources, kind, escalated)
		d.Recorder.UpdateWatermarks(sid, ctx.Now, sources, cause, escalated)
		if d.NudgeRecorder != nil {
			srcStrs := make([]string, len(sources))
			for i, s := range sources {
				srcStrs[i] = string(s)
			}
			_ = d.NudgeRecorder.Record(goCtx, RecordEvent{
				SessionID:       sid,
				Text:            text,
				Result:          "sent",
				ErrorText:       kind,
				CausedByErrorAt: causeAt,
				Escalated:       escalated,
				FiredAt:         ctx.Now,
				Sources:         srcStrs,
			})
		}
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
