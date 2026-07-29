package nudger

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/limits"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

// qualifyingLimitPauseSV builds a SessionView that satisfies the limit-pause
// producer's per-session gate: non-Working, terminal rate_limit LastError,
// zero RateLimitResetsAt (no parseable reset time), and not from a subagent.
func qualifyingLimitPauseSV(sid string, pid int) *aggregate.SessionView {
	sv := newSV(sid, pid, session.Idle)
	sv.LastError = &transcript.ErrorRecord{
		Kind:       transcript.ErrRateLimit,
		IsTerminal: true,
	}
	return sv
}

// treeWithFiveHour builds a Tree whose account-global FiveHourResetsAt latch is
// set (the limit-pause producer keys its once-per-window guard off this).
func treeWithFiveHour(fiveHour time.Time, sessions ...*aggregate.SessionView) *aggregate.Tree {
	t := &aggregate.Tree{FiveHourResetsAt: fiveHour}
	t.Dirs = []*aggregate.Directory{{Sessions: sessions}}
	return t
}

// Case 1: fires exactly one SourceLimitPause intent for a qualifying session
// when FiveHourResetsAt is beyond the latch.
func TestLimitPauseProducerFiresOnceForQualifyingSession(t *testing.T) {
	reset := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	now := reset.Add(-time.Hour)
	p := &LimitPauseProducer{}
	store := NewPendingStore()
	tree := treeWithFiveHour(reset, qualifyingLimitPauseSV("sid-1", 1))
	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true, AutoResumeMessage: "continue",
		Tree: tree, Watermarks: wmStub{}, // latch zero
	}, store)
	got := store.List()
	if len(got) != 1 {
		t.Fatalf("len(intents) = %d, want 1", len(got))
	}
	if got[0].Key.Source != SourceLimitPause {
		t.Errorf("source = %q, want limit_pause", got[0].Key.Source)
	}
	if got[0].Key.SessionID != "sid-1" {
		t.Errorf("sid = %q, want sid-1", got[0].Key.SessionID)
	}
	if got[0].Text != "continue" {
		t.Errorf("text = %q, want continue", got[0].Text)
	}
}

// Case 2: once-per-window — after the latch is set to reset, the same reset
// yields no new intent and cancels any pending one.
func TestLimitPauseProducerOncePerWindow(t *testing.T) {
	reset := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	now := reset.Add(-time.Hour)
	p := &LimitPauseProducer{}
	store := NewPendingStore()
	// Seed a pending intent so we also assert it is cancelled.
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceLimitPause}, Text: "continue", EmittedAt: now})
	tree := treeWithFiveHour(reset, qualifyingLimitPauseSV("sid-1", 1))
	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true, AutoResumeMessage: "continue",
		Tree: tree, Watermarks: wmStub{lp: reset}, // already fired for this window
	}, store)
	if store.HasAny("sid-1") {
		t.Errorf("intent still pending; want cancelAll when latch == reset (once-per-window)")
	}
}

// Case 3: re-arm — latch=R1, reset=R2 (R2 after R1) fires again for the new
// window.
func TestLimitPauseProducerReArmsOnNewWindow(t *testing.T) {
	r1 := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	r2 := r1.Add(5 * time.Hour)
	now := r2.Add(-time.Hour)
	p := &LimitPauseProducer{}
	store := NewPendingStore()
	tree := treeWithFiveHour(r2, qualifyingLimitPauseSV("sid-1", 1))
	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true, AutoResumeMessage: "continue",
		Tree: tree, Watermarks: wmStub{lp: r1},
	}, store)
	if got := len(store.List()); got != 1 {
		t.Fatalf("len(intents) = %d, want 1 (re-arm on new window R2>R1)", got)
	}
}

