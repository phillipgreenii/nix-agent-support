package discover

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/backoff"
	"github.com/phillipgreenii/pr-pool/internal/core"
	"github.com/phillipgreenii/pr-pool/internal/event"
	"github.com/phillipgreenii/pr-pool/internal/eventqueue"
	"github.com/phillipgreenii/pr-pool/internal/item"
	"github.com/phillipgreenii/pr-pool/internal/query"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// fakeQuery is a stand-in producer: it embeds query.Meta (for Emits/Trigger) and
// returns canned events (or an error).
type fakeQuery struct {
	query.Meta
	events []event.Event
	err    error
}

func (f fakeQuery) Validate() error { return nil }

// BackingCommand: the fake produces canned events in-process, so it shells out to
// nothing (config.Validate's absent-backing-command check skips it).
func (f fakeQuery) BackingCommand() string { return "" }

func (f fakeQuery) Run(context.Context, query.Env) ([]event.Event, error) {
	return f.events, f.err
}

func itemEvt(typ, id string) event.Event {
	return event.NewItemEvent(typ, "", item.Item{ID: id, Type: "task"})
}

// flakyQuery fails its first `failTimes` Run calls, then succeeds and returns
// events — the pull-source failure backoff's (INV-FAIL-3) retry-then-succeed
// case. calls counts every Run invocation so a test can assert the retry count.
type flakyQuery struct {
	query.Meta
	failTimes int
	events    []event.Event
	calls     *int
}

func (f flakyQuery) Validate() error        { return nil }
func (f flakyQuery) BackingCommand() string { return "" }
func (f flakyQuery) Run(context.Context, query.Env) ([]event.Event, error) {
	*f.calls++
	if *f.calls <= f.failTimes {
		return nil, errors.New("source unavailable")
	}
	return f.events, nil
}

// recordingSleep is the sleepFunc test double: it records every wait duration
// requested and returns immediately (no real sleeping), so a retry-with-backoff
// test runs instantly while still asserting the CADENCE that was requested.
func recordingSleep(waits *[]time.Duration) sleepFunc {
	return func(ctx context.Context, d time.Duration) error {
		*waits = append(*waits, d)
		return nil
	}
}

// testListener is a minimal eventqueue.Listener double: it matches on a fixed
// set of event types and records every event offered to it, always accepting
// (the queue-side stand-in for "a role bound to these types").
type testListener struct {
	id      string
	binds   map[string]bool
	offered []eventqueue.Event
}

func newTestListener(id string, binds ...string) *testListener {
	b := make(map[string]bool, len(binds))
	for _, t := range binds {
		b[t] = true
	}
	return &testListener{id: id, binds: b}
}

func (l *testListener) ID() string                        { return l.id }
func (l *testListener) Matches(evt eventqueue.Event) bool { return l.binds[evt.Type] }
func (l *testListener) Offer(evt eventqueue.Event) bool {
	l.offered = append(l.offered, evt)
	return true
}

func newQueue(t *testing.T) *eventqueue.Queue {
	t.Helper()
	q, err := eventqueue.New(eventqueue.NewMemStore())
	if err != nil {
		t.Fatalf("eventqueue.New: %v", err)
	}
	return q
}

