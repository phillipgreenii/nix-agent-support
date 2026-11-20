package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/backoff"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

// alwaysKilled is a handler whose every dispatch is killed (a transient class),
// except the ids listed in ok, which succeed.
type alwaysKilled struct {
	mu    sync.Mutex
	calls []string
	ok    map[string]bool
	err   error
}

func (h *alwaysKilled) Dispatch(_ context.Context, _ roles.Role, evt eventqueue.Event) (wireclient.Reply, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, evt.ID)
	if h.ok[evt.ID] {
		return wireclient.Reply{ID: "dsp", Outcome: "ok"}, nil
	}
	if h.err != nil {
		return wireclient.Reply{}, h.err
	}
	return wireclient.Reply{}, killedErr
}

func (h *alwaysKilled) PostStartup(context.Context, roles.Role) (wireclient.Reply, error) {
	return wireclient.Reply{}, nil
}

func (h *alwaysKilled) PreShutdown(context.Context, roles.Role) (wireclient.Reply, error) {
	return wireclient.Reply{}, nil
}
func (h *alwaysKilled) n() int { h.mu.Lock(); defer h.mu.Unlock(); return len(h.calls) }

type (
	redriveCall         struct{ role, class string }
	fakeRedriveObserver struct{ calls []redriveCall }
)

func (f *fakeRedriveObserver) OnDispatchRedrive(role, class string) {
	f.calls = append(f.calls, redriveCall{role, class})
}

// redriveRig is a real queue (fake clock) with a real roleListener for role
// "desk-pr", the listener's failure clock shared with the queue's.
type redriveRig struct {
	q       *eventqueue.Queue
	h       *alwaysKilled
	redrive *fakeRedriveObserver
	failed  *fakeDispatchFailedObserver
	now     *time.Time
}

func (r *redriveRig) advance(d time.Duration) { *r.now = r.now.Add(d) }

func newRedriveRig(t *testing.T, store eventqueue.Store, start time.Time, h *alwaysKilled, maxRetries, maxRedrives int) *redriveRig {
	t.Helper()
	now := start
	r := &redriveRig{h: h, redrive: &fakeRedriveObserver{}, failed: &fakeDispatchFailedObserver{}, now: &now}
	q, err := eventqueue.New(store, eventqueue.WithClock(func() time.Time { return now }),
		eventqueue.WithRetryBackoff(backoff.Policy{Initial: time.Second, Factor: 1, Max: time.Second}))
	if err != nil {
		t.Fatal(err)
	}
	r.q = q
	o := newOrch(fastCfg(t), testQuerySet(nil, nil))
	o.Handler = h
	o.RetryLog, o.FailedLog, o.RedriveQueue = q, q, q
	o.DispatchRedriveObserver, o.DispatchFailedObserver = r.redrive, r.failed
	o.failedNow = func() time.Time { return now }
	q.Register(o.NewListener(context.Background(), roles.Role{
		Name: "desk-pr", Binds: []string{"pr.changed"},
		MaxDispatchRetries: maxRetries, MaxRedrives: maxRedrives, RedriveDelay: time.Hour,
	}))
	return r
}

// run dispatches in 2-minute clock steps for at most 400 steps (8 hours of fake
// time), stopping early once the queue has been quiet for 40 steps.
func (r *redriveRig) run() {
	quiet := 0
	for i := 0; i < 400 && quiet < 40; i++ {
		before := r.h.n()
		r.q.Dispatch()
		if r.h.n() == before {
			quiet++
		} else {
			quiet = 0
		}
		r.advance(2 * time.Minute)
	}
}

func originalEvent(id string, at time.Time) eventqueue.Event {
	return eventqueue.Event{ID: id, Type: "pr.changed", At: at, ExpiresAt: at.Add(6 * time.Hour), Payload: map[string]any{"id": "pr-1"}}
}

