package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

// gateFunc adapts a function to the Gate the scheduler asks before a batch.
type gateFunc func(governor.Request) error

func (g gateFunc) Check(r governor.Request) error { return g(r) }

// bucketExec is an executor that is metered against one bucket and records the
// items it is handed.
func bucketExec(b governor.Bucket, max int) *fakeExec { return &fakeExec{max: max, bucket: b} }

// completed reports whether a flight has finished, without blocking.
func completed(f *Flight) bool {
	select {
	case <-f.Done():
		return true
	default:
		return false
	}
}

func TestHeldFollowsTheClassGroupOfTheHold(t *testing.T) {
	clk := governor.NewManualClock(t0)
	far := t0.Add(time.Hour)
	mk := func(c governor.Class) *entry {
		return &entry{Task: Task{Kind: "k", Entity: "e", Group: "g", Class: c}}
	}
	metered := bucketExec(governor.BucketGraphQL, 1)

	t.Run("no executor holds nothing back", func(t *testing.T) {
		s := New(Options{Clock: clk})
		s.holdBG[governor.BucketGraphQL] = far
		if s.held(mk(governor.ClassKeepAlive), nil, t0) {
			t.Fatal("a task with no executor was reported held")
		}
	})
	t.Run("an unmetered batch is never held", func(t *testing.T) {
		s := New(Options{Clock: clk})
		s.holdBG[""], s.holdFG[""] = far, far
		if s.held(mk(governor.ClassKeepAlive), &fakeExec{max: 1}, t0) {
			t.Fatal("a batch with no bucket was held back")
		}
	})
	t.Run("a foreground hold holds invalidated work only", func(t *testing.T) {
		s := New(Options{Clock: clk})
		s.holdFG[governor.BucketGraphQL] = far
		want := map[governor.Class]bool{
			governor.ClassInteractive: false, governor.ClassInvalidated: true, governor.ClassQuery: false,
			governor.ClassKeepAlive: false, governor.ClassFill: false,
		}
		for c, w := range want {
			if got := s.held(mk(c), metered, t0); got != w {
				t.Errorf("class %v held = %v, want %v", c, got, w)
			}
		}
	})
	t.Run("a background hold holds the background classes and not invalidated work", func(t *testing.T) {
		s := New(Options{Clock: clk})
		s.holdBG[governor.BucketGraphQL] = far
		want := map[governor.Class]bool{
			governor.ClassInteractive: false, governor.ClassInvalidated: false, governor.ClassQuery: true,
			governor.ClassKeepAlive: true, governor.ClassFill: true,
		}
		for c, w := range want {
			if got := s.held(mk(c), metered, t0); got != w {
				t.Errorf("class %v held = %v, want %v", c, got, w)
			}
		}
	})
	t.Run("a hold ends at its time and is per bucket", func(t *testing.T) {
		s := New(Options{Clock: clk})
		s.holdBG[governor.BucketGraphQL] = far
		if s.held(mk(governor.ClassKeepAlive), metered, far) {
			t.Error("still held at the instant the hold ends")
		}
		if !s.held(mk(governor.ClassKeepAlive), metered, far.Add(-time.Nanosecond)) {
			t.Error("not held a nanosecond before the hold ends")
		}
		if s.held(mk(governor.ClassKeepAlive), bucketExec(governor.BucketRESTCore, 1), t0) {
			t.Error("a hold on one bucket held a batch of another")
		}
	})
}

func TestNextBatchSkipsTasksOfAKindWithoutAnExecutor(t *testing.T) {
	e := &fakeExec{max: 4}
	s, _ := newSched(t, Options{Workers: 2}, e)
	mustSubmit(t, s, Task{Kind: "ghost", Entity: "orphan", Group: "g", Class: governor.ClassInteractive})
	if n := s.Step(context.Background()); n != 0 {
		t.Fatalf("Step started %d batches for a kind nobody registered", n)
	}
	mustSubmit(t, s, task("real", governor.ClassKeepAlive, t0))
	if n := s.Step(context.Background()); n != 1 {
		t.Fatalf("Step started %d batches, want 1 (the registered kind)", n)
	}
	s.Wait()
	if got := fmt.Sprint(ents(e.batches()[0])); got != "[real]" {
		t.Fatalf("batch = %s, want [real]", got)
	}
	if !s.Queued("orphan", "g") {
		t.Error("a task with no executor must stay queued until one is registered")
	}
}