func TestProduce_periodQueriesEnqueueForBoundRoles(t *testing.T) {
	sources := query.SourceSet{
		{Name: "feedback-source", Query: fakeQuery{
			Meta:   query.Meta{EmitTypes: []string{"feedback.ready"}, Trig: query.PeriodTrigger{}},
			events: []event.Event{itemEvt("feedback.ready", "fb-1")},
		}},
		{Name: "worker-source", Query: fakeQuery{
			Meta:   query.Meta{EmitTypes: []string{"work.ready"}, Trig: query.PeriodTrigger{}},
			events: []event.Event{itemEvt("work.ready", "wk-1"), itemEvt("work.ready", "wk-2")},
		}},
	}
	q := newQueue(t)
	fb := newTestListener("feedback", "feedback.ready")
	wk := newTestListener("worker", "work.ready")
	q.Register(fb)
	q.Register(wk)

	declared := core.NewBindings("feedback.ready", "work.ready")
	if _, err := Produce(context.Background(), query.Env{}, sources, q, declared); err != nil {
		t.Fatal(err)
	}
	// Both work.ready events are ENQUEUED by this one Produce call...
	if depth := q.DepthByType()["work.ready"]; depth != 2 {
		t.Fatalf("both work.ready events must be enqueued, depth = %d", depth)
	}
	// ...but only ONE head per listener is OFFERED per Dispatch() call
	// (per-handler serial FIFO, INV-CONC-1 / DEC-EVENT-2) — there is no cap
	// gating this, just the queue's own per-listener cursor.
	q.Dispatch()
	if len(fb.offered) != 1 || ItemFromPayload(fb.offered[0].Payload).ID != "fb-1" {
		t.Fatalf("feedback listener wrong: %+v", fb.offered)
	}
	if len(wk.offered) != 1 || ItemFromPayload(wk.offered[0].Payload).ID != "wk-1" {
		t.Fatalf("worker listener's first head wrong: %+v", wk.offered)
	}
	// Provenance stamped from the source name.
	if src, _ := fb.offered[0].Payload["source"].(string); src != "feedback-source" {
		t.Fatalf("event source must be stamped from the query name, got %q", src)
	}
	q.Dispatch() // the worker listener's head advances to the second event
	if len(wk.offered) != 2 || ItemFromPayload(wk.offered[1].Payload).ID != "wk-2" {
		t.Fatalf("worker listener's second head wrong: %+v", wk.offered)
	}
}

// A query failure (retries exhausted — the zero-value Meta.FB fails fast) is
// ISOLATED to that source's SourceErrors entry, not returned as Produce's own
// error (INV-FAIL-3, INV-EVT-1; ADR per Task 0.6 — INV-PREC-1 resolved as
// never-drop-work). It must still not masquerade as "no ready work" (pg2-qq9v)
// — it is recorded, just not propagated as a pass-aborting error.
func TestProduce_queryErrorIsolatedToSourceErrors(t *testing.T) {
	sentinel := errors.New("bd down")
	sources := query.SourceSet{
		{Name: "boom", Query: fakeQuery{Meta: query.Meta{EmitTypes: []string{"x"}}, err: sentinel}},
	}
	rpt, err := Produce(context.Background(), query.Env{}, sources, newQueue(t), core.NewBindings("x"))
	if err != nil {
		t.Fatalf("a source failure must not abort the pass; got Produce error %v", err)
	}
	if rpt.SourceErrors["boom"] == nil || !errors.Is(rpt.SourceErrors["boom"], sentinel) {
		t.Fatalf("SourceErrors[boom] = %v, want it to wrap %q", rpt.SourceErrors["boom"], sentinel)
	}
}

// A query that has NOT opted into a pull-source failure backoff (zero-value
// Meta.FB, Retries: 0) still fails FAST on the very first error — exactly
// pg2-qq9v's original behavior, unchanged by pg2-0c8yz's addition. No sleep is
// ever consulted. The failure is still isolated to SourceErrors, never
// returned as produce's own error (INV-FAIL-3, INV-EVT-1).
func TestProduce_queryErrorIsolatedImmediatelyWithoutOptIn(t *testing.T) {
	calls := 0
	var waits []time.Duration
	sources := query.SourceSet{
		{Name: "boom", Query: flakyQuery{
			Meta:      query.Meta{EmitTypes: []string{"x"}},
			failTimes: 100, // would never succeed — proves it did NOT retry
			calls:     &calls,
		}},
	}
	rpt, err := produce(context.Background(), query.Env{}, sources, newQueue(t), core.NewBindings("x"), Cadence{}, recordingSleep(&waits), time.Now, nil)
	if err != nil {
		t.Fatalf("a source failure must not abort the pass; got produce error %v", err)
	}
	if rpt.SourceErrors["boom"] == nil {
		t.Fatal("SourceErrors[boom] must be set once the query fails")
	}
	if calls != 1 {
		t.Fatalf("Run was called %d times, want exactly 1 (fail fast, no retry)", calls)
	}
	if len(waits) != 0 {
		t.Fatalf("waits = %v, want none (no backoff configured)", waits)
	}
}