// With max_redrives 0 (the default) behavior is unchanged: the failed attempt is
// accepted, counted as a failed dispatch, and nothing is enqueued.
func TestRedrive_DisabledByDefault_BehaviorUnchanged(t *testing.T) {
	start := time.Now()
	r := newRedriveRig(t, eventqueue.NewMemStore(), start, &alwaysKilled{}, 0, 0)
	if _, err := r.q.Enqueue(originalEvent("pr-1@a", start)); err != nil {
		t.Fatal(err)
	}
	r.run()
	if r.h.n() != 1 || len(r.redrive.calls) != 0 || len(r.failed.calls) != 1 {
		t.Fatalf("attempts=%d redrives=%v failed=%v, want 1, none, 1", r.h.n(), r.redrive.calls, r.failed.calls)
	}
	if got := len(r.q.DeadLetters()); got != 1 {
		t.Fatalf("dead letters = %d, want 1", got)
	}
}

// One original event is attempted at most (1+R) x (1+M) times, a redrive per
// generation counts once, and only the last generation counts as a failed dispatch
// and as a dead letter (ADR 0121 D4, D5).
func TestRedrive_AttemptBoundIsOnePlusRTimesOnePlusM(t *testing.T) {
	for _, c := range []struct{ m, r int }{{0, 1}, {1, 1}, {2, 1}, {1, 2}, {2, 2}, {3, 2}} {
		start := time.Now()
		rig := newRedriveRig(t, eventqueue.NewMemStore(), start, &alwaysKilled{}, c.m, c.r)
		if _, err := rig.q.Enqueue(originalEvent("pr-1@a", start)); err != nil {
			t.Fatal(err)
		}
		rig.run()
		want := (1 + c.r) * (1 + c.m)
		if got := rig.h.n(); got != want {
			t.Errorf("M=%d R=%d: attempts = %d, want %d (calls %v)", c.m, c.r, got, want, rig.h.calls)
		}
		if got := len(rig.redrive.calls); got != c.r {
			t.Errorf("M=%d R=%d: redrives counted = %d, want %d", c.m, c.r, got, c.r)
		}
		if got := len(rig.failed.calls); got != 1 {
			t.Errorf("M=%d R=%d: failed dispatches counted = %d, want 1 (the last generation only)", c.m, c.r, got)
		}
		if dl := rig.q.DeadLetters(); len(dl) != 1 || dl[0].EventID != eventqueue.RedriveID("pr-1@a", c.r) {
			t.Errorf("M=%d R=%d: dead letters = %+v, want one for generation %d", c.m, c.r, dl, c.r)
		}
	}
}

// A redrive is not offered before its notBefore (one hour here).
func TestRedrive_NotOfferedBeforeNotBefore(t *testing.T) {
	start := time.Now()
	rig := newRedriveRig(t, eventqueue.NewMemStore(), start, &alwaysKilled{}, 0, 1)
	if _, err := rig.q.Enqueue(originalEvent("pr-1@a", start)); err != nil {
		t.Fatal(err)
	}
	rig.q.Dispatch()
	if rig.h.n() != 1 {
		t.Fatalf("attempts = %d, want 1", rig.h.n())
	}
	rig.advance(30 * time.Minute)
	rig.q.Dispatch()
	if rig.h.n() != 1 {
		t.Fatalf("the redrive was offered %v before its notBefore", time.Hour-30*time.Minute)
	}
	rig.advance(31 * time.Minute)
	rig.q.Dispatch()
	if rig.h.n() != 2 || rig.h.calls[1] != "pr-1@a~r1" {
		t.Fatalf("calls = %v, want the redrive pr-1@a~r1 second", rig.h.calls)
	}
}

