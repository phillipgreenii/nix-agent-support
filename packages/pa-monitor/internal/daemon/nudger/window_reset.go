package nudger

import (
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// windowResetClockSkewGuard is the minimum delay after a window's computed reset
// time before a window-reset nudge may fire. The reset time is derived from the
// local clock, which can run ahead of the server that actually clears the limit;
// firing exactly at (or a few seconds after) the computed reset risks nudging a
// session whose window has not truly reset yet. Waiting at least this long
// absorbs plausible clock discrepancies (bead pg2-t8n96). It acts as a floor on
// the configured AutoResumeDelay — a longer configured delay already satisfies
// the margin and is left untouched.
const windowResetClockSkewGuard = 60 * time.Second

// Reconcile implements Producer. See spec §Architecture §Producers
// §WindowResetProducer for the rule table.
func (p *WindowResetProducer) Reconcile(ctx TickContext, store *PendingStore) {
	cancelAll := func() {
		for _, dir := range ctx.Tree.Dirs {
			for _, s := range dir.Sessions {
				store.Cancel(IntentKey{SessionID: s.SessionID, Source: SourceWindowReset})
			}
		}
	}

	if !ctx.AutoResumeEnabled {
		cancelAll()
		return
	}
	resetsAt := ctx.Tree.WindowResetsAt
	if resetsAt.IsZero() {
		cancelAll()
		return
	}
	fireAt := resetsAt.Add(max(ctx.AutoResumeDelay, windowResetClockSkewGuard))
	if ctx.Now.Before(fireAt) {
		// Window still pending (or within the clock-skew guard); producer waits
		// silently.
		return
	}
	if ctx.Watermarks.WindowResetFiredFor().Equal(resetsAt) {
		cancelAll()
		return
	}
	for _, dir := range ctx.Tree.Dirs {
		for _, s := range dir.Sessions {
			key := IntentKey{SessionID: s.SessionID, Source: SourceWindowReset}
			if s.Status == session.Working {
				continue
			}
			// Producer-side no-surface gate (bead pg2-gjekd): reap a surfaceless
			// "ghost" session from the candidate set — nowhere to deliver, so
			// never enqueue it (and thus never per-tick-suppress it).
			if !ctx.hasSurface(s.PID) {
				store.Cancel(key)
				continue
			}
			store.Add(NudgeIntent{
				Key:       key,
				Text:      ctx.AutoResumeMessage,
				EmittedAt: ctx.Now,
			})
		}
	}
}