// Case 4: monotonicity via After — a regressed reset (R1 < latch R2) does NOT
// fire; a later reset (R3 > R2) fires once.
func TestLimitPauseProducerMonotonicityUsesAfter(t *testing.T) {
	r2 := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	r1 := r2.Add(-5 * time.Hour) // regressed / garbage-low
	r3 := r2.Add(5 * time.Hour)
	now := r2

	t.Run("regressed reset does not fire", func(t *testing.T) {
		p := &LimitPauseProducer{}
		store := NewPendingStore()
		tree := treeWithFiveHour(r1, qualifyingLimitPauseSV("sid-1", 1))
		p.Reconcile(TickContext{
			Now: now, AutoResumeEnabled: true, AutoResumeMessage: "continue",
			Tree: tree, Watermarks: wmStub{lp: r2},
		}, store)
		if got := len(store.List()); got != 0 {
			t.Errorf("len(intents) = %d, want 0 (R1 < latch R2, After suppresses)", got)
		}
	})

	t.Run("later reset fires once", func(t *testing.T) {
		p := &LimitPauseProducer{}
		store := NewPendingStore()
		tree := treeWithFiveHour(r3, qualifyingLimitPauseSV("sid-1", 1))
		p.Reconcile(TickContext{
			Now: now, AutoResumeEnabled: true, AutoResumeMessage: "continue",
			Tree: tree, Watermarks: wmStub{lp: r2},
		}, store)
		if got := len(store.List()); got != 1 {
			t.Errorf("len(intents) = %d, want 1 (R3 > latch R2)", got)
		}
	})
}

// Case 5: per-session gate — a session with a parseable reset (RateLimitResetsAt
// > 0) is excluded even when Kind==rate_limit; a sibling with a zero reset in
// the same tree still fires.
func TestLimitPauseProducerPerSessionGateExcludesParseableReset(t *testing.T) {
	reset := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	now := reset.Add(-time.Hour)
	p := &LimitPauseProducer{}
	store := NewPendingStore()

	parseable := qualifyingLimitPauseSV("sid-parseable", 1)
	parseable.RateLimitResetsAt = reset.Add(30 * time.Minute) // has a parsed reset → excluded
	noReset := qualifyingLimitPauseSV("sid-noreset", 2)       // zero reset → fires

	tree := treeWithFiveHour(reset, parseable, noReset)
	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true, AutoResumeMessage: "continue",
		Tree: tree, Watermarks: wmStub{},
	}, store)

	if store.HasAny("sid-parseable") {
		t.Error("session with parseable RateLimitResetsAt should be excluded")
	}
	if !store.HasAny("sid-noreset") {
		t.Error("sibling with zero RateLimitResetsAt should still fire")
	}
	if got := len(store.List()); got != 1 {
		t.Fatalf("len(intents) = %d, want 1 (only the no-reset sibling)", got)
	}
}

// Case 6: guards — each condition suppresses the nudge and cancels any pending
// intent for the session.
func TestLimitPauseProducerGuards(t *testing.T) {
	reset := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	now := reset.Add(-time.Hour)

	mk := func(mut func(sv *aggregate.SessionView)) *aggregate.SessionView {
		sv := qualifyingLimitPauseSV("sid-1", 1)
		if mut != nil {
			mut(sv)
		}
		return sv
	}

	cases := []struct {
		name       string
		sv         *aggregate.SessionView
		fiveHour   time.Time
		autoResume bool
	}{
		{"non-terminal rate_limit", mk(func(sv *aggregate.SessionView) { sv.LastError.IsTerminal = false }), reset, true},
		{"from-subagent", mk(func(sv *aggregate.SessionView) { sv.LastError.FromSubagent = true }), reset, true},
		{"working", mk(func(sv *aggregate.SessionView) { sv.Status = session.Working }), reset, true},
		{"non-rate-limit kind", mk(func(sv *aggregate.SessionView) { sv.LastError.Kind = transcript.ErrServerError }), reset, true},
		{"five-hour zero", mk(nil), time.Time{}, true},
		{"auto-resume disabled", mk(nil), reset, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &LimitPauseProducer{}
			store := NewPendingStore()
			// Seed a pending intent so we assert both no-fire AND cancellation.
			store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceLimitPause}, Text: "continue", EmittedAt: now})
			tree := treeWithFiveHour(tc.fiveHour, tc.sv)
			p.Reconcile(TickContext{
				Now: now, AutoResumeEnabled: tc.autoResume, AutoResumeMessage: "continue",
				Tree: tree, Watermarks: wmStub{},
			}, store)
			if store.HasAny("sid-1") {
				t.Errorf("intent still pending; guard %q should suppress and cancel", tc.name)
			}
		})
	}
}

// Case 7: recovery — a previously-qualifying session whose LastError flips
// IsTerminal=false has its pending intent cancelled (no nudge).
func TestLimitPauseProducerRecoveryCancels(t *testing.T) {
	reset := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	now := reset.Add(-time.Hour)
	p := &LimitPauseProducer{}
	store := NewPendingStore()
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceLimitPause}, Text: "continue", EmittedAt: now})
	sv := qualifyingLimitPauseSV("sid-1", 1)
	sv.LastError.IsTerminal = false // session recovered
	tree := treeWithFiveHour(reset, sv)
	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true, AutoResumeMessage: "continue",
		Tree: tree, Watermarks: wmStub{},
	}, store)
	if store.HasAny("sid-1") {
		t.Error("intent not cancelled after session recovered (IsTerminal=false)")
	}
}