func TestATaskForARunningKeyWaitsForTheRunToFinish(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 4)
	e := &fakeExec{max: 1}
	e.fn = func(items []Item) []Result {
		entered <- struct{}{}
		<-release
		return make([]Result, len(items))
	}
	s, _ := newSched(t, Options{Workers: 3}, e)
	mustSubmit(t, s, task("a", governor.ClassKeepAlive, t0))
	if n := s.Step(context.Background()); n != 1 {
		t.Fatalf("first Step started %d, want 1", n)
	}
	<-entered
	// The same pair is submitted again while it runs: it is queued for after the
	// run, due now, and must not start beside the running one.
	mustSubmit(t, s, task("a", governor.ClassKeepAlive, t0))
	if n := s.Step(context.Background()); n != 0 {
		t.Fatalf("Step started %d batches for a pair that is running", n)
	}
	// A task that is not due and a running pair: still nothing starts.
	mustSubmit(t, s, task("later", governor.ClassKeepAlive, t0.Add(time.Hour)))
	if n := s.Step(context.Background()); n != 0 {
		t.Fatalf("Step started %d batches, want none (one running, one not due)", n)
	}
	close(release)
	s.Wait()
	if got := len(e.batches()); got != 1 {
		t.Fatalf("executor ran %d times, want 1 before the queued task is dispatched", got)
	}
	if !s.Queued("a", "g") {
		t.Error("the task submitted while the pair ran must stay queued")
	}
}

func TestANotYetDueTaskIsNeverALead(t *testing.T) {
	e := &fakeExec{max: 4}
	s, clk := newSched(t, Options{Workers: 2}, e)
	mustSubmit(t, s, task("soon", governor.ClassInteractive, t0.Add(time.Minute)))
	if n := s.Step(context.Background()); n != 0 {
		t.Fatalf("Step started %d batches for a task that is not due", n)
	}
	clk.Advance(time.Minute)
	if n := s.Step(context.Background()); n != 1 {
		t.Fatalf("Step started %d batches at the due time, want 1", n)
	}
	s.Wait()
}

func TestABatchNeverMixesKinds(t *testing.T) {
	s := New(Options{Clock: governor.NewManualClock(t0), Workers: 1, FillHorizon: time.Hour})
	execs := map[string]*fakeExec{}
	for _, k := range []string{"a", "b", "c"} {
		execs[k] = &fakeExec{max: 10}
		s.Register(k, execs[k])
	}
	// "b" is the lead kind; "a" sorts before it and "c" after it, so an
	// ordering comparison on the kind name goes wrong in a different way on
	// each side.
	mustSubmit(t, s, Task{Kind: "b", Entity: "b-lead", Group: "g", Class: governor.ClassQuery, Due: t0})
	mustSubmit(t, s, Task{Kind: "b", Entity: "b-2", Group: "g", Class: governor.ClassKeepAlive, Due: t0})
	mustSubmit(t, s, Task{Kind: "a", Entity: "a-due", Group: "g", Class: governor.ClassKeepAlive, Due: t0})
	mustSubmit(t, s, Task{Kind: "c", Entity: "c-due", Group: "g", Class: governor.ClassKeepAlive, Due: t0})
	mustSubmit(t, s, Task{Kind: "a", Entity: "a-near", Group: "g", Class: governor.ClassKeepAlive, Due: t0.Add(time.Minute)})
	mustSubmit(t, s, Task{Kind: "c", Entity: "c-near", Group: "g", Class: governor.ClassKeepAlive, Due: t0.Add(time.Minute)})
	mustSubmit(t, s, Task{Kind: "b", Entity: "b-near", Group: "g", Class: governor.ClassKeepAlive, Due: t0.Add(time.Minute)})
	s.Drain(context.Background())

	for kind, ex := range execs {
		for _, b := range ex.batches() {
			for _, it := range b {
				if it.Task.Kind != kind {
					t.Errorf("executor %q was handed %s of kind %q", kind, it.Task.Entity, it.Task.Kind)
				}
			}
		}
	}
	if got := fmt.Sprint(ents(execs["b"].batches()[0])); got != "[b-lead b-2 b-near]" {
		t.Errorf("b batch = %s, want [b-lead b-2 b-near] (the lead, its due peer, then its fill)", got)
	}
	if got := fmt.Sprint(execs["b"].batches()[0][2].Role); got != "fill" {
		t.Errorf("b-near role = %s, want fill", got)
	}
}

