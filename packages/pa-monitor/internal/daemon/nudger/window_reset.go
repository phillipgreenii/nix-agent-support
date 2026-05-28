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
			if s.Status == session.Working {
				continue
			}
			store.Add(NudgeIntent{
				Key:       IntentKey{SessionID: s.SessionID, Source: SourceWindowReset},
				Text:      ctx.AutoResumeMessage,
				EmittedAt: ctx.Now,
			})
		}
	}
}