// A deterministic failure is never redriven, whatever max_redrives says.
func TestRedrive_DeterministicFailureNeverRedriven(t *testing.T) {
	start := time.Now()
	h := &alwaysKilled{err: &wireclient.ExitError{Role: "desk-pr", Code: 1, Detail: "invalid_argument: bad PR id"}}
	rig := newRedriveRig(t, eventqueue.NewMemStore(), start, h, 2, 2)
	if _, err := rig.q.Enqueue(originalEvent("pr-1@a", start)); err != nil {
		t.Fatal(err)
	}
	rig.run()
	if h.n() != 1 || len(rig.redrive.calls) != 0 || len(rig.failed.calls) != 1 || len(rig.q.DeadLetters()) != 1 {
		t.Fatalf("attempts=%d redrives=%v failed=%v dead=%d, want 1, none, 1, 1", h.n(), rig.redrive.calls, rig.failed.calls, len(rig.q.DeadLetters()))
	}
}

// A restart mid-redrive keeps the generation: the redrive survives in the file-backed
// log with generation 1, and its own failure on the new process is final at
// max_redrives 1 (it is not redriven again, nor reset to generation 0).
func TestRedrive_RestartMidRedriveKeepsGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	open := func() *eventqueue.FileStore {
		fs, err := eventqueue.NewFileStore(path)
		if err != nil {
			t.Fatal(err)
		}
		return fs
	}
	start := time.Now()
	fs1 := open()
	rig1 := newRedriveRig(t, fs1, start, &alwaysKilled{}, 0, 1)
	if _, err := rig1.q.Enqueue(originalEvent("pr-1@a", start)); err != nil {
		t.Fatal(err)
	}
	rig1.q.Dispatch() // generation 0 fails; a redrive is enqueued
	if len(rig1.redrive.calls) != 1 {
		t.Fatalf("redrives = %v, want 1", rig1.redrive.calls)
	}
	_ = fs1.Close() // restart

	fs2 := open()
	defer func() { _ = fs2.Close() }()
	rig2 := newRedriveRig(t, fs2, start.Add(2*time.Hour), &alwaysKilled{}, 0, 1)
	rig2.run()
	if len(rig2.h.calls) != 1 || rig2.h.calls[0] != "pr-1@a~r1" {
		t.Fatalf("calls after restart = %v, want only the redrive pr-1@a~r1", rig2.h.calls)
	}
	if len(rig2.redrive.calls) != 0 || len(rig2.failed.calls) != 1 {
		t.Fatalf("after restart: redrives=%v failed=%v, want none and the one terminal failure", rig2.redrive.calls, rig2.failed.calls)
	}
}

// A redrive is skipped when the role has since accepted a later event of the same
// entity: only the last generation's failure then counts, and no second redrive is made.
func TestRedrive_SkippedWhenALaterEventForTheEntityWasAccepted(t *testing.T) {
	start := time.Now()
	h := &alwaysKilled{ok: map[string]bool{"pr-1@b": true}}
	rig := newRedriveRig(t, eventqueue.NewMemStore(), start, h, 0, 2)
	if _, err := rig.q.Enqueue(originalEvent("pr-1@a", start)); err != nil {
		t.Fatal(err)
	}
	rig.q.Dispatch() // pr-1@a fails; redrive pr-1@a~r1 pending
	rig.advance(time.Minute)
	if _, err := rig.q.Enqueue(originalEvent("pr-1@b", *rig.now)); err != nil {
		t.Fatal(err)
	}
	rig.q.Dispatch() // pr-1@b succeeds
	rig.advance(2 * time.Hour)
	rig.q.Dispatch() // pr-1@a~r1 fails: a later event for pr-1 was accepted
	if len(rig.redrive.calls) != 1 {
		t.Fatalf("redrives = %v, want only the first", rig.redrive.calls)
	}
	if len(rig.failed.calls) != 1 {
		t.Fatalf("failed dispatches = %v, want 1", rig.failed.calls)
	}
}

type errRedriveQueue struct{}

func (errRedriveQueue) Enqueue(eventqueue.Event) (eventqueue.EnqueueResult, error) {
	return eventqueue.Enqueued, errors.New("log full")
}
func (errRedriveQueue) AcceptedAfter(string, string, time.Time) bool { return false }