func TestFillSkipsOtherKindsRunningPairsAndTasksThatAreDue(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 4)
	leadSeen := make(chan []string, 1)
	slow := &fakeExec{max: 10}
	slow.fn = func(items []Item) []Result {
		switch items[0].Task.Entity {
		case "running":
			entered <- struct{}{}
			<-release
		case "lead":
			leadSeen <- ents(items)
		}
		return make([]Result, len(items))
	}
	other := &fakeExec{max: 10}
	s := New(Options{Clock: governor.NewManualClock(t0), Workers: 3, FillHorizon: time.Hour})
	s.Register("b", slow)
	s.Register("a", other)
	s.Register("c", other)

	mustSubmit(t, s, Task{Kind: "b", Entity: "running", Group: "g", Class: governor.ClassKeepAlive, Due: t0})
	s.Step(context.Background())
	<-entered
	// "running" is queued again, not yet due and inside the horizon: it must not
	// ride in a batch while its first run is still in flight.
	mustSubmit(t, s, Task{Kind: "b", Entity: "running", Group: "g", Class: governor.ClassKeepAlive, Due: t0.Add(time.Minute)})
	mustSubmit(t, s, Task{Kind: "a", Entity: "a-near", Group: "g", Class: governor.ClassKeepAlive, Due: t0.Add(time.Minute)})
	mustSubmit(t, s, Task{Kind: "c", Entity: "c-near", Group: "g", Class: governor.ClassKeepAlive, Due: t0.Add(time.Minute)})
	mustSubmit(t, s, Task{Kind: "b", Entity: "lead", Group: "g", Class: governor.ClassKeepAlive, Due: t0})
	if n := s.Step(context.Background()); n < 1 {
		t.Fatal("the lead's batch did not start")
	}
	select {
	case got := <-leadSeen:
		if fmt.Sprint(got) != "[lead]" {
			t.Errorf("batch = %v, want only the lead: another kind, a running pair or the lead itself rode along", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the lead's batch never reached the executor")
	}
	close(release)
	s.Wait()
}

func TestFillOfEqualDueTimesGoesInArrivalOrder(t *testing.T) {
	e := &fakeExec{max: 20}
	s, _ := newSched(t, Options{Workers: 1, FillHorizon: time.Hour}, e)
	mustSubmit(t, s, task("lead", governor.ClassKeepAlive, t0))
	for i := range 12 {
		mustSubmit(t, s, task(fmt.Sprintf("f%02d", i), governor.ClassKeepAlive, t0.Add(10*time.Second)))
	}
	s.Step(context.Background())
	s.Wait()
	b := e.batches()[0]
	if len(b) != 13 {
		t.Fatalf("batch has %d tasks, want the lead plus 12 fill", len(b))
	}
	for i, it := range b[1:] {
		if want := fmt.Sprintf("f%02d", i); it.Task.Entity != want {
			t.Fatalf("fill[%d] = %s, want %s (equal due times keep arrival order)", i, it.Task.Entity, want)
		}
	}
}

func TestFillPrefersTheEarlierDueWhateverTheArrivalOrder(t *testing.T) {
	e := &fakeExec{max: 3}
	s, _ := newSched(t, Options{Workers: 1, FillHorizon: time.Hour}, e)
	mustSubmit(t, s, task("lead", governor.ClassKeepAlive, t0))
	// The later due arrives first: arrival order must not win over due time.
	mustSubmit(t, s, task("later", governor.ClassKeepAlive, t0.Add(20*time.Second)))
	mustSubmit(t, s, task("sooner", governor.ClassKeepAlive, t0.Add(10*time.Second)))
	mustSubmit(t, s, task("latest", governor.ClassKeepAlive, t0.Add(30*time.Second)))
	s.Step(context.Background())
	s.Wait()
	if got := fmt.Sprint(ents(e.batches()[0])); got != "[lead sooner later]" {
		t.Fatalf("batch = %s, want [lead sooner later]", got)
	}
}

func TestTheGateIsAskedAtTheClassTheLeadCompetesAs(t *testing.T) {
	cases := []struct {
		name string
		task Task
		want governor.Class
	}{
		{"interactive", Task{Class: governor.ClassInteractive}, governor.ClassInteractive},
		{"invalidated", Task{Class: governor.ClassInvalidated}, governor.ClassInvalidated},
		{"query", Task{Class: governor.ClassQuery, Due: t0}, governor.ClassQuery},
		{"keep-alive", Task{Class: governor.ClassKeepAlive, Due: t0}, governor.ClassKeepAlive},
		{"fill that is due competes as keep-alive", Task{Class: governor.ClassFill, Due: t0}, governor.ClassKeepAlive},
		{"keep-alive overdue past the aging window competes as query", Task{Class: governor.ClassKeepAlive, Due: t0.Add(-6 * time.Minute)}, governor.ClassQuery},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var mu sync.Mutex
			var seen []governor.Class
			gate := gateFunc(func(r governor.Request) error {
				mu.Lock()
				seen = append(seen, r.Class)
				mu.Unlock()
				return nil
			})
			e := bucketExec(governor.BucketGraphQL, 1)
			s := New(Options{Clock: governor.NewManualClock(t0), Gate: gate, Workers: 1})
			s.Register("k", e)
			tk := c.task
			tk.Kind, tk.Entity, tk.Group = "k", "e", "g"
			mustSubmit(t, s, tk)
			s.Drain(context.Background())
			mu.Lock()
			defer mu.Unlock()
			if len(seen) != 1 || seen[0] != c.want {
				t.Fatalf("gate asked at classes %v, want exactly [%v]", seen, c.want)
			}
		})
	}
}

