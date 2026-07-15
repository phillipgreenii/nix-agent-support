package nudger

import (
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
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
	// Producer-side no-surface gate (bead pg2-gjekd): a surfaceless "ghost"
	// session has nowhere to deliver an auto-resume, so reap it from the
	// candidate set instead of enqueuing an intent the dispatcher can only
	// per-tick-suppress ("no_surface"). Deeper fix complementing pg2-2o0p7's
	// dispatcher-side suppress-and-drop backstop. Checked before the error gates
	// so a surfaceless session is reaped regardless of its LastError.
	if !ctx.hasSurface(s.PID) {
		cancel()
		return
	}
	if s.LastError == nil || !s.LastError.IsTerminal {
		cancel()
		return
	}
	// Auto-resume policy: resume only the transient classes
	// (ClassTransientServer, ClassTransientNetwork). This re-expresses the old
	// kind-based predicate on the shared RetryClass — a deliberate tightening
	// (an opaque non-network `unknown` no longer auto-resumes). The escalation
	// flip (LastErrorRetryable=false) is a separate, later gate handled by the
	// daemon lifecycle, not here.
	if !transcript.Retryable(s.LastError) {
		cancel()
		return
	}
	if s.LastError.FromSubagent {
		// Surface-only: nudging the parent will not revive a dead subagent.
		cancel()
		return
	}

	wm := ctx.Watermarks.SessionWatermark(s.SessionID)

	// Determine "new error" by comparing LastError.At to the persisted
	// last_disrupt_nudge_for watermark.
	isNewError := s.LastError.At.After(wm.LastDisruptNudgeFor)

	if isNewError {
		// Cancel any stale intent and clear the escalation flag so the new
		// error can go through the full nudge → escalate cycle again.
		store.Cancel(key)
		ctx.Watermarks.SetDisruptEscalated(s.SessionID, false) // re-arm
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
			ctx.Watermarks.SetDisruptEscalated(s.SessionID, true)
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
