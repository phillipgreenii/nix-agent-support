package nudger

import (
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
	UpdateWatermarks(sid string, now time.Time, cause *transcript.ErrorRecord, escalated bool)
}

// Dispatcher fires nudges based on the pending store, performs the
// active-session suppression check, and clears intents after success or
// suppression. Send failures leave intents for the next tick.
type Dispatcher struct {
	Signaler Signaler
	Recorder Recorder
}

// Dispatch iterates pending intents once, grouped by session.
func (d *Dispatcher) Dispatch(ctx TickContext, store *PendingStore) {
	intents := store.List()
	if len(intents) == 0 {
		return
	}
	bySession := map[string][]NudgeIntent{}
	for _, in := range intents {
		bySession[in.Key.SessionID] = append(bySession[in.Key.SessionID], in)
	}
	sids := make([]string, 0, len(bySession))
	for sid := range bySession {
		sids = append(sids, sid)
	}
	sort.Strings(sids)

	sessionsByID := indexSessions(ctx.Tree)
	for _, sid := range sids {
		group := bySession[sid]
		sources := make([]Source, 0, len(group))
		for _, in := range group {
			sources = append(sources, in.Key.Source)
		}

		view, ok := sessionsByID[sid]
		if !ok {
			store.ClearSession(sid)
			continue
		}
		if view.Status == session.Working {
			d.Recorder.RecordSuppressed(sid, sources, "session_active")
			store.ClearSession(sid)
			continue
		}
		text := resolveText(group)
		if err := d.Signaler.Send(view.PID, text); err != nil {
			// Leave intents in place; retry next tick.
			continue
		}
		var cause *transcript.ErrorRecord
		var kind string
		for _, in := range group {
			if in.Key.Source == SourceDisrupted && in.Cause != nil {
				cause = in.Cause
				kind = string(in.Cause.Kind)
				break
			}
		}
		wm := ctx.Watermarks.SessionWatermark(sid)
		escalated := wm.DisruptEscalated
		d.Recorder.RecordSent(sid, sources, kind, escalated)
		d.Recorder.UpdateWatermarks(sid, ctx.Now, cause, escalated)
		store.ClearSession(sid)
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