func TestAnUnmeteredBatchDoesNotConsultTheGate(t *testing.T) {
	asked := 0
	gate := gateFunc(func(governor.Request) error {
		asked++
		return &governor.Refusal{Kind: governor.RefusedCap, RetryAt: t0.Add(time.Hour)}
	})
	e := &fakeExec{max: 1} // no bucket: the call is not metered
	s := New(Options{Clock: governor.NewManualClock(t0), Gate: gate, Workers: 1})
	s.Register("k", e)
	mustSubmit(t, s, task("a", governor.ClassKeepAlive, t0))
	s.Drain(context.Background())
	if asked != 0 {
		t.Errorf("the gate was asked %d times for a batch with no bucket", asked)
	}
	if len(e.batches()) != 1 {
		t.Fatalf("an unmetered batch ran %d times, want 1", len(e.batches()))
	}
}

func TestARefusedInteractiveLeadRefusesOnlyTheWaitersOfItsBucket(t *testing.T) {
	clk := governor.NewManualClock(t0)
	hold := t0.Add(time.Hour)
	gate := gateFunc(func(r governor.Request) error {
		if r.Bucket == governor.BucketRESTCore && clk.Now().Before(hold) {
			return &governor.Refusal{Kind: governor.RefusedCap, RetryAt: hold}
		}
		return nil
	})
	s := New(Options{Clock: clk, Gate: gate, Workers: 4})
	lead := bucketExec(governor.BucketRESTCore, 1)
	same := bucketExec(governor.BucketRESTCore, 1)
	lower := bucketExec(governor.BucketGraphQL, 1)     // sorts before "rest_core"
	higher := bucketExec(governor.BucketRESTSearch, 1) // sorts after "rest_core"
	s.Register("lead", lead)
	s.Register("same", same)
	s.Register("lower", lower)
	s.Register("higher", higher)

	interactive := func(kind, entity string) *Flight {
		f, err := s.SubmitInteractive(Task{Kind: kind, Entity: entity, Group: "g"})
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	fLead := interactive("lead", "lead")
	fSame := interactive("same", "same")
	fLower := interactive("lower", "lower")
	fHigher := interactive("higher", "higher")
	// A queued class 1 task that nobody waits on (no flight), not yet due, and a
	// bystander of a lower class on the same bucket.
	mustSubmit(t, s, Task{Kind: "same", Entity: "no-waiter", Group: "g", Class: governor.ClassInteractive, Due: t0.Add(2 * time.Hour)})
	mustSubmit(t, s, Task{Kind: "same", Entity: "bystander", Group: "g", Class: governor.ClassQuery, Due: t0.Add(2 * time.Hour)})

	s.Step(context.Background())
	s.Wait()

	for name, f := range map[string]*Flight{"lead": fLead, "same bucket": fSame} {
		if !completed(f) {
			t.Fatalf("%s: a refused waiter's flight must complete at once", name)
		}
		if !errors.Is(f.Result().Err, governor.ErrRefused) {
			t.Errorf("%s: result = %v, want the refusal", name, f.Result().Err)
		}
	}
	for name, f := range map[string]*Flight{"lower bucket": fLower, "higher bucket": fHigher} {
		if !completed(f) {
			t.Fatalf("%s: a waiter of a bucket that was not refused must be served", name)
		}
		if err := f.Result().Err; err != nil {
			t.Errorf("%s: result = %v, want served", name, err)
		}
	}
	if len(lead.batches())+len(same.batches()) != 0 {
		t.Error("nothing of the refused bucket may reach the origin")
	}
	// The refreshes of the refused waiters stay queued as background work.
	for _, e := range []string{"lead", "same", "no-waiter"} {
		c, queued := s.InspectQueued(e, "g")
		if !queued || c != governor.ClassKeepAlive {
			t.Errorf("%s: queued = %v class = %v, want queued as keep-alive", e, queued, c)
		}
	}
	if c, queued := s.InspectQueued("bystander", "g"); !queued || c != governor.ClassQuery {
		t.Errorf("a class 3 task of the bucket was changed: queued = %v class = %v", queued, c)
	}
	// The downgraded task is due now, so it goes once the hold ends, ahead of its
	// original two-hour due.
	clk.Set(hold)
	s.Drain(context.Background())
	var sameRan []string
	for _, b := range same.batches() {
		sameRan = append(sameRan, ents(b)...)
	}
	if !strings.Contains(fmt.Sprint(sameRan), "no-waiter") {
		t.Errorf("the downgraded task (due reset to now) did not run after the hold: %v", sameRan)
	}
}

func TestARefusedInvalidatedLeadHoldsBothGroupsAndLeavesClass1Alone(t *testing.T) {
	clk := governor.NewManualClock(t0)
	gate := gateFunc(func(r governor.Request) error {
		return &governor.Refusal{Kind: governor.RefusedCap, RetryAt: t0.Add(time.Hour)}
	})
	e := bucketExec(governor.BucketGraphQL, 1)
	s := New(Options{Clock: clk, Gate: gate, Workers: 1})
	s.Register("k", e)
	// A class 1 task that is queued but not due, on the same bucket.
	mustSubmit(t, s, Task{Kind: "k", Entity: "waiting", Group: "g", Class: governor.ClassInteractive, Due: t0.Add(2 * time.Hour)})
	mustSubmit(t, s, task("lead", governor.ClassInvalidated, t0))
	if n := s.Step(context.Background()); n != 0 {
		t.Fatalf("Step started %d batches, want the refusal to stop it", n)
	}
	if c, q := s.InspectQueued("waiting", "g"); !q || c != governor.ClassInteractive {
		t.Errorf("a class 1 task was changed by an invalidated lead's refusal: queued = %v class = %v", q, c)
	}
	if c, q := s.InspectQueued("lead", "g"); !q || c != governor.ClassInvalidated {
		t.Errorf("the refused invalidated task must stay queued as class 2: queued = %v class = %v", q, c)
	}
	want := t0.Add(time.Hour)
	if got := s.holdFG[governor.BucketGraphQL]; !got.Equal(want) {
		t.Errorf("foreground hold = %v, want %v", got, want)
	}
	if got := s.holdBG[governor.BucketGraphQL]; !got.Equal(want) {
		t.Errorf("background hold = %v, want %v", got, want)
	}
}

func TestARefusedBackgroundLeadHoldsTheBackgroundGroupOnly(t *testing.T) {
	gate := gateFunc(func(r governor.Request) error {
		return &governor.Refusal{Kind: governor.RefusedCap, RetryAt: t0.Add(time.Hour)}
	})
	e := bucketExec(governor.BucketGraphQL, 1)
	s := New(Options{Clock: governor.NewManualClock(t0), Gate: gate, Workers: 1})
	s.Register("k", e)
	mustSubmit(t, s, task("lead", governor.ClassKeepAlive, t0))
	s.Step(context.Background())
	if _, ok := s.holdFG[governor.BucketGraphQL]; ok {
		t.Error("a background refusal set a foreground hold")
	}
	if got := s.holdBG[governor.BucketGraphQL]; !got.Equal(t0.Add(time.Hour)) {
		t.Errorf("background hold = %v, want %v", got, t0.Add(time.Hour))
	}
}

func TestTheRetryTimeOfARefusalSetsTheLengthOfTheHold(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		holdFor time.Duration
	}{
		{"a refusal names when to retry", &governor.Refusal{Kind: governor.RefusedCap, RetryAt: t0.Add(time.Hour)}, time.Hour},
		{"a refusal that names a time already past falls back to the retry delay", &governor.Refusal{Kind: governor.RefusedCap, RetryAt: t0.Add(-time.Minute)}, 30 * time.Second},
		{"a refusal without a retry time falls back to the retry delay", &governor.Refusal{Kind: governor.RefusedCap}, 30 * time.Second},
		{"any other error falls back to the retry delay", errors.New("gate down"), 30 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clk := governor.NewManualClock(t0)
			refuse := true
			gate := gateFunc(func(governor.Request) error {
				if refuse {
					return c.err
				}
				return nil
			})
			e := bucketExec(governor.BucketGraphQL, 1)
			s := New(Options{Clock: clk, Gate: gate, Workers: 1, RetryDelay: 30 * time.Second})
			s.Register("k", e)
			mustSubmit(t, s, task("a", governor.ClassKeepAlive, t0))
			s.Drain(context.Background())
			refuse = false
			clk.Set(t0.Add(c.holdFor - time.Second))
			s.Drain(context.Background())
			if len(e.batches()) != 0 {
				t.Fatalf("ran a second before the hold of %v ended", c.holdFor)
			}
			clk.Set(t0.Add(c.holdFor))
			s.Drain(context.Background())
			if len(e.batches()) != 1 {
				t.Fatalf("did not run when the hold of %v ended", c.holdFor)
			}
		})
	}
}

