package nudger

import (
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
)

// NewDisruptProducer constructs a producer with an empty firstSeen map.
func NewDisruptProducer() *DisruptProducer {
	return &DisruptProducer{firstSeen: map[string]time.Time{}}
}

// NoteFirstSeen primes the firstSeen map (currently used by tests; can be
// used by persistence layers on daemon startup to restore state).
func (p *DisruptProducer) NoteFirstSeen(sid string, at time.Time) {
	if p.firstSeen == nil {
		p.firstSeen = map[string]time.Time{}
	}
	p.firstSeen[sid] = at
}

// Reconcile implements Producer. See spec §Architecture §Producers
// §DisruptProducer for the rule table.
func (p *DisruptProducer) Reconcile(ctx TickContext, store *PendingStore) {
	if p.firstSeen == nil {
		p.firstSeen = map[string]time.Time{}
	}

	for _, dir := range ctx.Tree.Dirs {
		for _, s := range dir.Sessions {
			p.reconcileSession(ctx, store, s)
		}
	}
	// GC firstSeen for sessions not in the tree anymore.
	seen := map[string]struct{}{}
	for _, dir := range ctx.Tree.Dirs {
		for _, s := range dir.Sessions {
			seen[s.SessionID] = struct{}{}
		}
	}
	for sid := range p.firstSeen {
		if _, ok := seen[sid]; !ok {
			delete(p.firstSeen, sid)
		}
	}
}

func (p *DisruptProducer) reconcileSession(ctx TickContext, store *PendingStore, s *aggregate.SessionView) {
	key := IntentKey{SessionID: s.SessionID, Source: SourceDisrupted}
	cancel := func() {
		store.Cancel(key)
		delete(p.firstSeen, s.SessionID)
	}

	if !ctx.AutoResumeEnabled {
		cancel()
		return
	}
	if s.LastError == nil || !s.LastError.IsTerminal {
		cancel()
		return
	}
	if !s.LastError.IsRetryable {
		cancel()
		return
	}

	wm := ctx.Watermarks.SessionWatermark(s.SessionID)

	// Determine "new error" by comparing LastError.At to the persisted
	// last_disrupt_nudge_for watermark.
	isNewError := s.LastError.At.After(wm.LastDisruptNudgeFor)

	if isNewError {
		// Cancel any stale intent.
		store.Cancel(key)
		existing, ok := p.firstSeen[s.SessionID]
		// If firstSeen is absent or predates this error's timestamp, it belongs to
		// a previous error cycle; reset the grace clock to now and wait.
		if !ok || existing.IsZero() || existing.Before(s.LastError.At) {
			p.firstSeen[s.SessionID] = ctx.Now
			return
		}
		// firstSeen >= LastError.At means we already started the grace clock for
		// this new error; fall through to the grace check below.
	} else {
		// Same error we already nudged: evaluate escalation.
		if !wm.DisruptEscalated &&
			!wm.LastDisruptNudgeAt.IsZero() &&
			ctx.Now.Sub(wm.LastDisruptNudgeAt) >= ctx.EscalationAfter {
			cancel()
			return
		}
		if wm.DisruptEscalated {
			cancel()
			return
		}
	}

	// Grace check.
	first, ok := p.firstSeen[s.SessionID]
	if !ok || first.IsZero() {
		p.firstSeen[s.SessionID] = ctx.Now
		return
	}
	if ctx.Now.Sub(first) < ctx.DisruptGrace {
		return
	}
	// Grace elapsed; enqueue (idempotent).
	store.Add(NudgeIntent{
		Key:       key,
		Text:      ctx.AutoResumeMessage,
		EmittedAt: ctx.Now,
		Cause:     s.LastError,
	})
}
