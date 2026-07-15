package nudger

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

// hasSurfaceExcept returns a HasSurface predicate that reports every pid as
// having a surface EXCEPT those in surfaceless (the ghost pids).
func hasSurfaceExcept(surfaceless ...int) func(pid int) bool {
	ghosts := map[int]bool{}
	for _, p := range surfaceless {
		ghosts[p] = true
	}
	return func(pid int) bool { return !ghosts[pid] }
}

// TestDisruptProducerReapsSurfacelessGhost asserts the producer-side no-surface
// gate (pg2-gjekd): a surfaceless session past grace is NEVER enqueued (reaped
// from the candidate set), while a healthy session with the same error IS
// enqueued. Complements pg2-2o0p7's dispatcher-side suppress-and-drop backstop.
func TestDisruptProducerReapsSurfacelessGhost(t *testing.T) {
	now := time.Date(2026, 7, 15, 15, 0, 0, 0, time.UTC)
	errAt := now.Add(-2 * time.Minute) // well past the 30s grace
	p := NewDisruptProducer()
	store := NewPendingStore()

	ghost := sessionWithError("ghost", transcript.ErrUnknown, errAt, true)
	ghost.PID = 45600 // surfaceless: pane closed / detached from cmux
	healthy := sessionWithError("healthy", transcript.ErrUnknown, errAt, true)
	healthy.PID = 1001 // has a surface
	tree := treeWith(time.Time{}, ghost, healthy)

	// Prime firstSeen for both so the grace window has already elapsed and the
	// enqueue path is reached for both sessions.
	p.NoteFirstSeen("ghost", errAt)
	p.NoteFirstSeen("healthy", errAt)

	ctx := TickContext{
		Now: now, AutoResumeEnabled: true,
		DisruptGrace: 30 * time.Second, EscalationAfter: 60 * time.Second,
		AutoResumeMessage: "continue", Tree: tree, Watermarks: wmStub{},
		HasSurface: hasSurfaceExcept(45600),
	}
	p.Reconcile(ctx, store)

	if store.HasAny("ghost") {
		t.Error("surfaceless ghost was enqueued; producer-side gate must reap it from the candidate set")
	}
	if !store.HasAny("healthy") {
		t.Error("healthy session with a surface was not enqueued; gate over-reaped")
	}
}

// TestDisruptProducerReapsExistingIntentWhenSurfaceLost asserts that a session
// which already has a queued disrupt intent gets it CANCELLED (reaped) once its
// surface disappears, so the dispatcher never sees it again.
func TestDisruptProducerReapsExistingIntentWhenSurfaceLost(t *testing.T) {
	now := time.Date(2026, 7, 15, 15, 0, 0, 0, time.UTC)
	errAt := now.Add(-2 * time.Minute)
	p := NewDisruptProducer()
	store := NewPendingStore()
	store.Add(NudgeIntent{Key: IntentKey{"ghost", SourceDisrupted}, EmittedAt: now})

	ghost := sessionWithError("ghost", transcript.ErrUnknown, errAt, true)
	ghost.PID = 45600
	tree := treeWith(time.Time{}, ghost)
	p.NoteFirstSeen("ghost", errAt)

	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true,
		DisruptGrace: 30 * time.Second, EscalationAfter: 60 * time.Second,
		AutoResumeMessage: "continue", Tree: tree, Watermarks: wmStub{},
		HasSurface: hasSurfaceExcept(45600),
	}, store)

	if store.HasAny("ghost") {
		t.Error("existing intent for a session that lost its surface was not reaped")
	}
}

// TestWindowResetProducerReapsSurfacelessGhost asserts the no-surface gate also
// reaps surfaceless sessions from the window-reset candidate set.
func TestWindowResetProducerReapsSurfacelessGhost(t *testing.T) {
	now := time.Date(2026, 7, 15, 15, 0, 0, 0, time.UTC)
	resetsAt := now.Add(-1 * time.Minute) // window already reset, fire due
	p := &WindowResetProducer{}
	store := NewPendingStore()

	ghost := newSV("ghost", 45600, session.Idle)
	healthy := newSV("healthy", 1001, session.Idle)
	tree := treeWith(resetsAt, ghost, healthy)

	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true, AutoResumeDelay: 0,
		AutoResumeMessage: "continue", Tree: tree, Watermarks: wmStub{},
		HasSurface: hasSurfaceExcept(45600),
	}, store)

	if store.HasAny("ghost") {
		t.Error("surfaceless ghost enqueued by window-reset producer; must be reaped")
	}
	if !store.HasAny("healthy") {
		t.Error("healthy session not enqueued by window-reset producer; gate over-reaped")
	}
}

// TestLimitPauseProducerReapsSurfacelessGhost asserts the no-surface gate also
// reaps surfaceless sessions from the limit-pause candidate set.
func TestLimitPauseProducerReapsSurfacelessGhost(t *testing.T) {
	now := time.Date(2026, 7, 15, 15, 0, 0, 0, time.UTC)
	fiveHour := now.Add(1 * time.Hour) // future reset, latch not yet fired
	p := &LimitPauseProducer{}
	store := NewPendingStore()

	rateLimited := func(sid string, pid int) *aggregate.SessionView {
		rec := &transcript.ErrorRecord{
			Kind: transcript.ErrRateLimit, Text: "You've hit your org's monthly spend limit",
			At: now.Add(-1 * time.Minute), IsTerminal: true,
		}
		return &aggregate.SessionView{
			Session:           &session.Session{SessionID: sid, PID: pid, Status: session.Blocked, Blocker: session.UsageLimit},
			SessionEnrichment: aggregate.SessionEnrichment{LastError: rec},
		}
	}
	ghost := rateLimited("ghost", 45600)
	healthy := rateLimited("healthy", 1001)
	tree := treeWithFiveHour(fiveHour, ghost, healthy)

	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true,
		AutoResumeMessage: "continue", Tree: tree, Watermarks: wmStub{},
		HasSurface: hasSurfaceExcept(45600),
	}, store)

	if store.HasAny("ghost") {
		t.Error("surfaceless ghost enqueued by limit-pause producer; must be reaped")
	}
	if !store.HasAny("healthy") {
		t.Error("healthy rate-limited session not enqueued by limit-pause producer; gate over-reaped")
	}
}