// INV-FAIL-3: a pull-source query that fails and then succeeds within its
// configured Retries is retried at the configured backoff cadence, and its
// events are still enqueued once it succeeds — the failure never propagates.
func TestProduce_pullSourceRetriesThenSucceeds(t *testing.T) {
	calls := 0
	var waits []time.Duration
	sources := query.SourceSet{
		{Name: "flaky", Query: flakyQuery{
			Meta: query.Meta{
				EmitTypes: []string{"work.ready"},
				FB: query.FailureBackoff{
					Policy:  backoff.Policy{Initial: time.Second, Factor: 2, Max: time.Minute},
					Retries: 2,
				},
			},
			failTimes: 2, // fails twice, succeeds on the 3rd Run
			events:    []event.Event{itemEvt("work.ready", "wk-1")},
			calls:     &calls,
		}},
	}
	q := newQueue(t)
	wk := newTestListener("worker", "work.ready")
	q.Register(wk)
	obs := &recordingSourceFailureObserver{}

	rpt, err := produce(context.Background(), query.Env{}, sources, q, core.NewBindings("work.ready"), Cadence{}, recordingSleep(&waits), time.Now, obs)
	if err != nil {
		t.Fatalf("failure must NOT propagate once a retry succeeds: %v", err)
	}
	if len(rpt.SourceErrors) != 0 {
		t.Fatalf("SourceErrors = %v, want none once the retry succeeded", rpt.SourceErrors)
	}
	if calls != 3 {
		t.Fatalf("Run was called %d times, want 3 (2 failures + 1 success)", calls)
	}
	if !equalDurations(waits, []time.Duration{time.Second, 2 * time.Second}) {
		t.Fatalf("waits = %v, want [1s 2s] (the configured backoff, growing per consecutive failure)", waits)
	}
	// MetricSourceFailures's feed point (register gap R21 / bead pg2-00jpn,
	// INV-FAIL-3): the observer is notified once per retry attempt, alongside
	// the existing log-only Warn — same cadence as `waits` above.
	if want := []string{"flaky", "flaky"}; !equalStrings(obs.sources, want) {
		t.Fatalf("observer.sources = %v, want %v (one OnSourceFailure call per retry attempt)", obs.sources, want)
	}
	q.Dispatch()
	if len(wk.offered) != 1 || ItemFromPayload(wk.offered[0].Payload).ID != "wk-1" {
		t.Fatalf("events not enqueued after the retry succeeded: %+v", wk.offered)
	}
}

// INV-FAIL-3: once Retries is exhausted the failure is recorded (never
// silently dropped — pg2-qq9v's "must not masquerade as no ready work") but no
// longer aborts the pass (INV-EVT-1, INV-PREC-1 resolved as never-drop-work,
// ADR per Task 0.6) — the backoff smooths a transient blip; a source that
// stays down is isolated, not treated as fatal to the whole tick.
func TestProduce_pullSourceIsolatedAfterRetriesExhausted(t *testing.T) {
	calls := 0
	var waits []time.Duration
	sentinel := errors.New("source unavailable")
	sources := query.SourceSet{
		{Name: "down", Query: flakyQuery{
			Meta: query.Meta{
				EmitTypes: []string{"x"},
				FB: query.FailureBackoff{
					Policy:  backoff.Policy{Initial: time.Second, Factor: 2, Max: time.Minute},
					Retries: 2,
				},
			},
			failTimes: 100, // never recovers within the retry budget
			calls:     &calls,
		}},
	}
	rpt, err := produce(context.Background(), query.Env{}, sources, newQueue(t), core.NewBindings("x"), Cadence{}, recordingSleep(&waits), time.Now, nil)
	if err != nil {
		t.Fatalf("a source failure must not abort the pass; got produce error %v", err)
	}
	if rpt.SourceErrors["down"] == nil || !strings.Contains(rpt.SourceErrors["down"].Error(), sentinel.Error()) {
		t.Fatalf("SourceErrors[down] = %v, want it to wrap %q once retries are exhausted", rpt.SourceErrors["down"], sentinel)
	}
	if calls != 3 {
		t.Fatalf("Run was called %d times, want 3 (1 initial + 2 retries)", calls)
	}
	if !equalDurations(waits, []time.Duration{time.Second, 2 * time.Second}) {
		t.Fatalf("waits = %v, want [1s 2s]", waits)
	}
}

