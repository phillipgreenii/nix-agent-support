package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

const requestsBucket governor.Bucket = "requests"

// admitExec is an executor that spends through a real governor: it admits one
// request, holds the reservation until the test releases it, then settles. It
// reports every attempt on entered (true when admitted), so a test can wait for
// exactly the executions a Step started without a sleep.
type admitExec struct {
	gov     *governor.Governor
	clock   governor.Clock
	entered chan bool
	release chan struct{}

	mu   sync.Mutex
	done []time.Time
}

func (e *admitExec) MaxBatch() int { return 1 }

func (e *admitExec) Spend(class governor.Class, n int) governor.Request {
	return governor.Request{Bucket: requestsBucket, Class: class, Cost: 1}
}

func (e *admitExec) Execute(_ context.Context, items []Item) []Result {
	tk, err := e.gov.Admit(e.Spend(governor.ClassQuery, 1))
	if err != nil {
		e.entered <- false
		return []Result{{Err: err}}
	}
	e.entered <- true
	<-e.release
	if err := tk.Settle(governor.Spend{Purpose: governor.PurposeQuery, Cost: 1, Outcome: "ok"}); err != nil {
		return []Result{{Err: err}}
	}
	e.mu.Lock()
	e.done = append(e.done, e.clock.Now())
	e.mu.Unlock()
	return make([]Result, len(items))
}

// round runs the scheduler at the current time until a Step starts nothing:
// every batch a Step started has attempted its admission before the reservations
// are released, so two workers contend for the cap exactly as they do live.
func (e *admitExec) round(t *testing.T, s *Scheduler) {
	t.Helper()
	for i := 0; i < 100; i++ {
		n := s.Step(context.Background())
		if n == 0 {
			return
		}
		admitted := 0
		for j := 0; j < n; j++ {
			if <-e.entered {
				admitted++
			}
		}
		for j := 0; j < admitted; j++ {
			e.release <- struct{}{}
		}
		s.Wait()
	}
	t.Fatal("a round never settled")
}

