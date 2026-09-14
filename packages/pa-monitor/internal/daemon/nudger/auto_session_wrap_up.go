package nudger

import (
	_ "embed"
	"fmt"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// DefaultDirectiveTemplate is the shipped AutoSessionWrapUp nudge text (bead
// tc-m08w3). Kept as its own file alongside the producer (rather than a Go
// string literal) for maintainability — the prose can be edited without
// touching Go syntax. A single indexed verb (%[1]d) is formatted with the
// session's idle minutes at fire time; see Reconcile.
//
// The provenance tag ("[auto-nudge: pa-monitor, idle Nm]") is load-bearing,
// not decorative: delivery is literal typed input via the terminal signaler
// (tmux send-keys / cmux paste), so the directive becomes an ordinary user
// turn in the session's own transcript, permanently, in the user's voice-slot
// but not their words. The tag lets a later reader (the user, a future agent)
// tell it was not typed by the user.
//
//go:embed auto_session_wrap_up_directive.txt
var DefaultDirectiveTemplate string

// Reconcile implements Producer for AutoSessionWrapUpProducer. It nudges
// sessions that have been Idle for at least AutoSessionWrapUpIdleThreshold to
// run session-wrapup:wrap-up-session "auto-trigger", at most once per idle
// episode (see SessionWatermark.LastAutoSessionWrapUpNudgedFor).
//
// Per ADR 0024, Status is the 3-value {Working, Blocked, Idle} enum — a
// Blocked session (including a permission-prompt-parked one) never fires,
// even past the threshold; only Idle does. "Idle episode start" is the
// session's own TranscriptMTime: the timestamp the transcript last changed,
// i.e. the instant idleness began. When the session goes Working again and
// returns to Idle, TranscriptMTime advances, naturally starting a fresh
// episode the once-per-episode latch has never seen.
func (p *AutoSessionWrapUpProducer) Reconcile(ctx TickContext, store *PendingStore) {
	cancelAll := func() {
		for _, dir := range ctx.Tree.Dirs {
			for _, s := range dir.Sessions {
				store.Cancel(IntentKey{SessionID: s.SessionID, Source: SourceAutoSessionWrapUp})
			}
		}
	}

	if !ctx.AutoSessionWrapUpEnabled {
		cancelAll()
		return
	}
	if ctx.AutoSessionWrapUpIdleThreshold <= 0 {
		cancelAll()
		return
	}
	tmpl := ctx.AutoSessionWrapUpDirectiveTemplate
	if tmpl == "" {
		tmpl = DefaultDirectiveTemplate
	}

	for _, dir := range ctx.Tree.Dirs {
		for _, s := range dir.Sessions {
			key := IntentKey{SessionID: s.SessionID, Source: SourceAutoSessionWrapUp}

			if s.Status != session.Idle {
				store.Cancel(key)
				continue
			}
			episodeStart := s.TranscriptMTime
			if episodeStart.IsZero() {
				// No known activity timestamp; nothing to measure idleness
				// against.
				store.Cancel(key)
				continue
			}
			if ctx.Now.Sub(episodeStart) < ctx.AutoSessionWrapUpIdleThreshold {
				store.Cancel(key)
				continue
			}
			// Producer-side no-surface gate (bead pg2-gjekd), same rationale as
			// WindowResetProducer/DisruptProducer: a surfaceless "ghost" session
			// has nowhere to deliver a nudge — reap it rather than enqueue.
			if !ctx.hasSurface(s.PID) {
				store.Cancel(key)
				continue
			}
			// Once-per-episode latch: fire only when this episode is newer than
			// the last one a nudge was recorded for. Read from the persisted
			// WatermarkView so a daemon restart mid-episode cannot re-fire (the
			// user's explicit, firm no-runaway requirement).
			wm := ctx.Watermarks.SessionWatermark(s.SessionID)
			if !episodeStart.After(wm.LastAutoSessionWrapUpNudgedFor) {
				store.Cancel(key)
				continue
			}
			// Unsubmitted-input guard (safety-critical): idle (no new
			// transcript activity) is indistinguishable from a session
			// mid-composition. Skip THIS tick without consuming the episode —
			// no watermark write, so the next tick re-evaluates and can still
			// fire once the input line clears.
			if ctx.hasUnsubmittedInput(s.PID) {
				store.Cancel(key)
				continue
			}

			idleMinutes := int(ctx.Now.Sub(episodeStart).Minutes())
			store.Add(NudgeIntent{
				Key:       key,
				Text:      fmt.Sprintf(tmpl, idleMinutes),
				EmittedAt: ctx.Now,
			})
			// Persist the latch immediately (mirrors DisruptProducer's direct
			// SetDisruptEscalated write): a missed nudge (e.g. delivery fails)
			// costs nothing extra; a duplicate one burns tokens for no benefit,
			// so this producer is biased toward under-firing, never re-firing.
			ctx.Watermarks.SetAutoSessionWrapUpNudgedFor(s.SessionID, episodeStart)
		}
	}
}