func TestAnExecutorThatReturnsTheWrongNumberOfResultsFailsTheBatch(t *testing.T) {
	for _, n := range []int{0, 1, 3} {
		t.Run(fmt.Sprintf("%d results for 2 items", n), func(t *testing.T) {
			e := &fakeExec{max: 2}
			returned := n
			e.fn = func(items []Item) []Result {
				if returned < 0 {
					return make([]Result, len(items))
				}
				return make([]Result, returned)
			}
			s, clk := newSched(t, Options{Workers: 1, RetryDelay: time.Minute}, e)
			f1, _ := s.SubmitInteractive(Task{Kind: "k", Entity: "one", Group: "g"})
			f2, _ := s.SubmitInteractive(Task{Kind: "k", Entity: "two", Group: "g"})
			s.Drain(context.Background())
			want := fmt.Sprintf("executor returned %d results for 2 items", n)
			for name, f := range map[string]*Flight{"one": f1, "two": f2} {
				if !completed(f) {
					t.Fatalf("%s: flight did not complete", name)
				}
				if err := f.Result().Err; err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("%s: result = %v, want an error containing %q", name, err, want)
				}
			}
			// Both tasks are retried, as background work, after the retry delay.
			for _, name := range []string{"one", "two"} {
				if c, q := s.InspectQueued(name, "g"); !q || c != governor.ClassKeepAlive {
					t.Errorf("%s: queued = %v class = %v, want requeued as keep-alive", name, q, c)
				}
			}
			returned = -1
			clk.Advance(59 * time.Second)
			s.Drain(context.Background())
			if got := len(e.batches()); got != 1 {
				t.Fatalf("retried before the retry delay (%d batches)", got)
			}
			clk.Advance(2 * time.Second)
			s.Drain(context.Background())
			if got := len(e.batches()); got != 2 {
				t.Fatalf("batches after the retry delay = %d, want 2", got)
			}
		})
	}
}

