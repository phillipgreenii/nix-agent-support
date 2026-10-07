package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/backoff"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/query"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

// seqHandler is a wireclient.HandlerClient whose Nth Dispatch returns errs[N]
// (nil past the end = success), recording every call's role and event id.
type seqHandler struct {
	mu    sync.Mutex
	errs  []error
	calls []string
}

func (h *seqHandler) Dispatch(_ context.Context, _ roles.Role, evt eventqueue.Event) (wireclient.Reply, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	i := len(h.calls)
	h.calls = append(h.calls, evt.ID)
	if i < len(h.errs) && h.errs[i] != nil {
		return wireclient.Reply{}, h.errs[i]
	}
	return wireclient.Reply{ID: "dsp-x", Outcome: "ok"}, nil
}

func (h *seqHandler) PostStartup(context.Context, roles.Role) (wireclient.Reply, error) {
	return wireclient.Reply{}, nil
}

func (h *seqHandler) PreShutdown(context.Context, roles.Role) (wireclient.Reply, error) {
	return wireclient.Reply{}, nil
}

func (h *seqHandler) callCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.calls)
}

type retryCall struct{ role, class string }

type fakeRetryObserver struct {
	mu    sync.Mutex
	calls []retryCall
}

func (f *fakeRetryObserver) OnDispatchRetry(role, class string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, retryCall{role, class})
}

func (f *fakeRetryObserver) snapshot() []retryCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]retryCall(nil), f.calls...)
}

var killedErr = &wireclient.ExitError{Role: "desk-pr", Code: -1}

func retryEvent(id string) eventqueue.Event {
	return eventqueue.Event{ID: id, Type: "pr.changed", Payload: map[string]any{"id": "pr-1"}, ExpiresAt: time.Now().Add(time.Hour)}
}

func newRetryListener(t *testing.T, h wireclient.HandlerClient, maxRetries int, ctx context.Context) (eventqueue.Listener, *fakeRetryObserver, *fakeHandlerFailureObserver) {
	t.Helper()
	o := newOrch(fastCfg(t), testQuerySet(nil, nil))
	o.Handler = h
	robs := &fakeRetryObserver{}
	fobs := &fakeHandlerFailureObserver{}
	o.DispatchRetryObserver = robs
	o.HandlerFailureObserver = fobs
	role := roles.Role{Name: "desk-pr", Binds: []string{"pr.changed"}, MaxDispatchRetries: maxRetries}
	return o.NewListener(ctx, role), robs, fobs
}

func TestClassifyTransient(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		class string
		ok    bool
	}{
		{"nil", nil, "", false},
		{"typed killed exit -1", killedErr, RetryClassKilled, true},
		{"wrapped typed killed", fmt.Errorf("outer: %w", killedErr), RetryClassKilled, true},
		{"handler-flattened child killed", errors.New(`command role "desk-pr" item pr-1: exit status 1: stderr tail: x: signal: killed`), RetryClassKilled, true},
		{"pg-desk exit -1 text", errors.New(`gather: pg-connector changes: exit -1: boom`), RetryClassKilled, true},
		{"pg-desk error_class killed", errors.New(`stderr tail: {"event":"run_failed","error_class":"killed"}`), RetryClassKilled, true},
		{"context deadline", fmt.Errorf("x: %w", context.DeadlineExceeded), RetryClassDeadline, true},
		{"deadline text", errors.New("rpc: context deadline exceeded"), RetryClassDeadline, true},
		{"pg-desk error_class deadline", errors.New(`{"error_class":"deadline"}`), RetryClassDeadline, true},
		{"unavailable text", errors.New(`wireclient: role "desk-pr" exited 1: connector: unavailable`), RetryClassUnavailable, true},
		{"deterministic exit 1", &wireclient.ExitError{Role: "desk-pr", Code: 1, Detail: "invalid_argument: bad PR id"}, "", false},
		{"cancelled run", fmt.Errorf("x: %w", context.Canceled), "", false},
		{"plain exit 2", &wireclient.ExitError{Role: "desk-pr", Code: 2}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			class, ok := classifyTransient(c.err)
			if class != c.class || ok != c.ok {
				t.Fatalf("classifyTransient(%v) = (%q, %v), want (%q, %v)", c.err, class, ok, c.class, c.ok)
			}
		})
	}
}