// A redrive that cannot be enqueued counts as a normal terminal failure.
func TestRedrive_EnqueueFailureCountsAsTerminalFailure(t *testing.T) {
	o := newOrch(fastCfg(t), testQuerySet(nil, nil))
	o.Handler = &alwaysKilled{}
	o.RedriveQueue = errRedriveQueue{}
	robs, fobs := &fakeRedriveObserver{}, &fakeDispatchFailedObserver{}
	o.DispatchRedriveObserver, o.DispatchFailedObserver = robs, fobs
	l := o.NewListener(context.Background(), roles.Role{Name: "desk-pr", Binds: []string{"pr.changed"}, MaxRedrives: 1})
	if got := l.Offer(eventqueue.Offering{ID: "d", Event: retryEvent("pr-1@a")}); !got.Accepted {
		t.Fatalf("Offer = %+v, want accepted", got)
	}
	if len(robs.calls) != 0 || len(fobs.calls) != 1 {
		t.Fatalf("redrives=%v failed=%v, want none and 1", robs.calls, fobs.calls)
	}
}

type recordingRedriveQueue struct{ enqueued []eventqueue.Event }

func (r *recordingRedriveQueue) Enqueue(e eventqueue.Event) (eventqueue.EnqueueResult, error) {
	r.enqueued = append(r.enqueued, e)
	return eventqueue.Enqueued, nil
}
func (r *recordingRedriveQueue) AcceptedAfter(string, string, time.Time) bool { return false }

// The redrive's fields: new id, generation + 1, notBefore now + delay, a window
// of the original's length from notBefore, same type and payload; a shutdown
// never enqueues one; the cap is clamped to the hard cap of 2.
func TestRedrive_EnqueuedFieldsShutdownAndCap(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	mk := func(ctx context.Context, maxRedrives int) (eventqueue.Listener, *recordingRedriveQueue) {
		o := newOrch(fastCfg(t), testQuerySet(nil, nil))
		o.Handler = &alwaysKilled{}
		rq := &recordingRedriveQueue{}
		o.RedriveQueue = rq
		o.failedNow = func() time.Time { return now }
		return o.NewListener(ctx, roles.Role{Name: "desk-pr", Binds: []string{"pr.changed"}, MaxRedrives: maxRedrives, RedriveDelay: 90 * time.Minute}), rq
	}
	evt := eventqueue.Event{ID: "pr-1@a", Type: "pr.changed", At: now.Add(-7 * time.Hour), ExpiresAt: now.Add(-time.Hour), Payload: map[string]any{"id": "pr-1"}}

	l, rq := mk(context.Background(), 1)
	l.Offer(eventqueue.Offering{ID: "d", Event: evt})
	if len(rq.enqueued) != 1 {
		t.Fatalf("enqueued = %d, want 1 (window closed on a transient class)", len(rq.enqueued))
	}
	got := rq.enqueued[0]
	if got.ID != "pr-1@a~r1" || got.Generation != 1 || got.Type != "pr.changed" || got.Payload["id"] != "pr-1" ||
		!got.NotBefore.Equal(now.Add(90*time.Minute)) || !got.ExpiresAt.Equal(got.NotBefore.Add(6*time.Hour)) {
		t.Fatalf("redrive = %+v", got)
	}

	// Generation at the cap: not redriven. A cap above 2 is clamped to 2.
	l, rq = mk(context.Background(), 9)
	for gen, want := range map[int]int{0: 1, 1: 1, 2: 0} {
		rq.enqueued = nil
		e := evt
		e.Generation = gen
		l.Offer(eventqueue.Offering{ID: "d", Event: e})
		if len(rq.enqueued) != want {
			t.Errorf("generation %d: enqueued %d, want %d", gen, len(rq.enqueued), want)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	l, rq = mk(ctx, 1)
	cancel()
	l.Offer(eventqueue.Offering{ID: "d", Event: evt})
	if len(rq.enqueued) != 0 {
		t.Fatalf("shutdown enqueued %d redrives, want 0", len(rq.enqueued))
	}
}
