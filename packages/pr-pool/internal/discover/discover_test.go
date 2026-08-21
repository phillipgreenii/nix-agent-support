package discover

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/backoff"
	"github.com/phillipgreenii/pr-pool/internal/event"
	"github.com/phillipgreenii/pr-pool/internal/eventbus"
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

var epoch = time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)

func TestProduce_periodQueriesPublishToBoundRoles(t *testing.T) {
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
	bus := eventbus.New()
	bus.Subscribe("feedback", "feedback.ready")
	bus.Subscribe("worker", "work.ready")

	if err := Produce(context.Background(), query.Env{}, sources, bus, epoch); err != nil {
		t.Fatal(err)
	}
	fb, _ := bus.Lease(context.Background(), "feedback", 10)
	wk, _ := bus.Lease(context.Background(), "worker", 10)
	if len(fb) != 1 || fb[0].Item.ID != "fb-1" {
		t.Fatalf("feedback queue wrong: %+v", fb)
	}
	if len(wk) != 2 {
		t.Fatalf("worker queue wrong: %+v", wk)
	}
	// Provenance stamped from the source name.
	if fb[0].Source != "feedback-source" {
		t.Fatalf("event source must be stamped from the query name, got %q", fb[0].Source)
	}
}

func TestProduce_queryErrorPropagates(t *testing.T) {
	sentinel := errors.New("bd down")
	sources := query.SourceSet{
		{Name: "boom", Query: fakeQuery{Meta: query.Meta{EmitTypes: []string{"x"}}, err: sentinel}},
	}
	err := Produce(context.Background(), query.Env{}, sources, eventbus.New(), epoch)
	if err == nil || !errors.Is(err, sentinel) {
		t.Fatalf("a query error must propagate; got %v", err)
	}
}

// A query that has NOT opted into a pull-source failure backoff (zero-value
// Meta.FB, Retries: 0) still fails FAST on the very first error — exactly
// pg2-qq9v's original behavior, unchanged by pg2-0c8yz's addition. No sleep is
// ever consulted.
func TestProduce_queryErrorPropagatesImmediatelyWithoutOptIn(t *testing.T) {
	calls := 0
	var waits []time.Duration
	sources := query.SourceSet{
		{Name: "boom", Query: flakyQuery{
			Meta:      query.Meta{EmitTypes: []string{"x"}},
			failTimes: 100, // would never succeed — proves it did NOT retry
			calls:     &calls,
		}},
	}
	err := produce(context.Background(), query.Env{}, sources, eventbus.New(), epoch, recordingSleep(&waits))
	if err == nil {
		t.Fatal("a query error must still propagate without an opt-in")
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
// events are still published once it succeeds — the failure never propagates.
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
	bus := eventbus.New()
	bus.Subscribe("worker", "work.ready")

	err := produce(context.Background(), query.Env{}, sources, bus, epoch, recordingSleep(&waits))
	if err != nil {
		t.Fatalf("failure must NOT propagate once a retry succeeds: %v", err)
	}
	if calls != 3 {
		t.Fatalf("Run was called %d times, want 3 (2 failures + 1 success)", calls)
	}
	if !equalDurations(waits, []time.Duration{time.Second, 2 * time.Second}) {
		t.Fatalf("waits = %v, want [1s 2s] (the configured backoff, growing per consecutive failure)", waits)
	}
	got, _ := bus.Lease(context.Background(), "worker", 10)
	if len(got) != 1 || got[0].Item.ID != "wk-1" {
		t.Fatalf("events not published after the retry succeeded: %+v", got)
	}
}

// INV-FAIL-3: once Retries is exhausted the failure STILL propagates — the
// backoff smooths a transient blip, it does not turn "always down" into
// "silently idle" (pg2-qq9v).
func TestProduce_pullSourcePropagatesAfterRetriesExhausted(t *testing.T) {
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
	err := produce(context.Background(), query.Env{}, sources, eventbus.New(), epoch, recordingSleep(&waits))
	if err == nil || !strings.Contains(err.Error(), sentinel.Error()) {
		t.Fatalf("error = %v, want it to wrap %q once retries are exhausted", err, sentinel)
	}
	if calls != 3 {
		t.Fatalf("Run was called %d times, want 3 (1 initial + 2 retries)", calls)
	}
	if !equalDurations(waits, []time.Duration{time.Second, 2 * time.Second}) {
		t.Fatalf("waits = %v, want [1s 2s]", waits)
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

	// Count=1: one "up" is enough — the threshold query fires.
	bus := eventbus.New()
	bus.Subscribe("upR", "up")
	bus.Subscribe("downR", "down")
	if err := Produce(context.Background(), query.Env{}, newSources(1), bus, epoch); err != nil {
		t.Fatal(err)
	}
	if got, _ := bus.Lease(context.Background(), "downR", 10); len(got) != 1 {
		t.Fatalf("threshold(Count=1) must fire on one upstream event, got %d", len(got))
	}

	// Count=2: one "up" is NOT enough — the threshold query does not fire.
	bus2 := eventbus.New()
	bus2.Subscribe("upR", "up")
	bus2.Subscribe("downR", "down")
	if err := Produce(context.Background(), query.Env{}, newSources(2), bus2, epoch); err != nil {
		t.Fatal(err)
	}
	if got, _ := bus2.Lease(context.Background(), "downR", 10); len(got) != 0 {
		t.Fatalf("threshold(Count=2) must NOT fire on one upstream event, got %d", len(got))
	}
}

func TestProduce_manualNeverFiresOnTick(t *testing.T) {
	sources := query.SourceSet{
		{Name: "manual", Query: fakeQuery{
			Meta:   query.Meta{EmitTypes: []string{"m"}, Trig: query.ManualTrigger{}},
			events: []event.Event{itemEvt("m", "m1")},
		}},
	}
	bus := eventbus.New()
	bus.Subscribe("mR", "m")
	if err := Produce(context.Background(), query.Env{}, sources, bus, epoch); err != nil {
		t.Fatal(err)
	}
	if got, _ := bus.Lease(context.Background(), "mR", 10); len(got) != 0 {
		t.Fatalf("manual trigger must not fire on a tick, got %d", len(got))
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
