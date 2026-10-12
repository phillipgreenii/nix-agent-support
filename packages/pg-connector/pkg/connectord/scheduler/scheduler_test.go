package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

var t0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

// fakeExec records every batch it is handed.
type fakeExec struct {
	mu     sync.Mutex
	max    int
	bucket governor.Bucket
	calls  [][]Item
	fn     func(items []Item) []Result
}

func (f *fakeExec) MaxBatch() int { return f.max }

func (f *fakeExec) Spend(class governor.Class, n int) governor.Request {
	return governor.Request{Bucket: f.bucket, Class: class, Cost: 1}
}

func (f *fakeExec) Execute(_ context.Context, items []Item) []Result {
	f.mu.Lock()
	f.calls = append(f.calls, append([]Item(nil), items...))
	fn := f.fn
	f.mu.Unlock()
	if fn != nil {
		return fn(items)
	}
	return make([]Result, len(items))
}

func (f *fakeExec) batches() [][]Item {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]Item(nil), f.calls...)
}

func newSched(t *testing.T, opt Options, e *fakeExec) (*Scheduler, *governor.ManualClock) {
	t.Helper()
	clk := governor.NewManualClock(t0)
	opt.Clock = clk
	s := New(opt)
	s.Register("k", e)
	return s, clk
}

func task(entity string, class governor.Class, due time.Time) Task {
	return Task{Kind: "k", Entity: entity, Group: "g", Class: class, Due: due}
}

func mustSubmit(t *testing.T, s *Scheduler, tk Task) {
	t.Helper()
	if err := s.Submit(tk); err != nil {
		t.Fatal(err)
	}
}

func ents(items []Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Task.Entity
	}
	return out
}