// Case 8: skipped windows — latch=R1, reset jumps to R3 (skipping R2); fires
// exactly once for R3.
func TestLimitPauseProducerSkippedWindowFiresOnce(t *testing.T) {
	r1 := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	r3 := r1.Add(15 * time.Hour) // several windows later
	now := r3.Add(-time.Hour)
	p := &LimitPauseProducer{}
	store := NewPendingStore()
	tree := treeWithFiveHour(r3, qualifyingLimitPauseSV("sid-1", 1))
	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true, AutoResumeMessage: "continue",
		Tree: tree, Watermarks: wmStub{lp: r1},
	}, store)
	if got := len(store.List()); got != 1 {
		t.Fatalf("len(intents) = %d, want 1 (fires once for skipped-to window R3)", got)
	}
}

// Case 9 (bead pg2-yzs6a): the once-per-window latch cannot be advanced past a
// legitimate later reset by an out-of-range value.
//
// This is a COMPOSITION test, because the latch itself has no defence: it stores
// whatever Tree.FiveHourResetsAt holds and compares with After, so a garbage-HIGH
// value stored once suppresses every real window until that instant passes. The
// protection is the bound at ingestion — so the test drives the real fold
// (limits.Current, the same call the daemon's LimitsSource makes) over a record set
// that contains BOTH a legitimate window reset and a garbage-HIGH epoch, and then
// runs the producer across the window boundary:
//
//	tick 1 — window R1 (the legitimate reset) fires and the dispatcher advances the
//	         latch to Tree.FiveHourResetsAt;
//	tick 2 — the next real window R2 (R1 + 5h) MUST still fire.
//
// Pre-bound, Current elected the garbage epoch (greatest-reset wins), the latch
// absorbed it, and tick 2 was silently swallowed.
func TestLimitPauseProducerOutOfRangeResetCannotPoisonLatch(t *testing.T) {
	r1 := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	r2 := r1.Add(5 * time.Hour) // the next legitimate window
	render := r1.Add(-time.Hour)

	ts := render.Unix()
	legit := r1.Unix()
	garbage := render.Add(365 * 24 * time.Hour).Unix() // a year out — upstream garbage
	pct80, pct99 := 80.0, 99.0
	cur := limits.Current([]limits.Record{
		{TS: &ts, FiveHourPct: &pct80, FiveHourResetsAt: &legit},
		{TS: &ts, FiveHourPct: &pct99, FiveHourResetsAt: &garbage},
	})
	if cur == nil {
		t.Fatal("limits.Current = nil, want a reading")
	}
	if !cur.FiveHourResetsAt.Equal(r1) {
		t.Fatalf("FiveHourResetsAt = %v, want %v — the out-of-range epoch must be DISCARDED at ingestion, not latched",
			cur.FiveHourResetsAt, r1)
	}

	// Tick 1: the legitimate window fires, and the dispatcher would advance the
	// latch to exactly the (bounded) tree value.
	p := &LimitPauseProducer{}
	store := NewPendingStore()
	p.Reconcile(TickContext{
		Now: render, AutoResumeEnabled: true, AutoResumeMessage: "continue",
		Tree:       treeWithFiveHour(cur.FiveHourResetsAt, qualifyingLimitPauseSV("sid-1", 1)),
		Watermarks: wmStub{},
	}, store)
	if got := len(store.List()); got != 1 {
		t.Fatalf("tick 1: len(intents) = %d, want 1 (first window fires)", got)
	}
	latch := cur.FiveHourResetsAt

	// Tick 2: the next real window must still be able to advance past the latch.
	store2 := NewPendingStore()
	p.Reconcile(TickContext{
		Now: r2.Add(-time.Hour), AutoResumeEnabled: true, AutoResumeMessage: "continue",
		Tree:       treeWithFiveHour(r2, qualifyingLimitPauseSV("sid-1", 1)),
		Watermarks: wmStub{lp: latch},
	}, store2)
	if got := len(store2.List()); got != 1 {
		t.Fatalf("tick 2: len(intents) = %d, want 1 (legitimate later window R2 must not be swallowed by the latch)", got)
	}
}