// TestProduceIsolatesSourceFailure is Task 1.1's Step 1(a) red test
// (INV-FAIL-3, INV-EVT-1; ADR per Task 0.6 — INV-PREC-1 resolved as
// never-drop-work): with two sources, the FIRST exhausts its retries — but
// the SECOND still runs, its event still enqueues, and Dispatch/Expire still
// run over the queue exactly as if nothing had failed. The report carries the
// failing source's error.
func TestProduceIsolatesSourceFailure(t *testing.T) {
	calls := 0
	sources := query.SourceSet{
		{Name: "failing", Query: flakyQuery{
			Meta:      query.Meta{EmitTypes: []string{"x"}},
			failTimes: 100, // never recovers — retries exhausted (zero-value FB fails fast)
			calls:     &calls,
		}},
		{Name: "healthy", Query: fakeQuery{
			Meta:   query.Meta{EmitTypes: []string{"work.ready"}, Trig: query.PeriodTrigger{}},
			events: []event.Event{itemEvt("work.ready", "wk-1")},
		}},
	}
	q := newQueue(t)
	wk := newTestListener("worker", "work.ready")
	q.Register(wk)

	rpt, err := Produce(context.Background(), query.Env{}, sources, q, core.NewBindings("work.ready"))
	if err != nil {
		t.Fatalf("a failing source must not abort the pass; got Produce error %v", err)
	}
	if rpt.SourceErrors["failing"] == nil || !strings.Contains(rpt.SourceErrors["failing"].Error(), "source unavailable") {
		t.Fatalf("SourceErrors[failing] = %v, want it to wrap the query's error", rpt.SourceErrors["failing"])
	}
	if calls != 1 {
		t.Fatalf("failing source's Run called %d times, want exactly 1 (retries exhausted, no opt-in)", calls)
	}
	if depth := q.DepthByType()["work.ready"]; depth != 1 {
		t.Fatalf("the healthy source's event must still enqueue despite the sibling failure, depth = %d", depth)
	}

	if accepted := q.Dispatch(); accepted != 1 {
		t.Fatalf("Dispatch must still deliver the healthy source's event, accepted = %d", accepted)
	}
	if len(wk.offered) != 1 || ItemFromPayload(wk.offered[0].Payload).ID != "wk-1" {
		t.Fatalf("worker listener wrong: %+v", wk.offered)
	}
	if dropped := q.Expire(); dropped != 1 {
		t.Fatalf("Expire must still retire the already-delivered event, dropped = %d", dropped)
	}
}