func TestPriorityOrderAcrossTheFiveClasses(t *testing.T) {
	e := &fakeExec{max: 1}
	s, clk := newSched(t, Options{Workers: 1}, e)
	past := t0.Add(-time.Minute)
	clk.Set(t0)

	mustSubmit(t, s, task("fill-due", governor.ClassFill, past))
	// Class 4 is most recently accessed first, whatever the due times.
	a := task("keep-old", governor.ClassKeepAlive, past) // overdue, but not yet aged
	a.Access = t0.Add(-2 * time.Hour)
	b := task("keep-new", governor.ClassKeepAlive, past)
	b.Access = t0.Add(-time.Minute)
	mustSubmit(t, s, a)
	mustSubmit(t, s, b)
	mustSubmit(t, s, task("query-late", governor.ClassQuery, past))
	mustSubmit(t, s, task("query-early", governor.ClassQuery, past.Add(-time.Second)))
	mustSubmit(t, s, task("invalidated", governor.ClassInvalidated, time.Time{}))
	if _, err := s.SubmitInteractive(Task{Kind: "k", Entity: "interactive", Group: "g"}); err != nil {
		t.Fatal(err)
	}
	// A task that is not due yet is never a lead.
	mustSubmit(t, s, task("later", governor.ClassInteractive, t0.Add(time.Hour)))

	s.Drain(context.Background())
	var got []string
	for _, bt := range e.batches() {
		got = append(got, ents(bt)...)
	}
	// "later" is class 1 but due in an hour: it waits. The fill task that is due
	// ranks as class 4 behind the accessed keep-alive tasks (Access is zero).
	want := []string{"interactive", "invalidated", "query-early", "query-late", "keep-new", "keep-old", "fill-due"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if !s.Queued("later", "g") {
		t.Error("a task not yet due must stay queued")
	}
}

func TestFillNeverDisplacesDueAndNeverExceedsTheBatchSize(t *testing.T) {
	e := &fakeExec{max: 74}
	s, _ := newSched(t, Options{Workers: 1, FillHorizon: time.Minute}, e)
	for i := range 80 { // 80 due tasks
		mustSubmit(t, s, task(fmt.Sprintf("due-%03d", i), governor.ClassKeepAlive, t0.Add(-time.Second)))
	}
	for i := range 40 { // 40 due within the horizon
		mustSubmit(t, s, task(fmt.Sprintf("near-%03d", i), governor.ClassKeepAlive, t0.Add(time.Duration(i+1)*time.Second)))
	}
	for i := range 10 { // outside the horizon
		mustSubmit(t, s, task(fmt.Sprintf("far-%03d", i), governor.ClassKeepAlive, t0.Add(time.Hour)))
	}
	s.Drain(context.Background())
	bs := e.batches()
	if len(bs) != 2 {
		t.Fatalf("batches = %d, want 2", len(bs))
	}
	if len(bs[0]) != 74 {
		t.Errorf("first batch has %d ids, want 74", len(bs[0]))
	}
	for _, it := range bs[0] {
		if it.Role != RoleDue {
			t.Errorf("fill %s displaced a due task: first batch must be all due", it.Task.Entity)
		}
	}
	var due, fill int
	for _, it := range bs[1] {
		if it.Role == RoleDue {
			due++
		} else {
			fill++
			if it.Task.Entity[:4] != "near" {
				t.Errorf("fill %s is outside the horizon", it.Task.Entity)
			}
		}
	}
	if due != 6 || fill != 40 || len(bs[1]) != 46 {
		t.Errorf("second batch: due=%d fill=%d total=%d, want 6, 40, 46", due, fill, len(bs[1]))
	}
	if !s.Queued("far-000", "g") {
		t.Error("a task outside the horizon must not ride")
	}
}

func TestFillIsNearestFirstAndCapped(t *testing.T) {
	e := &fakeExec{max: 74}
	s, _ := newSched(t, Options{Workers: 1, FillHorizon: time.Hour}, e)
	for i := range 3 {
		mustSubmit(t, s, task(fmt.Sprintf("due-%d", i), governor.ClassKeepAlive, t0))
	}
	for i := range 100 { // nearest is n-000
		mustSubmit(t, s, task(fmt.Sprintf("n-%03d", i), governor.ClassKeepAlive, t0.Add(time.Duration(i+1)*time.Second)))
	}
	s.Step(context.Background())
	s.Wait()
	b := e.batches()[0]
	if len(b) != 74 {
		t.Fatalf("batch has %d ids, want 74 (3 due + 71 fill)", len(b))
	}
	for i, it := range b[3:] {
		if want := fmt.Sprintf("n-%03d", i); it.Task.Entity != want || it.Role != RoleFill {
			t.Fatalf("fill[%d] = %s/%s, want %s/fill", i, it.Task.Entity, it.Role, want)
		}
	}
}

func TestInteractiveGoesOutAtOnceWithSpareSlotsFilled(t *testing.T) {
	e := &fakeExec{max: 74}
	s, _ := newSched(t, Options{Workers: 1, FillHorizon: time.Minute}, e)
	for i := range 5 {
		mustSubmit(t, s, task(fmt.Sprintf("near-%d", i), governor.ClassKeepAlive, t0.Add(time.Duration(i+1)*time.Second)))
	}
	f, err := s.SubmitInteractive(Task{Kind: "k", Entity: "wanted", Group: "g"})
	if err != nil {
		t.Fatal(err)
	}
	if n := s.Step(context.Background()); n != 1 {
		t.Fatalf("Step started %d batches, want 1 (no waiting for the batch to fill)", n)
	}
	s.Wait()
	<-f.Done()
	b := e.batches()[0]
	if len(b) != 6 || b[0].Task.Entity != "wanted" || b[0].Role != RoleDue {
		t.Fatalf("batch = %v, want the interactive task first plus 5 fill", ents(b))
	}
	for _, it := range b[1:] {
		if it.Role != RoleFill {
			t.Errorf("%s role %s, want fill", it.Task.Entity, it.Role)
		}
	}
	if f.Started().IsZero() {
		t.Error("the flight must record when it started")
	}
}

func TestSingleFlightSharesOneFetch(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	e := &fakeExec{max: 1}
	e.fn = func(items []Item) []Result {
		close(entered)
		<-release
		return make([]Result, len(items))
	}
	s, _ := newSched(t, Options{Workers: 2}, e)
	f1, _ := s.SubmitInteractive(Task{Kind: "k", Entity: "e", Group: "g"})
	f1b, _ := s.SubmitInteractive(Task{Kind: "k", Entity: "e", Group: "g"}) // queued: same flight
	if f1 != f1b {
		t.Fatal("two interactive requests for one (entity, group) must share a flight while queued")
	}
	s.Step(context.Background())
	<-entered
	f2, _ := s.SubmitInteractive(Task{Kind: "k", Entity: "e", Group: "g"}) // running: join it
	if f2 != f1 {
		t.Fatal("a request while the fetch runs must join the running flight")
	}
	if n := s.Step(context.Background()); n != 0 {
		t.Fatalf("Step started %d batches for a key already running", n)
	}
	close(release)
	s.Wait()
	<-f1.Done()
	if got := len(e.batches()); got != 1 {
		t.Fatalf("executor ran %d times, want 1", got)
	}
}

func TestAgingLetsClass4RunWhileClass3StaysBusy(t *testing.T) {
	e := &fakeExec{max: 1}
	s, clk := newSched(t, Options{Workers: 1, AgeAfter: 5 * time.Minute}, e)
	mustSubmit(t, s, task("starved", governor.ClassKeepAlive, t0))
	ranAt := func() int {
		for i, b := range e.batches() {
			if b[0].Task.Entity == "starved" {
				return i
			}
		}
		return -1
	}
	// Every round a fresh class 3 task is due, so class 3 never runs dry.
	for round := range 20 {
		mustSubmit(t, s, task(fmt.Sprintf("q-%02d", round), governor.ClassQuery, clk.Now()))
		s.Step(context.Background())
		s.Wait()
		if ranAt() >= 0 {
			break
		}
		clk.Advance(time.Minute)
	}
	if got := ranAt(); got < 0 {
		t.Fatal("a class 4 task never ran while class 3 stayed busy")
	}
	if waited := clk.Now().Sub(t0); waited < 5*time.Minute {
		t.Errorf("class 4 ran after %v, before it aged (5m): class 3 must go first until then", waited)
	}
}

func TestGovernorRefusalHoldsBackgroundUntilTheWindowMoves(t *testing.T) {
	clk := governor.NewManualClock(t0)
	gov, err := governor.New(governor.Config{
		GraphQL: governor.Limits{Cap: 3}, RESTCore: governor.Limits{Cap: 100}, RESTSearch: governor.Limits{Cap: 100},
		BackgroundShare: 1,
	}, governor.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	e := &fakeExec{max: 1, bucket: governor.BucketGraphQL}
	e.fn = func(items []Item) []Result {
		_ = gov.Record(governor.Spend{Bucket: governor.BucketGraphQL, Class: items[0].Task.Class, Cost: 1})
		return make([]Result, len(items))
	}
	s := New(Options{Clock: clk, Gate: gov, Workers: 1})
	s.Register("k", e)
	for i := range 8 {
		mustSubmit(t, s, task(fmt.Sprintf("t-%d", i), governor.ClassKeepAlive, t0))
	}
	s.Drain(context.Background())
	if got := len(e.batches()); got != 3 {
		t.Fatalf("ran %d batches against a cap of 3", got)
	}
	// A refusal does not spin: nothing more starts until the hold ends.
	if n := s.Step(context.Background()); n != 0 {
		t.Fatalf("Step started %d batches while the cap is spent", n)
	}
	clk.Advance(time.Hour + time.Second)
	s.Drain(context.Background())
	if got := len(e.batches()); got != 6 {
		t.Fatalf("ran %d batches after the window moved, want 6", got)
	}
}

func TestInteractiveRefusedIsCompletedWithTheRefusal(t *testing.T) {
	clk := governor.NewManualClock(t0)
	gov, err := governor.New(governor.Config{
		GraphQL: governor.Limits{Cap: 1}, RESTCore: governor.Limits{Cap: 100}, RESTSearch: governor.Limits{Cap: 100},
		BackgroundShare: 1,
	}, governor.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	if err := gov.Record(governor.Spend{Bucket: governor.BucketGraphQL, Class: governor.ClassQuery, Cost: 1}); err != nil {
		t.Fatal(err)
	}
	e := &fakeExec{max: 1, bucket: governor.BucketGraphQL}
	s := New(Options{Clock: clk, Gate: gov, Workers: 1})
	s.Register("k", e)
	f, _ := s.SubmitInteractive(Task{Kind: "k", Entity: "e", Group: "g"})
	s.Step(context.Background())
	select {
	case <-f.Done():
	default:
		t.Fatal("a refused interactive task must complete at once so its caller can serve stale")
	}
	var r *governor.Refusal
	if !errors.As(f.Result().Err, &r) || r.Kind != governor.RefusedCap {
		t.Fatalf("result = %v, want a cap refusal", f.Result().Err)
	}
	if len(e.batches()) != 0 {
		t.Error("nothing may spend past the cap")
	}
	if !s.Queued("e", "g") {
		t.Error("the refresh itself must stay queued as background work")
	}
}

func TestExecutorRefusalHoldsAndRequeues(t *testing.T) {
	clk := governor.NewManualClock(t0)
	refuse := true
	e := &fakeExec{max: 1, bucket: governor.BucketGraphQL}
	e.fn = func(items []Item) []Result {
		if refuse {
			return []Result{{Err: &governor.Refusal{Kind: governor.RefusedCap, RetryAt: clk.Now().Add(time.Hour)}}}
		}
		return make([]Result, len(items))
	}
	s := New(Options{Clock: clk, Workers: 1})
	s.Register("k", e)
	mustSubmit(t, s, task("a", governor.ClassKeepAlive, t0))
	mustSubmit(t, s, task("b", governor.ClassKeepAlive, t0))
	s.Drain(context.Background())
	if got := len(e.batches()); got != 1 {
		t.Fatalf("ran %d batches, want 1: the refusal holds the class group", got)
	}
	refuse = false
	clk.Advance(time.Hour + time.Second)
	s.Drain(context.Background())
	if got := len(e.batches()); got != 3 {
		t.Fatalf("ran %d batches after the hold, want 3 (the refused one retried, then the other)", got)
	}
}

func TestFailureRequeuesAfterRetryDelayAndPanicIsAFailure(t *testing.T) {
	e := &fakeExec{max: 1}
	calls := 0
	e.fn = func(items []Item) []Result {
		calls++
		switch calls {
		case 1:
			return []Result{{Err: errors.New("boom")}}
		case 2:
			panic("oops")
		}
		return make([]Result, len(items))
	}
	s, clk := newSched(t, Options{Workers: 1, RetryDelay: 30 * time.Second}, e)
	mustSubmit(t, s, task("a", governor.ClassKeepAlive, t0))
	s.Drain(context.Background())
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (the retry waits)", calls)
	}
	clk.Advance(29 * time.Second)
	s.Drain(context.Background())
	if calls != 1 {
		t.Fatalf("retried before RetryDelay (calls = %d)", calls)
	}
	clk.Advance(2 * time.Second)
	s.Drain(context.Background()) // panics: a failure, not a crash
	clk.Advance(time.Minute)
	s.Drain(context.Background())
	if calls != 3 || s.Queued("a", "g") {
		t.Fatalf("calls = %d queued = %v, want 3 calls and the task done", calls, s.Queued("a", "g"))
	}
}

func TestSkippedIsDropped(t *testing.T) {
	e := &fakeExec{max: 1}
	e.fn = func(items []Item) []Result { return []Result{{Skipped: true}} }
	s, clk := newSched(t, Options{Workers: 1}, e)
	mustSubmit(t, s, task("a", governor.ClassKeepAlive, t0))
	s.Drain(context.Background())
	clk.Advance(time.Hour)
	s.Drain(context.Background())
	if len(e.batches()) != 1 || s.Queued("a", "g") {
		t.Fatalf("a skipped task must be dropped, not retried (ran %d)", len(e.batches()))
	}
}

func TestSubmitMergesAndRescheduleReplaces(t *testing.T) {
	e := &fakeExec{max: 1}
	s, _ := newSched(t, Options{Workers: 1}, e)
	mustSubmit(t, s, task("a", governor.ClassKeepAlive, t0.Add(time.Hour)))
	mustSubmit(t, s, task("a", governor.ClassQuery, t0.Add(2*time.Hour))) // better class, later due
	st := s.State()
	if st.Classes[2].Depth != 1 || st.Classes[3].Depth != 0 {
		t.Fatalf("merge must keep the better class: depths = %v", st.Classes)
	}
	if n, ok := s.Position("a", "g"); !ok || n != 1 {
		t.Fatalf("Position = %d %v", n, ok)
	}
	s.Drain(context.Background())
	if len(e.batches()) != 0 {
		t.Fatal("merge must keep the earlier due (an hour away): not due yet")
	}
	if err := s.Reschedule(task("a", governor.ClassKeepAlive, t0)); err != nil {
		t.Fatal(err)
	}
	s.Drain(context.Background())
	if len(e.batches()) != 1 {
		t.Fatal("Reschedule must replace the due time")
	}
}

func TestPositionRanksByDueWithinClass(t *testing.T) {
	e := &fakeExec{max: 1}
	s, _ := newSched(t, Options{}, e)
	mustSubmit(t, s, task("late", governor.ClassKeepAlive, t0.Add(3*time.Hour)))
	mustSubmit(t, s, task("early", governor.ClassKeepAlive, t0.Add(time.Hour)))
	mustSubmit(t, s, task("mid", governor.ClassKeepAlive, t0.Add(2*time.Hour)))
	mustSubmit(t, s, task("other-class", governor.ClassQuery, t0.Add(4*time.Hour)))
	for name, want := range map[string]int{"early": 1, "mid": 2, "late": 3, "other-class": 1} {
		if got, ok := s.Position(name, "g"); !ok || got != want {
			t.Errorf("Position(%s) = %d %v, want %d", name, got, ok, want)
		}
	}
	if _, ok := s.Position("nobody", "g"); ok {
		t.Error("Position of an unqueued task must say none")
	}
}

func TestStateReportsDepthOverdueAndBatches(t *testing.T) {
	var gate sync.WaitGroup
	gate.Add(1)
	e := &fakeExec{max: 4}
	e.fn = func(items []Item) []Result { gate.Wait(); return make([]Result, len(items)) }
	s, _ := newSched(t, Options{Workers: 1, FillHorizon: time.Hour}, e)
	mustSubmit(t, s, task("a", governor.ClassKeepAlive, t0.Add(-90*time.Second)))
	mustSubmit(t, s, task("b", governor.ClassKeepAlive, t0.Add(-30*time.Second)))
	mustSubmit(t, s, task("c", governor.ClassKeepAlive, t0.Add(time.Minute)))
	st := s.State()
	if cs := st.Classes[3]; cs.Class != governor.ClassKeepAlive || cs.Depth != 3 || cs.OldestOverdue != 90*time.Second {
		t.Fatalf("class 4 state = %+v", cs)
	}
	s.Step(context.Background())
	st = s.State()
	if st.Running != 1 || len(st.Batches) != 1 || !st.Batches[0].Running {
		t.Fatalf("running batch not reported: %+v", st)
	}
	if got := st.Batches[0].IDs(RoleDue); fmt.Sprint(got) != "[a b]" {
		t.Errorf("due ids = %v", got)
	}
	if got := st.Batches[0].IDs(RoleFill); fmt.Sprint(got) != "[c]" {
		t.Errorf("fill ids = %v", got)
	}
	gate.Done()
	s.Wait()
	if st = s.State(); st.Running != 0 || st.Batches[0].Running {
		t.Fatalf("finished batch still reported running: %+v", st)
	}
}

func TestCancelCompletesTheFlight(t *testing.T) {
	e := &fakeExec{max: 1}
	s, _ := newSched(t, Options{}, e)
	f, _ := s.SubmitInteractive(Task{Kind: "k", Entity: "e", Group: "g"})
	s.CancelEntity("e")
	<-f.Done()
	if !errors.Is(f.Result().Err, ErrCanceled) || s.Queued("e", "g") {
		t.Fatalf("result = %v queued = %v", f.Result().Err, s.Queued("e", "g"))
	}
}

func TestSubmitRejectsAMalformedTask(t *testing.T) {
	s := New(Options{})
	for name, tk := range map[string]Task{
		"no entity":     {Group: "g", Class: governor.ClassQuery},
		"no kind/group": {Entity: "e", Class: governor.ClassQuery},
		"bad class":     {Entity: "e", Group: "g", Class: 9},
	} {
		if err := s.Submit(tk); err == nil {
			t.Errorf("%s: Submit accepted it", name)
		}
	}
}

func TestRunDispatchesOnTheSystemClockAndStopsOnCancel(t *testing.T) {
	e := &fakeExec{max: 1}
	s := New(Options{Workers: 2, PollInterval: 5 * time.Millisecond})
	s.Register("k", e)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	f, _ := s.SubmitInteractive(Task{Kind: "k", Entity: "e", Group: "g"})
	mustSubmit(t, s, task("soon", governor.ClassKeepAlive, time.Now().Add(30*time.Millisecond)))
	select {
	case <-f.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("interactive task never ran")
	}
	deadline := time.After(5 * time.Second)
	for len(e.batches()) < 2 {
		select {
		case <-deadline:
			t.Fatal("a task that came due later never ran")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}