func TestAnExecutorThatPanicsFailsEveryTaskOfTheBatch(t *testing.T) {
	e := &fakeExec{max: 2}
	e.fn = func(items []Item) []Result { panic("kaboom") }
	s, _ := newSched(t, Options{Workers: 1}, e)
	f1, _ := s.SubmitInteractive(Task{Kind: "k", Entity: "one", Group: "g"})
	f2, _ := s.SubmitInteractive(Task{Kind: "k", Entity: "two", Group: "g"})
	s.Step(context.Background())
	s.Wait()
	for name, f := range map[string]*Flight{"one": f1, "two": f2} {
		if err := f.Result().Err; err == nil || !strings.Contains(err.Error(), "executor panicked: kaboom") {
			t.Errorf("%s: result = %v, want the panic reported as an error", name, err)
		}
	}
}

func TestHistoryKeepsTheNewestFinishedBatchesAndNeverDropsARunningOne(t *testing.T) {
	runOne := func(t *testing.T, s *Scheduler, entity string) {
		t.Helper()
		f, err := s.SubmitInteractive(Task{Kind: "k", Entity: entity, Group: "g"})
		if err != nil {
			t.Fatal(err)
		}
		s.Step(context.Background())
		<-f.Done()
		s.Wait()
	}
	batchEntities := func(s *Scheduler) []string {
		var out []string
		for _, b := range s.State().Batches {
			out = append(out, fmt.Sprint(b.IDs(RoleDue)))
		}
		return out
	}

	t.Run("one batch of history", func(t *testing.T) {
		s, _ := newSched(t, Options{Workers: 1, History: 1}, &fakeExec{max: 1})
		for _, e := range []string{"e1", "e2", "e3"} {
			runOne(t, s, e)
		}
		if got := fmt.Sprint(batchEntities(s)); got != "[[e3]]" {
			t.Fatalf("history = %s, want only the newest batch", got)
		}
	})
	t.Run("two batches of history", func(t *testing.T) {
		s, _ := newSched(t, Options{Workers: 1, History: 2}, &fakeExec{max: 1})
		for _, e := range []string{"e1", "e2", "e3", "e4", "e5"} {
			runOne(t, s, e)
		}
		if got := fmt.Sprint(batchEntities(s)); got != "[[e4] [e5]]" {
			t.Fatalf("history = %s, want the two newest batches, oldest first", got)
		}
	})
	t.Run("a running batch is never dropped", func(t *testing.T) {
		release := make(chan struct{})
		entered := make(chan struct{})
		e := &fakeExec{max: 1}
		e.fn = func(items []Item) []Result {
			if items[0].Task.Entity == "slow" {
				close(entered)
				<-release
			}
			return make([]Result, len(items))
		}
		s, _ := newSched(t, Options{Workers: 2, History: 1}, e)
		mustSubmit(t, s, task("slow", governor.ClassKeepAlive, t0))
		s.Step(context.Background())
		<-entered
		for _, name := range []string{"f1", "f2"} {
			f, _ := s.SubmitInteractive(Task{Kind: "k", Entity: name, Group: "g"})
			s.Step(context.Background())
			<-f.Done()
		}
		st := s.State()
		if len(st.Batches) != 1 || !st.Batches[0].Running || fmt.Sprint(st.Batches[0].IDs(RoleDue)) != "[slow]" {
			t.Fatalf("history = %+v, want only the running batch while finished ones are pruned", st.Batches)
		}
		close(release)
		s.Wait()
		if st := s.State(); len(st.Batches) != 1 || st.Batches[0].Running {
			t.Fatalf("after the run, history = %+v, want one finished batch", st.Batches)
		}
	})
}