// TestProduce_undeclaredTypeRejected is Task 1.1's Step 1(b) red test
// (INV-DISP-3; ADR per Task 0.6): an event of a type NO configured role binds
// to is rejected and counted in Rejected[source], never enqueued — a
// defense-in-depth check now held on the pull path too, matching what
// core.Listen already enforces on push. A sibling event of a DECLARED type
// from another source still enqueues; the rejection is per-event and does
// not abort the pass or count as a source failure.
func TestProduce_undeclaredTypeRejected(t *testing.T) {
	sources := query.SourceSet{
		{Name: "known", Query: fakeQuery{
			Meta:   query.Meta{EmitTypes: []string{"known.type"}, Trig: query.PeriodTrigger{}},
			events: []event.Event{itemEvt("known.type", "k-1")},
		}},
		{Name: "unknown", Query: fakeQuery{
			Meta:   query.Meta{EmitTypes: []string{"unknown.type"}, Trig: query.PeriodTrigger{}},
			events: []event.Event{itemEvt("unknown.type", "u-1")},
		}},
	}
	q := newQueue(t)
	declared := core.NewBindings("known.type") // "unknown.type" is NOT declared

	rpt, err := Produce(context.Background(), query.Env{}, sources, q, declared)
	if err != nil {
		t.Fatalf("an undeclared-type rejection must not abort the pass; got Produce error %v", err)
	}
	if len(rpt.SourceErrors) != 0 {
		t.Fatalf("SourceErrors = %v, want none — nothing failed, one event was merely rejected", rpt.SourceErrors)
	}
	if depth := q.DepthByType()["unknown.type"]; depth != 0 {
		t.Fatalf("an undeclared-type event must never enqueue, depth = %d", depth)
	}
	if got := rpt.Rejected["unknown"]; got != 1 {
		t.Fatalf("Rejected[unknown] = %d, want 1", got)
	}
	if depth := q.DepthByType()["known.type"]; depth != 1 {
		t.Fatalf("a sibling DECLARED-type event from another source must still enqueue, depth = %d", depth)
	}
	if got := rpt.Emitted["known"]; got != 1 {
		t.Fatalf("Emitted[known] = %d, want 1", got)
	}
}