func TestExitError_MessageUnchanged(t *testing.T) {
	if got, want := (&wireclient.ExitError{Role: "r", Code: 3}).Error(), `wireclient: role "r" exited 3`; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if got, want := (&wireclient.ExitError{Role: "r", Code: 3, Detail: "why"}).Error(), `wireclient: role "r" exited 3: why`; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// A role that did not opt in (MaxDispatchRetries 0, the default) keeps the
// original behavior: a transient failure is still an ACCEPT, never re-run.
func TestOffer_RetryDisabledByDefault_TransientFailureStillAccepted(t *testing.T) {
	h := &seqHandler{errs: []error{killedErr}}
	l, robs, fobs := newRetryListener(t, h, 0, context.Background())

	got := l.Offer(eventqueue.Offering{ID: "dsp-1", Event: retryEvent("e1")})

	if want := (eventqueue.OfferResult{Accepted: true, Decline: eventqueue.DeclineNone}); got != want {
		t.Fatalf("Offer() = %+v, want %+v", got, want)
	}
	if n := len(robs.snapshot()); n != 0 {
		t.Fatalf("retry observer calls = %d, want 0", n)
	}
	if n := len(fobs.calls); n != 1 {
		t.Fatalf("handler-failure observer calls = %d, want 1", n)
	}
}

// With 2 retries a persistently killed event is declined twice (re-run
// twice, 3 attempts total), then accepted: the cap holds and the metric hook
// fires exactly once per scheduled re-run.
func TestOffer_TransientFailure_RetriesAtMostMaxThenAccepts(t *testing.T) {
	h := &seqHandler{errs: []error{killedErr, killedErr, killedErr, killedErr}}
	l, robs, fobs := newRetryListener(t, h, 2, context.Background())
	evt := retryEvent("e1")

	retryDecline := eventqueue.OfferResult{Accepted: false, Decline: eventqueue.DeclineNone, DeclineDetail: DeclineDetailDispatchRetry}
	for i := 1; i <= 2; i++ {
		if got := l.Offer(eventqueue.Offering{ID: "dsp", Event: evt}); got != retryDecline {
			t.Fatalf("attempt %d: Offer() = %+v, want retry decline %+v", i, got, retryDecline)
		}
	}
	if got, want := l.Offer(eventqueue.Offering{ID: "dsp", Event: evt}), (eventqueue.OfferResult{Accepted: true}); got != want {
		t.Fatalf("attempt 3: Offer() = %+v, want accept %+v (cap exhausted)", got, want)
	}
	if h.callCount() != 3 {
		t.Fatalf("handler dispatches = %d, want 3 (1 original + 2 retries)", h.callCount())
	}
	want := []retryCall{{"desk-pr", RetryClassKilled}, {"desk-pr", RetryClassKilled}}
	if got := robs.snapshot(); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("retry observer calls = %+v, want %+v", got, want)
	}
	if n := len(fobs.calls); n != 3 {
		t.Fatalf("handler-failure observer calls = %d, want 3 (every failed attempt is a failure)", n)
	}
	if rl := l.(*roleListener); rl.retries.pending() != 0 {
		t.Fatalf("retry ledger holds %d entries after exhaustion, want 0", rl.retries.pending())
	}
}

func TestOffer_TransientFailureThenSuccess_AcceptsAndClearsLedger(t *testing.T) {
	h := &seqHandler{errs: []error{killedErr}}
	l, robs, _ := newRetryListener(t, h, 2, context.Background())
	evt := retryEvent("e1")

	if got := l.Offer(eventqueue.Offering{ID: "dsp", Event: evt}); got.Accepted {
		t.Fatalf("first attempt must be a retry decline, got %+v", got)
	}
	if got := l.Offer(eventqueue.Offering{ID: "dsp", Event: evt}); !got.Accepted {
		t.Fatalf("second attempt succeeded and must be accepted, got %+v", got)
	}
	if n := len(robs.snapshot()); n != 1 {
		t.Fatalf("retry observer calls = %d, want 1", n)
	}
	if rl := l.(*roleListener); rl.retries.pending() != 0 {
		t.Fatalf("retry ledger holds %d entries after success, want 0", rl.retries.pending())
	}
}

// The budget is per event: a second event gets its own full allowance.
func TestOffer_RetryBudgetIsPerEvent(t *testing.T) {
	h := &seqHandler{errs: []error{killedErr, killedErr, killedErr, killedErr}}
	l, _, _ := newRetryListener(t, h, 1, context.Background())

	for _, id := range []string{"a", "b"} {
		if got := l.Offer(eventqueue.Offering{ID: "dsp", Event: retryEvent(id)}); got.Accepted {
			t.Fatalf("event %s first failure must be a retry decline, got %+v", id, got)
		}
	}
}

func TestOffer_DeterministicFailureNeverRetried(t *testing.T) {
	h := &seqHandler{errs: []error{&wireclient.ExitError{Role: "desk-pr", Code: 1, Detail: "invalid_argument: bad PR id"}}}
	l, robs, _ := newRetryListener(t, h, 3, context.Background())

	if got := l.Offer(eventqueue.Offering{ID: "dsp", Event: retryEvent("e1")}); !got.Accepted {
		t.Fatalf("a deterministic failure must be accepted (never re-run), got %+v", got)
	}
	if n := len(robs.snapshot()); n != 0 {
		t.Fatalf("retry observer calls = %d, want 0", n)
	}
}