// TestMoreWorkersThanTheBackgroundCapStillSpendTheBackgroundCap is the default
// Jira shape: a cap of 2 per second, a background share of 0.9 (a background cap
// of 1) and 2 workers. A background batch refused only because the other worker
// holds the one unit must wait for that worker to settle, not for RetryDelay: the
// daemon then spends its background cap every second instead of one request per
// RetryDelay (30 s).
func TestMoreWorkersThanTheBackgroundCapStillSpendTheBackgroundCap(t *testing.T) {
	clk := governor.NewManualClock(t0)
	gov, err := governor.New(governor.Config{
		Declared:        []governor.BucketSpec{{Bucket: requestsBucket, Unit: "requests", Limits: governor.Limits{Cap: 2, Window: time.Second}}},
		BackgroundShare: 0.9,
	}, governor.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	if got := backgroundCapOf(t, gov); got != 1 {
		t.Fatalf("background cap = %d, want 1 (the shape under test)", got)
	}
	e := &admitExec{gov: gov, clock: clk, entered: make(chan bool), release: make(chan struct{})}
	s := New(Options{Clock: clk, Gate: gov, Workers: 2})
	s.Register("k", e)
	const tasks = 20
	for i := 0; i < tasks; i++ {
		mustSubmit(t, s, Task{Kind: "k", Entity: fmt.Sprintf("q%02d", i), Group: "g", Class: governor.ClassQuery, Due: t0})
	}

	const seconds = 25
	for sec := 0; sec < seconds; sec++ {
		e.round(t, s)
		clk.Advance(time.Second)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	// 20 due tasks at one per second finish inside 25 s; with the 30 s
	// RetryDelay hold only the first one does.
	if len(e.done) != tasks {
		t.Fatalf("%d of %d background tasks ran in %d s; want all of them (about one per second)", len(e.done), tasks, seconds)
	}
	perSecond := map[time.Time]int{}
	for _, at := range e.done {
		perSecond[at]++
	}
	for at, n := range perSecond {
		if n > 1 {
			t.Errorf("%d background requests at %v, over the background cap of 1 per second", n, at)
		}
	}
}

func backgroundCapOf(t *testing.T, g *governor.Governor) int64 {
	t.Helper()
	bs, ok := g.State().Bucket(requestsBucket)
	if !ok {
		t.Fatal("no requests bucket")
	}
	return bs.BackgroundCap
}

// TestAFinishedBatchEndsAnInFlightHold: a batch refused only by the reservation
// of a running one is retried when that one finishes, without the clock moving.
func TestAFinishedBatchEndsAnInFlightHold(t *testing.T) {
	// The gate stands for a governor whose one background unit is reserved from
	// the moment a batch is let through until it finishes.
	var reserved atomic.Int32
	gate := gateFunc(func(governor.Request) error {
		if reserved.Load() > 0 {
			return &governor.Refusal{Kind: governor.RefusedBackgroundShare, InFlight: true}
		}
		reserved.Add(1)
		return nil
	})
	block := make(chan struct{})
	e := bucketExec(governor.BucketGraphQL, 1)
	e.fn = func(items []Item) []Result {
		if items[0].Task.Entity == "a" {
			<-block
		}
		reserved.Add(-1)
		return make([]Result, len(items))
	}
	s, _ := newSched(t, Options{Gate: gate, Workers: 2}, e)
	mustSubmit(t, s, task("a", governor.ClassQuery, t0))
	mustSubmit(t, s, task("b", governor.ClassQuery, t0))

	if n := s.Step(context.Background()); n != 1 {
		t.Fatalf("first Step started %d batches, want 1 (b is refused while a runs)", n)
	}
	if !s.Queued("b", "g") {
		t.Fatal("b is not queued behind the refusal")
	}
	close(block)
	s.Wait()
	// a has finished; the clock has not moved, and the hold (1 s at most, never
	// the 30 s RetryDelay) is already over because a reservation settled.
	s.Drain(context.Background())
	if got := len(e.batches()); got != 2 {
		t.Fatalf("%d batches ran, want 2: the in-flight hold outlived the batch that caused it", got)
	}
}

func TestAnInFlightHoldIsBoundedByTheInFlightRetryDelay(t *testing.T) {
	cases := []struct {
		name string
		opt  Options
		ref  *governor.Refusal
		want time.Duration
	}{
		{"the default bound is a second", Options{}, &governor.Refusal{Kind: governor.RefusedCap, InFlight: true}, time.Second},
		{"the bound is configurable", Options{InFlightRetryDelay: 3 * time.Second}, &governor.Refusal{Kind: governor.RefusedCap, InFlight: true}, 3 * time.Second},
		{"a sooner RetryAt shortens it", Options{}, &governor.Refusal{Kind: governor.RefusedCap, InFlight: true, RetryAt: t0.Add(400 * time.Millisecond)}, 400 * time.Millisecond},
		{"a later RetryAt does not lengthen it", Options{}, &governor.Refusal{Kind: governor.RefusedCap, InFlight: true, RetryAt: t0.Add(time.Hour)}, time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.opt.Clock = governor.NewManualClock(t0)
			c.opt.RetryDelay = 30 * time.Second
			s := New(c.opt)
			until, settle := s.holdUntil(c.ref, t0)
			if !settle || until.Sub(t0) != c.want {
				t.Errorf("hold = %v (in-flight %v), want %v in-flight", until.Sub(t0), settle, c.want)
			}
		})
	}
	s := New(Options{Clock: governor.NewManualClock(t0), RetryDelay: 30 * time.Second})
	if until, settle := s.holdUntil(errors.New("other"), t0); settle || until.Sub(t0) != 30*time.Second {
		t.Errorf("a plain error holds %v (in-flight %v), want the 30s retry delay", until.Sub(t0), settle)
	}
}

func TestAnInFlightHoldNeverShortensALongerHold(t *testing.T) {
	s := New(Options{Clock: governor.NewManualClock(t0)})
	s.setHold(false, governor.BucketGraphQL, t0.Add(time.Hour), false)
	s.setHold(false, governor.BucketGraphQL, t0.Add(time.Second), true)
	if got := s.holdBG[governor.BucketGraphQL]; !got.Equal(t0.Add(time.Hour)) {
		t.Errorf("hold = %v, want the hour-long hold kept", got)
	}
	s.endSettleHolds()
	if got := s.holdBG[governor.BucketGraphQL]; !got.Equal(t0.Add(time.Hour)) {
		t.Errorf("a finished batch lifted a hold that was not an in-flight one: %v", got)
	}
}
