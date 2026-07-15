package nudger

import (
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

// Reconcile implements Producer for sessions stuck on a rate-limit error that
// carries NO parseable reset time (e.g. the org monthly spend-limit 429:
// message "You've hit your org's monthly spend limit ...", from which
// parseLimitResetText extracts nothing, so RateLimitResetsAt stays zero). Such
// sessions are detected (LastError.Kind=rate_limit, IsTerminal) but neither the
// disrupt path (rate_limit is not Retryable) nor the window-reset path (needs a
// parsed reset) ever nudges them. This producer nudges them ONCE per
// account-global 5h window (Tree.FiveHourResetsAt), retrying once each new
// window until the limit clears. See bead pg2-2z7k.
//
// Structure MIRRORS WindowResetProducer, but the once-per-window latch keys off
// the account-global FiveHourResetsAt rather than a per-session parsed reset,
// and there is no AutoResumeDelay gate (the value-latch handles timing — do NOT
// consult LastError.At here).
func (p *LimitPauseProducer) Reconcile(ctx TickContext, store *PendingStore) {
	cancelAll := func() {
		for _, dir := range ctx.Tree.Dirs {
			for _, s := range dir.Sessions {
				store.Cancel(IntentKey{SessionID: s.SessionID, Source: SourceLimitPause})
			}
		}
	}

	if !ctx.AutoResumeEnabled {
		cancelAll()
		return
	}
	reset := ctx.Tree.FiveHourResetsAt
	if reset.IsZero() {
		cancelAll()
		return
	}
	// Once-per-window guard using After (NOT Equal): FiveHourResetsAt is not
	// guaranteed monotonic. After suppresses a spurious re-fire when a regressed
	// / garbage-LOW reset value arrives at or below the latch. (A garbage-HIGH
	// value could instead poison future windows by advancing the latch past
	// legitimate later resets — the real fix for that is bounding the value at
	// upstream ingestion, which is out of scope here.)
	if !reset.After(ctx.Watermarks.LimitPauseFiredFor()) {
		cancelAll()
		return
	}
	for _, dir := range ctx.Tree.Dirs {
		for _, s := range dir.Sessions {
			key := IntentKey{SessionID: s.SessionID, Source: SourceLimitPause}
			if s.Status == session.Working {
				store.Cancel(key)
				continue
			}
			// Producer-side no-surface gate (bead pg2-gjekd): reap a surfaceless
			// "ghost" session from the candidate set — nowhere to deliver, so
			// never enqueue it (and thus never per-tick-suppress it).
			if !ctx.hasSurface(s.PID) {
				store.Cancel(key)
				continue
			}
			le := s.LastError
			if le == nil || !le.IsTerminal || le.Kind != transcript.ErrRateLimit || !s.RateLimitResetsAt.IsZero() || le.FromSubagent {
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