func TestOffer_CancelledRunNeverRetried(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	h := &seqHandler{errs: []error{killedErr}}
	l, robs, _ := newRetryListener(t, h, 3, ctx)
	cancel()

	if got := l.Offer(eventqueue.Offering{ID: "dsp", Event: retryEvent("e1")}); !got.Accepted {
		t.Fatalf("a failure during shutdown must not be re-run, got %+v", got)
	}
	if n := len(robs.snapshot()); n != 0 {
		t.Fatalf("retry observer calls = %d, want 0", n)
	}
}

// An event already past its expiry gets its one (final) attempt; the queue
// settles it either way, so the listener must not ask for another.
func TestOffer_ExpiredEventNeverRetried(t *testing.T) {
	h := &seqHandler{errs: []error{killedErr}}
	l, robs, _ := newRetryListener(t, h, 3, context.Background())
	evt := retryEvent("e1")
	evt.ExpiresAt = time.Now().Add(-time.Second)

	if got := l.Offer(eventqueue.Offering{ID: "dsp", Event: evt}); !got.Accepted {
		t.Fatalf("an expired event's last attempt must be accepted, got %+v", got)
	}
	if n := len(robs.snapshot()); n != 0 {
		t.Fatalf("retry observer calls = %d, want 0", n)
	}
}

func TestOffer_BusyStillDeclinesBusyNotRetry(t *testing.T) {
	h := &seqHandler{errs: []error{&wireclient.BusyDecline{Reason: "at-capacity"}}}
	l, robs, _ := newRetryListener(t, h, 3, context.Background())

	got := l.Offer(eventqueue.Offering{ID: "dsp", Event: retryEvent("e1")})
	if got.Accepted || got.Decline != eventqueue.DeclineBusy || got.DeclineDetail != "at-capacity" {
		t.Fatalf("Offer() = %+v, want the unchanged busy decline", got)
	}
	if n := len(robs.snapshot()); n != 0 {
		t.Fatalf("retry observer calls = %d, want 0", n)
	}
}

func TestNewListener_ClampsMaxDispatchRetriesToCap(t *testing.T) {
	for _, c := range []struct{ in, want int }{{-5, 0}, {0, 0}, {2, 2}, {roles.MaxDispatchRetriesCap, roles.MaxDispatchRetriesCap}, {99, roles.MaxDispatchRetriesCap}} {
		h := &seqHandler{}
		l, _, _ := newRetryListener(t, h, c.in, context.Background())
		if got := l.(*roleListener).maxRetries; got != c.want {
			t.Errorf("MaxDispatchRetries %d: listener maxRetries = %d, want %d", c.in, got, c.want)
		}
	}
}

// End to end through the real queue: a killed dispatch is re-offered after the
// role's backoff WITHOUT waiting for any other trigger (the pr-sweep this bead
// exists to make unnecessary), and is settled once it succeeds.
func TestQueue_TransientFailureIsReofferedAtBackoffThenSettles(t *testing.T) {
	now := time.Now() // real time: roleListener judges expiry against time.Now()
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); defer mu.Unlock(); now = now.Add(d) }

	q, err := eventqueue.New(eventqueue.NewMemStore(), eventqueue.WithClock(clock),
		eventqueue.WithRetryBackoff(backoff.Policy{Initial: 5 * time.Second, Factor: 2, Max: time.Minute}))
	if err != nil {
		t.Fatal(err)
	}
	h := &seqHandler{errs: []error{killedErr}}
	o := newOrch(fastCfg(t), testQuerySet(nil, nil))
	o.Handler = h
	q.Register(o.NewListener(context.Background(), roles.Role{Name: "desk-pr", Binds: []string{"pr.changed"}, MaxDispatchRetries: 2}))

	evt := eventqueue.Event{ID: "e1", Type: "pr.changed", At: now, ExpiresAt: now.Add(time.Hour), Payload: map[string]any{"id": "pr-1"}}
	if _, err := q.Enqueue(evt); err != nil {
		t.Fatal(err)
	}

	if got := q.Dispatch(); got != 0 || h.callCount() != 1 {
		t.Fatalf("pass 1: accepted=%d calls=%d, want 0 accepted (retry decline) and 1 call", got, h.callCount())
	}
	if got := q.Dispatch(); got != 0 || h.callCount() != 1 {
		t.Fatalf("pass 2 (inside the backoff): accepted=%d calls=%d, want the retry withheld", got, h.callCount())
	}
	advance(6 * time.Second)
	if got := q.Dispatch(); got != 1 || h.callCount() != 2 {
		t.Fatalf("pass 3 (after the backoff): accepted=%d calls=%d, want the re-run accepted", got, h.callCount())
	}
}