func TestARequeuedTaskGoesBehindWhatWasAlreadyWaiting(t *testing.T) {
	e := &fakeExec{max: 1}
	failed := false
	e.fn = func(items []Item) []Result {
		if items[0].Task.Entity == "a" && !failed {
			failed = true
			return []Result{{Err: errors.New("boom")}}
		}
		return make([]Result, len(items))
	}
	s, clk := newSched(t, Options{Workers: 1, RetryDelay: 30 * time.Second}, e)
	mustSubmit(t, s, task("a", governor.ClassKeepAlive, t0))
	// "c" comes due at the very instant "a"'s retry does.
	mustSubmit(t, s, task("c", governor.ClassKeepAlive, t0.Add(30*time.Second)))
	s.Drain(context.Background())
	clk.Advance(30 * time.Second)
	s.Drain(context.Background())
	var order []string
	for _, b := range e.batches() {
		order = append(order, ents(b)...)
	}
	if got := fmt.Sprint(order); got != "[a c a]" {
		t.Fatalf("order = %s, want [a c a]: the requeued task queues behind the one already waiting", got)
	}
}

func TestRequeueDowngradesAWaitingCallerAndKeepsOtherClasses(t *testing.T) {
	e := &fakeExec{max: 1}
	e.fn = func(items []Item) []Result { return []Result{{Err: errors.New("boom")}} }
	s, _ := newSched(t, Options{Workers: 1, RetryDelay: time.Hour}, e)
	f, _ := s.SubmitInteractive(Task{Kind: "k", Entity: "waiting", Group: "g"})
	mustSubmit(t, s, task("query", governor.ClassQuery, t0))
	mustSubmit(t, s, task("invalidated", governor.ClassInvalidated, t0))
	s.Drain(context.Background())
	if !completed(f) || f.Result().Err == nil {
		t.Fatal("the waiting caller must be completed with the failure")
	}
	for name, want := range map[string]governor.Class{
		"waiting": governor.ClassKeepAlive, "query": governor.ClassQuery, "invalidated": governor.ClassInvalidated,
	} {
		if c, q := s.InspectQueued(name, "g"); !q || c != want {
			t.Errorf("%s: queued = %v class = %v, want requeued as %v", name, q, c, want)
		}
	}
}

func TestRequeueYieldsToATaskQueuedWhileItRan(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	e := &fakeExec{max: 1}
	e.fn = func(items []Item) []Result {
		close(entered)
		<-release
		return []Result{{Err: errors.New("boom")}}
	}
	s, _ := newSched(t, Options{Workers: 1}, e)
	mustSubmit(t, s, task("a", governor.ClassKeepAlive, t0))
	s.Step(context.Background())
	<-entered
	mustSubmit(t, s, task("a", governor.ClassInvalidated, t0))
	close(release)
	s.Wait()
	if c, q := s.InspectQueued("a", "g"); !q || c != governor.ClassInvalidated {
		t.Fatalf("queued = %v class = %v, want the newer class 2 task to win over the requeue", q, c)
	}
}

