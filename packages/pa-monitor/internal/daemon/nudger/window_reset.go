package nudger

import (
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

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
	fireAt := resetsAt.Add(ctx.AutoResumeDelay)
	if ctx.Now.Before(fireAt) {
		// Window still pending; producer waits silently.
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