// stdoutCmd is a query.Commander double returning canned stdout for the
// command query the end-to-end retry tests drive through ProduceTick.
type stdoutCmd struct{ out string }

func (c stdoutCmd) Run(context.Context, []string) ([]byte, error) { return []byte(c.out), nil }

// retryE2E wires the WHOLE producer-to-handler path a deployed desk role takes:
// a real query.CommandQuery decodes adapter stdout, Orchestrator.ProduceTick
// (discover.Produce -> ToQueueEvent) enqueues it, and the real roleListener
// offers it to a handler whose dispatches always die killed. Unlike the tests
// above, no eventqueue.Event is constructed by hand, so an adapter's expiresAt
// only helps if it really survives the discover path (bead pg2-d9yi7).
type retryE2E struct {
	q       *eventqueue.Queue
	o       *Orchestrator
	h       *seqHandler
	robs    *fakeRetryObserver
	advance func(time.Duration)
}

func newRetryE2E(t *testing.T, stdout string, maxRetries int) *retryE2E {
	t.Helper()
	now := time.Now() // real time: roleListener judges expiry against time.Now()
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); defer mu.Unlock(); now = now.Add(d) }

	q, err := eventqueue.New(eventqueue.NewMemStore(), eventqueue.WithClock(clock),
		eventqueue.WithRetryBackoff(backoff.Policy{Initial: 5 * time.Second, Factor: 2, Max: time.Minute}))
	if err != nil {
		t.Fatal(err)
	}
	sources := query.SourceSet{{Name: "desk-pr-changes", Query: query.CommandQuery{
		Meta:   query.Meta{EmitTypes: []string{"pr.changed"}, Trig: query.PeriodTrigger{}},
		Argv:   []string{"adapter"},
		Format: query.FormatJSONL,
	}}}
	o := newOrch(fastCfg(t), sources)
	o.Cmd = stdoutCmd{out: stdout}
	o.Bindings = core.NewBindings("pr.changed")
	h := &seqHandler{errs: []error{killedErr, killedErr, killedErr, killedErr, killedErr, killedErr}}
	o.Handler = h
	robs := &fakeRetryObserver{}
	o.DispatchRetryObserver = robs
	q.Register(o.NewListener(context.Background(), roles.Role{Name: "desk-pr", Binds: []string{"pr.changed"}, MaxDispatchRetries: maxRetries}))

	if _, err := o.ProduceTick(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	return &retryE2E{q: q, o: o, h: h, robs: robs, advance: advance}
}

// settle runs n dispatch passes, each after letting the queue's retry backoff
// (5s, then 10s, capped at 1m) elapse.
func (e *retryE2E) settle(n int) {
	for i := 0; i < n; i++ {
		e.advance(time.Minute)
		e.q.Dispatch()
		e.q.Expire()
	}
}

// A command-query record carrying an adapter-supplied expiresAt is re-offered
// after a transient (killed) failure, up to max_dispatch_retries, and then
// stops: 1 original + 2 re-runs = 3 attempts, 2 pg_router_dispatch_retries
// increments (the exporter appends _total).
func TestE2E_CommandQueryExpiresAt_KilledDispatchRetriesUpToMaxThenStops(t *testing.T) {
	exp := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	e := newRetryE2E(t, `{"id":"pr-1","type":"pr","expiresAt":"`+exp+`"}`+"\n", 2)

	e.q.Dispatch()
	if e.h.callCount() != 1 || len(e.robs.snapshot()) != 1 {
		t.Fatalf("first attempt: calls=%d retries=%d, want 1 and 1 (re-run scheduled)", e.h.callCount(), len(e.robs.snapshot()))
	}
	e.settle(6)
	if got := e.h.callCount(); got != 3 {
		t.Fatalf("handler attempts = %d, want 3 (1 original + max_dispatch_retries 2)", got)
	}
	want := []retryCall{{"desk-pr", RetryClassKilled}, {"desk-pr", RetryClassKilled}}
	if got := e.robs.snapshot(); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("retry observer calls = %+v, want %+v", got, want)
	}
}

// Without an expiresAt the event is born expired (INV-EVT-1): even a role that
// opted in to retries gets exactly one attempt and no re-run is scheduled.
func TestE2E_CommandQueryWithoutExpiresAt_GetsExactlyOneAttempt(t *testing.T) {
	e := newRetryE2E(t, `{"id":"pr-1","type":"pr"}`+"\n", 2)

	e.q.Dispatch()
	e.settle(6)
	if got := e.h.callCount(); got != 1 {
		t.Fatalf("handler attempts = %d, want exactly 1 (no retry window)", got)
	}
	if n := len(e.robs.snapshot()); n != 0 {
		t.Fatalf("retry observer calls = %d, want 0", n)
	}
}