func equalDurations(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// recordingSourceFailureObserver is a SourceFailureObserver test double: it
// records every source name OnSourceFailure was called with, in call order.
type recordingSourceFailureObserver struct {
	sources []string
}

func (r *recordingSourceFailureObserver) OnSourceFailure(source string) {
	r.sources = append(r.sources, source)
}

// TestProduce_WithSourceFailureObserverOption proves the exported Produce
// entry point (not just the private produce helper above) threads
// WithSourceFailureObserver through end to end — existing 4-arg callers keep
// compiling unchanged, and a caller that opts in gets notified.
func TestProduce_WithSourceFailureObserverOption(t *testing.T) {
	calls := 0
	sources := query.SourceSet{
		{Name: "flaky", Query: flakyQuery{
			Meta: query.Meta{
				EmitTypes: []string{"work.ready"},
				FB: query.FailureBackoff{
					Policy:  backoff.Policy{Initial: time.Millisecond, Factor: 1, Max: time.Millisecond},
					Retries: 1,
				},
			},
			failTimes: 1,
			events:    []event.Event{itemEvt("work.ready", "wk-1")},
			calls:     &calls,
		}},
	}
	obs := &recordingSourceFailureObserver{}
	_, err := Produce(context.Background(), query.Env{}, sources, newQueue(t), core.NewBindings("work.ready"), WithSourceFailureObserver(obs))
	if err != nil {
		t.Fatalf("Produce: %v", err)
	}
	if want := []string{"flaky"}; !equalStrings(obs.sources, want) {
		t.Fatalf("observer.sources = %v, want %v", obs.sources, want)
	}
}

func TestProduce_thresholdFiresOnlyWhenEnough(t *testing.T) {
	// upstream (period) emits one "up" event; downstream (threshold Count=1 on
	// "up") should fire and emit a "down" event.
	newSources := func(count int) query.SourceSet {
		return query.SourceSet{
			{Name: "up-source", Query: fakeQuery{
				Meta:   query.Meta{EmitTypes: []string{"up"}, Trig: query.PeriodTrigger{}},
				events: []event.Event{itemEvt("up", "u1")},
			}},
			{Name: "down-source", Query: fakeQuery{
				Meta:   query.Meta{EmitTypes: []string{"down"}, Trig: query.ThresholdTrigger{Binds: []string{"up"}, Count: count}},
				events: []event.Event{itemEvt("down", "d1")},
			}},
		}
	}

	declared := core.NewBindings("up", "down")

	// Count=1: one "up" is enough — the threshold query fires.
	q1 := newQueue(t)
	if _, err := Produce(context.Background(), query.Env{}, newSources(1), q1, declared); err != nil {
		t.Fatal(err)
	}
	if depth := q1.DepthByType()["down"]; depth != 1 {
		t.Fatalf("threshold(Count=1) must fire on one upstream event, got depth %d", depth)
	}

	// Count=2: one "up" is NOT enough — the threshold query does not fire.
	q2 := newQueue(t)
	if _, err := Produce(context.Background(), query.Env{}, newSources(2), q2, declared); err != nil {
		t.Fatal(err)
	}
	if depth := q2.DepthByType()["down"]; depth != 0 {
		t.Fatalf("threshold(Count=2) must NOT fire on one upstream event, got depth %d", depth)
	}
}

func TestProduce_manualNeverFiresOnTick(t *testing.T) {
	sources := query.SourceSet{
		{Name: "manual", Query: fakeQuery{
			Meta:   query.Meta{EmitTypes: []string{"m"}, Trig: query.ManualTrigger{}},
			events: []event.Event{itemEvt("m", "m1")},
		}},
	}
	q := newQueue(t)
	if _, err := Produce(context.Background(), query.Env{}, sources, q, core.NewBindings("m")); err != nil {
		t.Fatal(err)
	}
	if depth := q.DepthByType()["m"]; depth != 0 {
		t.Fatalf("manual trigger must not fire on a tick, got depth %d", depth)
	}
}

// TestProduceWithCadence_periodSourceSkippedUntilOwnEveryElapsed is Task 1.3's
// third required RED test: a source with its own PeriodTrigger.Every == 30s
// must NOT fire on a 10s poll tick until 30s have actually elapsed since it
// last fired — cad.LastTick (fed forward from each call's own returned
// ProduceReport.LastTick, exactly as orchestrator.Orchestrator.ProduceTick
// does across real ticks) is the cadence substrate; the clock is injected so
// the test drives four simulated 10s ticks without waiting real time.
func TestProduceWithCadence_periodSourceSkippedUntilOwnEveryElapsed(t *testing.T) {
	clockT := time.Unix(1_700_000_000, 0)
	clock := func() time.Time { return clockT }

	var calls int
	sources := query.SourceSet{
		{Name: "slow-source", Query: flakyQuery{
			Meta:      query.Meta{EmitTypes: []string{"slow.ready"}, Trig: query.PeriodTrigger{Every: 30 * time.Second}},
			failTimes: 0,
			events:    []event.Event{itemEvt("slow.ready", "sl-1")},
			calls:     &calls,
		}},
	}
	declared := core.NewBindings("slow.ready")
	q := newQueue(t)
	cad := Cadence{PollInterval: 10 * time.Second}

	// Tick @ t0: this cadence has never fired this source before -> due
	// immediately (preserves the pre-Task-1.3 always-fire-first-pass behavior).
	rpt, err := produce(context.Background(), query.Env{}, sources, q, declared, cad, realSleep, clock, nil)
	if err != nil {
		t.Fatalf("produce (t0): %v", err)
	}
	if calls != 1 {
		t.Fatalf("t0: want 1 query run, got %d", calls)
	}
	cad.LastTick = rpt.LastTick // fed forward, matching ProduceTick's own threading

	// Tick @ t0+10s (a 10s poll tick): the source's own 30s period is NOT yet
	// due -> its query must not run at all.
	clockT = clockT.Add(10 * time.Second)
	if _, err := produce(context.Background(), query.Env{}, sources, q, declared, cad, realSleep, clock, nil); err != nil {
		t.Fatalf("produce (t0+10s): %v", err)
	}
	if calls != 1 {
		t.Fatalf("t0+10s: want still 1 query run (not due), got %d", calls)
	}

	// Tick @ t0+20s: still not due (20s < 30s).
	clockT = clockT.Add(10 * time.Second)
	if _, err := produce(context.Background(), query.Env{}, sources, q, declared, cad, realSleep, clock, nil); err != nil {
		t.Fatalf("produce (t0+20s): %v", err)
	}
	if calls != 1 {
		t.Fatalf("t0+20s: want still 1 query run (not due), got %d", calls)
	}

	// Tick @ t0+30s: now due (30s elapsed since the t0 fire) -> runs again.
	clockT = clockT.Add(10 * time.Second)
	if _, err := produce(context.Background(), query.Env{}, sources, q, declared, cad, realSleep, clock, nil); err != nil {
		t.Fatalf("produce (t0+30s): %v", err)
	}
	if calls != 2 {
		t.Fatalf("t0+30s: want 2 query runs (due), got %d", calls)
	}
}

// TestProduceWithCadence_zeroEveryFallsBackToPollInterval proves the OTHER
// half of Task 1.3's Objective — a source whose own PeriodTrigger.Every is
// the zero value (query.Meta{}'s own documented default, the built-in Go
// query set's shape when unconfigured) is gated on cad.PollInterval instead,
// rather than firing every pass unconditionally.
func TestProduceWithCadence_zeroEveryFallsBackToPollInterval(t *testing.T) {
	clockT := time.Unix(1_700_000_000, 0)
	clock := func() time.Time { return clockT }

	var calls int
	sources := query.SourceSet{
		{Name: "default-source", Query: flakyQuery{
			Meta:      query.Meta{EmitTypes: []string{"default.ready"}}, // Trig unset -> PeriodTrigger{Every: 0}
			failTimes: 0,
			events:    []event.Event{itemEvt("default.ready", "d1")},
			calls:     &calls,
		}},
	}
	declared := core.NewBindings("default.ready")
	q := newQueue(t)
	cad := Cadence{PollInterval: 20 * time.Second}

	rpt, err := produce(context.Background(), query.Env{}, sources, q, declared, cad, realSleep, clock, nil)
	if err != nil {
		t.Fatalf("produce (t0): %v", err)
	}
	if calls != 1 {
		t.Fatalf("t0: want 1 query run, got %d", calls)
	}
	cad.LastTick = rpt.LastTick

	clockT = clockT.Add(10 * time.Second)
	if _, err := produce(context.Background(), query.Env{}, sources, q, declared, cad, realSleep, clock, nil); err != nil {
		t.Fatalf("produce (t0+10s): %v", err)
	}
	if calls != 1 {
		t.Fatalf("t0+10s: want still 1 query run (PollInterval=20s not yet elapsed), got %d", calls)
	}

	clockT = clockT.Add(10 * time.Second)
	if _, err := produce(context.Background(), query.Env{}, sources, q, declared, cad, realSleep, clock, nil); err != nil {
		t.Fatalf("produce (t0+20s): %v", err)
	}
	if calls != 2 {
		t.Fatalf("t0+20s: want 2 query runs (PollInterval elapsed), got %d", calls)
	}
}

func TestDeriveContext(t *testing.T) {
	role := roles.Role{Name: "worker"}
	e := itemEvt("work.ready", "zr-9")
	d := DeriveContext(role, e)
	if d.Role.Name != "worker" || d.Item.ID != "zr-9" {
		t.Fatalf("derived context wrong: %+v", d)
	}
}

func TestDeriveContextFromQueueEvent(t *testing.T) {
	role := roles.Role{Name: "worker"}
	qe := ToQueueEvent(itemEvt("work.ready", "zr-9"))
	d := DeriveContextFromQueueEvent(role, qe)
	if d.Role.Name != "worker" || d.Item.ID != "zr-9" {
		t.Fatalf("derived context wrong: %+v", d)
	}
}

func TestItemFromPayload_absentIsZeroValue(t *testing.T) {
	if got := ItemFromPayload(nil); got.ID != "" {
		t.Fatalf("ItemFromPayload(nil) = %+v, want zero value", got)
	}
	if got := ItemFromPayload(map[string]any{"other": 1}); got.ID != "" {
		t.Fatalf("ItemFromPayload without an item key = %+v, want zero value", got)
	}
}

// A discover-constructed event's payload (ToQueueEvent's item+source
// convention) survives an EncodeEvent/DecodeEvent wire round trip unchanged —
// the path a forwarded (cross-process, e.g. socket-relayed) discover event
// takes — and ItemFromPayload reconstructs the same item on the far side
// (Task 1.6: wire.go's payload normalization must not disturb a
// non-empty payload discover.go already builds).
func TestToQueueEvent_PayloadSurvivesWireRoundTrip(t *testing.T) {
	qe := ToQueueEvent(itemEvt("work.ready", "zr-9"))
	wire, err := eventqueue.EncodeEvent(qe)
	if err != nil {
		t.Fatalf("EncodeEvent: %v", err)
	}
	decoded, err := eventqueue.DecodeEvent(wire)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	got := ItemFromPayload(decoded.Payload)
	if got.ID != "zr-9" || got.Type != "task" {
		t.Fatalf("ItemFromPayload(round-tripped payload) = %+v, want id=zr-9 type=task", got)
	}
}

// An event with NO payload at all (a pushed event a source sent with `payload`
// omitted, never a discover-produced one) decodes, per Task 1.6's wire
// normalization, to a non-nil EMPTY map ({}) rather than nil. discover's own
// consumer of a queue event's payload — ItemFromPayload, and the
// DeriveContextFromQueueEvent bridge built on it — must handle that
// wire-normalized {} exactly like the absent-payload case: a non-match, not an
// error, yielding the zero Item/DispatchContext.
func TestItemFromPayload_HandlesWireNormalizedEmptyPayload(t *testing.T) {
	decoded, err := eventqueue.DecodeEvent([]byte(`{"id":"e","type":"t"}`))
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if decoded.Payload == nil || len(decoded.Payload) != 0 {
		t.Fatalf("decoded.Payload = %v, want a non-nil empty map (wire.go's normalization)", decoded.Payload)
	}
	if got := ItemFromPayload(decoded.Payload); got.ID != "" || got.Type != "" {
		t.Fatalf("ItemFromPayload(wire-normalized empty payload) = %+v, want zero Item", got)
	}
	role := roles.Role{Name: "worker"}
	d := DeriveContextFromQueueEvent(role, decoded)
	if d.Item.ID != "" || d.Item.Type != "" || d.Item.Title != "" || d.Item.Metadata != nil {
		t.Fatalf("DeriveContextFromQueueEvent(wire-normalized empty payload) item = %+v, want zero Item", d.Item)
	}
}

func TestQueriesForRole(t *testing.T) {
	sources := query.SourceSet{
		{Name: "a", Query: fakeQuery{Meta: query.Meta{EmitTypes: []string{"feedback.ready"}}}},
		{Name: "b", Query: fakeQuery{Meta: query.Meta{EmitTypes: []string{"work.ready"}}}},
		{Name: "c", Query: fakeQuery{Meta: query.Meta{EmitTypes: []string{"feedback.ready"}}}},
	}
	role := roles.Role{Name: "feedback", Binds: []string{"feedback.ready"}}
	got := QueriesForRole(sources, role)
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "c" {
		t.Fatalf("QueriesForRole should return the feedback-emitting sources, got %+v", got)
	}
}

func TestDispatchContext_Validate(t *testing.T) {
	cases := []struct {
		name     string
		d        DispatchContext
		wantErr  bool
		wantSubs []string
	}{
		{"valid", DispatchContext{Role: roles.Role{Name: "worker"}, Item: item.Item{ID: "zr-1"}}, false, nil},
		{"missing-item", DispatchContext{Role: roles.Role{Name: "worker"}}, true, []string{"item"}},
		{"missing-role", DispatchContext{Item: item.Item{ID: "zr-1"}}, true, []string{"role"}},
		{"missing-both", DispatchContext{}, true, []string{"role", "item"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.d.Validate()
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate() err=%v, wantErr=%v", err, tc.wantErr)
			}
			for _, sub := range tc.wantSubs {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("err %q should mention %q", err, sub)
				}
			}
		})
	}
}