func TestAnExecutorRefusalHoldsTheClassGroupsOfItsBucket(t *testing.T) {
	retry := t0.Add(time.Hour)
	cases := []struct {
		class  governor.Class
		wantFG bool
	}{
		{governor.ClassInteractive, true},
		{governor.ClassInvalidated, true},
		{governor.ClassQuery, false},
		{governor.ClassKeepAlive, false},
		{governor.ClassFill, false},
	}
	for _, c := range cases {
		t.Run(c.class.String(), func(t *testing.T) {
			e := bucketExec(governor.BucketGraphQL, 1)
			e.fn = func(items []Item) []Result {
				return []Result{{Err: &governor.Refusal{Kind: governor.RefusedCap, RetryAt: retry}}}
			}
			s := New(Options{Clock: governor.NewManualClock(t0), Workers: 1})
			s.Register("k", e)
			mustSubmit(t, s, task("a", c.class, t0))
			s.Step(context.Background())
			s.Wait()
			if got := s.holdBG[governor.BucketGraphQL]; !got.Equal(retry) {
				t.Errorf("background hold = %v, want %v", got, retry)
			}
			got, ok := s.holdFG[governor.BucketGraphQL]
			if ok != c.wantFG || (ok && !got.Equal(retry)) {
				t.Errorf("foreground hold = %v (set %v), want set = %v at %v", got, ok, c.wantFG, retry)
			}
		})
	}
	t.Run("an unmetered executor sets no hold", func(t *testing.T) {
		e := &fakeExec{max: 1}
		e.fn = func(items []Item) []Result {
			return []Result{{Err: &governor.Refusal{Kind: governor.RefusedCap, RetryAt: retry}}}
		}
		s := New(Options{Clock: governor.NewManualClock(t0), Workers: 1})
		s.Register("k", e)
		mustSubmit(t, s, task("a", governor.ClassKeepAlive, t0))
		s.Step(context.Background())
		s.Wait()
		if len(s.holdBG) != 0 || len(s.holdFG) != 0 {
			t.Errorf("holds = %v %v, want none for a batch with no bucket", s.holdBG, s.holdFG)
		}
		if !s.Queued("a", "g") {
			t.Error("the refused task must still be requeued")
		}
	})
}

func TestNextWakeIsTheNearestOfTheNextDueTheHoldsAndThePollInterval(t *testing.T) {
	clk := governor.NewManualClock(t0)
	newS := func() *Scheduler { return New(Options{Clock: clk, PollInterval: time.Second}) }
	const poll = time.Second

	if got := newS().nextWake(t0); got != poll {
		t.Errorf("empty scheduler wakes in %v, want the poll interval", got)
	}

	s := newS()
	mustSubmit(t, s, Task{Kind: "k", Entity: "zero", Group: "g", Class: governor.ClassKeepAlive})
	mustSubmit(t, s, Task{Kind: "k", Entity: "past", Group: "g", Class: governor.ClassKeepAlive, Due: t0.Add(-time.Minute)})
	mustSubmit(t, s, Task{Kind: "k", Entity: "now", Group: "g", Class: governor.ClassKeepAlive, Due: t0})
	if got := s.nextWake(t0); got != poll {
		t.Errorf("tasks already due wake in %v, want the poll interval (they are dispatched, not waited for)", got)
	}

	mustSubmit(t, s, Task{Kind: "k", Entity: "soon", Group: "g", Class: governor.ClassKeepAlive, Due: t0.Add(300 * time.Millisecond)})
	if got := s.nextWake(t0); got != 300*time.Millisecond {
		t.Errorf("wakes in %v, want 300ms (the next due)", got)
	}
	mustSubmit(t, s, Task{Kind: "k", Entity: "far", Group: "g", Class: governor.ClassKeepAlive, Due: t0.Add(time.Hour)})
	if got := s.nextWake(t0); got != 300*time.Millisecond {
		t.Errorf("a later due moved the wake to %v", got)
	}

	s = newS()
	s.holdBG[governor.BucketGraphQL] = t0.Add(-time.Minute)
	s.holdFG[governor.BucketGraphQL] = t0
	if got := s.nextWake(t0); got != poll {
		t.Errorf("holds that have ended wake in %v, want the poll interval", got)
	}
	s.holdBG[governor.BucketRESTCore] = t0.Add(200 * time.Millisecond)
	if got := s.nextWake(t0); got != 200*time.Millisecond {
		t.Errorf("a background hold ending in 200ms wakes in %v", got)
	}
	s.holdFG[governor.BucketRESTSearch] = t0.Add(100 * time.Millisecond)
	if got := s.nextWake(t0); got != 100*time.Millisecond {
		t.Errorf("a foreground hold ending in 100ms wakes in %v", got)
	}
	s = newS()
	s.holdFG[governor.BucketRESTSearch] = t0.Add(150 * time.Millisecond)
	if got := s.nextWake(t0); got != 150*time.Millisecond {
		t.Errorf("a foreground hold alone wakes in %v, want 150ms", got)
	}
	s = newS()
	mustSubmit(t, s, Task{Kind: "k", Entity: "tiny", Group: "g", Class: governor.ClassKeepAlive, Due: t0.Add(time.Microsecond)})
	if got := s.nextWake(t0); got != time.Millisecond {
		t.Errorf("wakes in %v, want the 1ms floor", got)
	}
}
