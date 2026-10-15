package scheduler

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

func TestDefaultOptionValues(t *testing.T) {
	if DefaultWorkers != 4 || DefaultAgeAfter != 5*time.Minute || DefaultRetryDelay != 30*time.Second ||
		DefaultPollInterval != time.Second || defaultHistory != 32 {
		t.Fatalf("defaults = %d %v %v %v %d", DefaultWorkers, DefaultAgeAfter, DefaultRetryDelay, DefaultPollInterval, defaultHistory)
	}
}

func TestNewAppliesADefaultToAnyOptionBelowItsMinimum(t *testing.T) {
	type got struct {
		workers          int
		age, retry, poll time.Duration
		history          int
		systemClock      bool
	}
	read := func(o Options) got {
		s := New(o)
		_, sys := s.clock.(governor.SystemClock)
		return got{
			workers: s.opt.Workers, age: s.opt.AgeAfter, retry: s.opt.RetryDelay,
			poll: s.opt.PollInterval, history: s.opt.History, systemClock: sys,
		}
	}
	defaults := got{workers: 4, age: 5 * time.Minute, retry: 30 * time.Second, poll: time.Second, history: 32, systemClock: true}
	if g := read(Options{}); g != defaults {
		t.Errorf("zero Options = %+v, want %+v", g, defaults)
	}
	if g := read(Options{Workers: -1, AgeAfter: -1, RetryDelay: -1, PollInterval: -1, History: -1}); g != defaults {
		t.Errorf("negative Options = %+v, want %+v", g, defaults)
	}
	// Values at and above the minimum are the caller's own.
	for _, w := range []int{1, 2, 9} {
		if g := read(Options{Workers: w}); g.workers != w {
			t.Errorf("Workers %d became %d", w, g.workers)
		}
	}
	for _, d := range []time.Duration{time.Nanosecond, time.Second, time.Hour} {
		g := read(Options{AgeAfter: d, RetryDelay: d, PollInterval: d})
		if g.age != d || g.retry != d || g.poll != d {
			t.Errorf("duration %v became age %v retry %v poll %v", d, g.age, g.retry, g.poll)
		}
	}
	for _, h := range []int{1, 2, 100} {
		if g := read(Options{History: h}); g.history != h {
			t.Errorf("History %d became %d", h, g.history)
		}
	}
	clk := governor.NewManualClock(t0)
	if s := New(Options{Clock: clk}); s.clock != governor.Clock(clk) {
		t.Error("an explicit clock was replaced")
	}
}

func TestATaskWithoutAKindRunsOnTheExecutorOfItsGroup(t *testing.T) {
	byGroup := &fakeExec{max: 1}
	byKind := &fakeExec{max: 1}
	s := New(Options{Clock: governor.NewManualClock(t0), Workers: 2})
	s.Register("grp", byGroup)
	s.Register("explicit", byKind)
	mustSubmit(t, s, Task{Entity: "e1", Group: "grp", Class: governor.ClassKeepAlive})
	mustSubmit(t, s, Task{Kind: "explicit", Entity: "e2", Group: "grp", Class: governor.ClassKeepAlive})
	s.Drain(context.Background())
	if got := fmt.Sprint(ents(byGroup.batches()[0])); got != "[e1]" {
		t.Errorf("group executor ran %s, want [e1]", got)
	}
	if got := fmt.Sprint(ents(byKind.batches()[0])); got != "[e2]" {
		t.Errorf("kind executor ran %s, want [e2] (a Kind is never overwritten by the Group)", got)
	}
}

func TestEveryWayOfQueueingARefreshValidatesTheTask(t *testing.T) {
	good := Task{Kind: "k", Entity: "e", Group: "g", Class: governor.ClassQuery}
	bad := map[string]Task{
		"no entity":              {Kind: "k", Group: "g", Class: governor.ClassQuery},
		"neither kind nor group": {Entity: "e", Class: governor.ClassQuery},
		"class below range":      {Kind: "k", Entity: "e", Group: "g", Class: 0},
		"class above range":      {Kind: "k", Entity: "e", Group: "g", Class: governor.ClassFill + 1},
	}
	submitters := map[string]func(*Scheduler, Task) error{
		"Submit":     func(s *Scheduler, tk Task) error { return s.Submit(tk) },
		"Reschedule": func(s *Scheduler, tk Task) error { return s.Reschedule(tk) },
	}
	for name, submit := range submitters {
		for what, tk := range bad {
			s := New(Options{Clock: governor.NewManualClock(t0)})
			if err := submit(s, tk); err == nil {
				t.Errorf("%s accepted a task with %s", name, what)
			}
			if len(s.queue) != 0 {
				t.Errorf("%s queued a task with %s", name, what)
			}
		}
		s := New(Options{Clock: governor.NewManualClock(t0)})
		if err := submit(s, good); err != nil {
			t.Errorf("%s rejected a valid task: %v", name, err)
		}
		// A kind without a group is valid: the kind selects the executor.
		if err := submit(s, Task{Kind: "k", Entity: "kind-only", Class: governor.ClassQuery}); err != nil {
			t.Errorf("%s rejected a task with a kind and no group: %v", name, err)
		}
		if !s.Queued("kind-only", "") {
			t.Errorf("%s did not queue the kind-only task", name)
		}
		// A group without a kind is valid: the group names the executor.
		if err := submit(s, Task{Entity: "group-only", Group: "g", Class: governor.ClassQuery}); err != nil {
			t.Errorf("%s rejected a task with a group and no kind: %v", name, err)
		}
	}

	s := New(Options{Clock: governor.NewManualClock(t0)})
	if f, err := s.SubmitInteractive(Task{Kind: "k", Group: "g"}); err == nil || f != nil {
		t.Errorf("SubmitInteractive of a task with no entity = %v, %v; want an error and no flight", f, err)
	}
	if f, err := s.SubmitInteractive(Task{Entity: "e"}); err == nil || f != nil {
		t.Errorf("SubmitInteractive of a task with neither kind nor group = %v, %v; want an error and no flight", f, err)
	}
	if len(s.queue) != 0 {
		t.Error("SubmitInteractive queued a malformed task")
	}
	if f, err := s.SubmitInteractive(Task{Kind: "k", Entity: "e", Group: "g"}); err != nil || f == nil {
		t.Errorf("SubmitInteractive of a valid task = %v, %v", f, err)
	}
}

func TestSubmitMergeOnlyEverMakesATaskMoreUrgent(t *testing.T) {
	s := New(Options{Clock: governor.NewManualClock(t0)})
	k := key{"e", "g"}
	first := Task{
		Kind: "k", Entity: "e", Group: "g", Class: governor.ClassQuery,
		Due: t0.Add(time.Hour), Access: t0.Add(time.Minute), Payload: "first",
	}
	mustSubmit(t, s, first)

	// Every field of the second submission is less urgent: nothing changes, and
	// a nil payload does not erase the one that is there.
	worse := Task{
		Kind: "k", Entity: "e", Group: "g", Class: governor.ClassKeepAlive,
		Due: t0.Add(2 * time.Hour), Access: t0, Payload: nil,
	}
	mustSubmit(t, s, worse)
	e := s.queue[k]
	if e.Class != governor.ClassQuery || !e.Due.Equal(t0.Add(time.Hour)) || !e.Access.Equal(t0.Add(time.Minute)) || e.Payload != "first" {
		t.Fatalf("a less urgent submission changed the task: %+v", e.Task)
	}

	// The same class changes nothing either.
	mustSubmit(t, s, Task{Kind: "k", Entity: "e", Group: "g", Class: governor.ClassQuery, Due: t0.Add(time.Hour)})
	if e.Class != governor.ClassQuery {
		t.Fatalf("class = %v", e.Class)
	}

	// Every field of the third is more urgent: each wins, and a new payload
	// replaces the old.
	better := Task{
		Kind: "k", Entity: "e", Group: "g", Class: governor.ClassInvalidated,
		Due: t0.Add(30 * time.Minute), Access: t0.Add(5 * time.Minute), Payload: "better",
	}
	mustSubmit(t, s, better)
	if e.Class != governor.ClassInvalidated || !e.Due.Equal(t0.Add(30*time.Minute)) ||
		!e.Access.Equal(t0.Add(5*time.Minute)) || e.Payload != "better" {
		t.Fatalf("a more urgent submission did not win: %+v", e.Task)
	}
	if len(s.queue) != 1 {
		t.Fatalf("merging left %d queued tasks, want 1", len(s.queue))
	}

	// A zero due (due now) beats any later one, and nothing beats it.
	mustSubmit(t, s, Task{Kind: "k", Entity: "e", Group: "g", Class: governor.ClassInvalidated})
	if !e.Due.IsZero() {
		t.Fatalf("a task due now did not win: due = %v", e.Due)
	}
	mustSubmit(t, s, Task{Kind: "k", Entity: "e", Group: "g", Class: governor.ClassInvalidated, Due: t0.Add(time.Minute)})
	if !e.Due.IsZero() {
		t.Fatalf("a later due replaced a task due now: due = %v", e.Due)
	}
}

func TestRescheduleReplacesTheQueuedTaskInFullUnlessACallerWaits(t *testing.T) {
	s := New(Options{Clock: governor.NewManualClock(t0)})
	k := key{"e", "g"}
	mustSubmit(t, s, Task{
		Kind: "k", Entity: "e", Group: "g", Class: governor.ClassInvalidated,
		Due: t0.Add(time.Minute), Access: t0.Add(time.Hour), Payload: "old",
	})
	next := Task{
		Kind: "k", Entity: "e", Group: "g", Class: governor.ClassFill,
		Due: t0.Add(3 * time.Hour), Access: t0, Payload: nil,
	}
	if err := s.Reschedule(next); err != nil {
		t.Fatal(err)
	}
	e := s.queue[k]
	if e.Class != governor.ClassFill || !e.Due.Equal(t0.Add(3*time.Hour)) || !e.Access.Equal(t0) || e.Payload != nil {
		t.Fatalf("Reschedule did not replace the task in full: %+v", e.Task)
	}
	if len(s.queue) != 1 {
		t.Fatalf("Reschedule left %d queued tasks", len(s.queue))
	}

	// A queued task a caller waits on keeps its urgency: the Reschedule merges.
	f, err := s.SubmitInteractive(Task{Kind: "k", Entity: "w", Group: "g"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Reschedule(Task{Kind: "k", Entity: "w", Group: "g", Class: governor.ClassKeepAlive, Due: t0.Add(time.Hour), Payload: "p"}); err != nil {
		t.Fatal(err)
	}
	w := s.queue[key{"w", "g"}]
	if w.Class != governor.ClassInteractive || !w.Due.IsZero() || w.flight != f {
		t.Fatalf("Reschedule downgraded a task a caller waits on: class %v due %v", w.Class, w.Due)
	}
	if w.Payload != "p" {
		t.Errorf("the merge dropped the new payload: %v", w.Payload)
	}
}

func TestDueBeforeTreatsAZeroTimeAsDueFirst(t *testing.T) {
	early, late := t0, t0.Add(time.Hour)
	cases := []struct {
		name string
		a, b time.Time
		want bool
	}{
		{"zero before zero", time.Time{}, time.Time{}, false},
		{"zero before a time", time.Time{}, late, true},
		{"a time before zero", late, time.Time{}, false},
		{"earlier before later", early, late, true},
		{"later before earlier", late, early, false},
		{"equal times", early, early, false},
	}
	for _, c := range cases {
		if got := dueBefore(c.a, c.b); got != c.want {
			t.Errorf("%s: dueBefore = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCancelRemovesOnlyWhatItNames(t *testing.T) {
	s := New(Options{Clock: governor.NewManualClock(t0)})
	mustSubmitAll := func(entity string, groups ...string) {
		for _, g := range groups {
			mustSubmit(t, s, Task{Kind: "k", Entity: entity, Group: g, Class: governor.ClassKeepAlive})
		}
	}
	mustSubmitAll("a", "g1", "g2")
	mustSubmitAll("b", "g1", "g2")
	mustSubmitAll("c", "g1")
	waiting, _ := s.SubmitInteractive(Task{Kind: "k", Entity: "b", Group: "g3"})

	s.Cancel("nobody", "g1") // a pair that is not queued: nothing happens
	s.Cancel("a", "g1")      // a queued task with no caller waiting: no flight to complete
	if s.Queued("a", "g1") || !s.Queued("a", "g2") {
		t.Fatalf("Cancel(a, g1) removed the wrong tasks: a/g1 = %v a/g2 = %v", s.Queued("a", "g1"), s.Queued("a", "g2"))
	}

	s.CancelEntity("b")
	for _, g := range []string{"g1", "g2", "g3"} {
		if s.Queued("b", g) {
			t.Errorf("CancelEntity left b/%s queued", g)
		}
	}
	for _, p := range [][2]string{{"a", "g2"}, {"c", "g1"}} {
		if !s.Queued(p[0], p[1]) {
			t.Errorf("CancelEntity(b) removed %s/%s", p[0], p[1])
		}
	}
	if !completed(waiting) || waiting.Result().Err != ErrCanceled {
		t.Errorf("the flight of a canceled task: done = %v err = %v, want ErrCanceled", completed(waiting), waiting.Result().Err)
	}
	// Entities that sort either side of the canceled one are untouched, so the
	// match is on the name itself and not an ordering of it.
	s2 := New(Options{Clock: governor.NewManualClock(t0)})
	for _, e := range []string{"a", "b", "c"} {
		mustSubmit(t, s2, Task{Kind: "k", Entity: e, Group: "g", Class: governor.ClassKeepAlive})
	}
	s2.CancelEntity("b")
	if !s2.Queued("a", "g") || s2.Queued("b", "g") || !s2.Queued("c", "g") {
		t.Errorf("CancelEntity(b): a = %v b = %v c = %v", s2.Queued("a", "g"), s2.Queued("b", "g"), s2.Queued("c", "g"))
	}
}

func TestPositionRanksEqualDueTimesByArrivalAndIgnoresOtherClasses(t *testing.T) {
	s := New(Options{Clock: governor.NewManualClock(t0)})
	for _, n := range []string{"first", "second", "third"} {
		mustSubmit(t, s, task(n, governor.ClassKeepAlive, t0.Add(time.Hour)))
	}
	// Tasks of other classes, due earlier, do not count towards the rank.
	mustSubmit(t, s, task("better-class", governor.ClassQuery, t0.Add(time.Minute)))
	mustSubmit(t, s, task("worse-class", governor.ClassFill, t0.Add(time.Minute)))
	for want, name := range []string{"first", "second", "third"} {
		if got, ok := s.Position(name, "g"); !ok || got != want+1 {
			t.Errorf("Position(%s) = %d %v, want %d", name, got, ok, want+1)
		}
	}
	if got, _ := s.Position("better-class", "g"); got != 1 {
		t.Errorf("Position(better-class) = %d, want 1", got)
	}
	if got, _ := s.Position("worse-class", "g"); got != 1 {
		t.Errorf("Position(worse-class) = %d, want 1", got)
	}
}

func TestDrainRunsTasksInTheOrderTheyWereSubmitted(t *testing.T) {
	e := &fakeExec{max: 1}
	s, _ := newSched(t, Options{Workers: 1}, e)
	var want []string
	for i := range 12 {
		name := fmt.Sprintf("t%02d", i)
		want = append(want, name)
		mustSubmit(t, s, task(name, governor.ClassKeepAlive, t0))
	}
	s.Drain(context.Background())
	var got []string
	for _, b := range e.batches() {
		got = append(got, ents(b)...)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("order = %v, want %v (equal class, access and due: arrival order)", got, want)
	}
}

func TestStateListsEveryClassInOrderAndMeasuresOverdueOnlyForDueTimes(t *testing.T) {
	s := New(Options{Clock: governor.NewManualClock(t0)})
	mustSubmit(t, s, task("due-now", governor.ClassKeepAlive, time.Time{}))
	mustSubmit(t, s, task("future", governor.ClassKeepAlive, t0.Add(time.Hour)))
	mustSubmit(t, s, task("at-now", governor.ClassKeepAlive, t0))
	mustSubmit(t, s, task("late", governor.ClassKeepAlive, t0.Add(-2*time.Minute)))
	mustSubmit(t, s, task("later", governor.ClassKeepAlive, t0.Add(-time.Minute)))
	mustSubmit(t, s, task("q", governor.ClassQuery, time.Time{}))
	st := s.State()
	if len(st.Classes) != 5 {
		t.Fatalf("State has %d classes, want one per class", len(st.Classes))
	}
	for i, cs := range st.Classes {
		if want := governor.Class(i + 1); cs.Class != want {
			t.Errorf("Classes[%d].Class = %v, want %v", i, cs.Class, want)
		}
	}
	if cs := st.Classes[3]; cs.Depth != 5 || cs.OldestOverdue != 2*time.Minute {
		t.Errorf("class 4: depth %d overdue %v, want 5 and 2m", cs.Depth, cs.OldestOverdue)
	}
	if cs := st.Classes[2]; cs.Depth != 1 || cs.OldestOverdue != 0 {
		t.Errorf("class 3: depth %d overdue %v, want 1 and 0 (a task due now is not overdue)", cs.Depth, cs.OldestOverdue)
	}
	if cs := st.Classes[4]; cs.Depth != 0 {
		t.Errorf("class 5: depth %d, want 0", cs.Depth)
	}
	if !st.At.Equal(t0) {
		t.Errorf("State.At = %v, want the clock's now", st.At)
	}
}

func TestEffClassAgesAKeepAliveTaskOnlyOnceItIsOverdueForTheAgingWindow(t *testing.T) {
	s := New(Options{Clock: governor.NewManualClock(t0), AgeAfter: 5 * time.Minute})
	mk := func(c governor.Class, due time.Time) *entry {
		return &entry{Task: Task{Kind: "k", Entity: "e", Group: "g", Class: c, Due: due}}
	}
	cases := []struct {
		name string
		e    *entry
		want governor.Class
	}{
		{"interactive", mk(governor.ClassInteractive, time.Time{}), governor.ClassInteractive},
		{"interactive overdue for hours", mk(governor.ClassInteractive, t0.Add(-5*time.Hour)), governor.ClassInteractive},
		{"invalidated overdue for hours", mk(governor.ClassInvalidated, t0.Add(-5*time.Hour)), governor.ClassInvalidated},
		{"query overdue for hours", mk(governor.ClassQuery, t0.Add(-5*time.Hour)), governor.ClassQuery},
		{"keep-alive due now", mk(governor.ClassKeepAlive, time.Time{}), governor.ClassKeepAlive},
		{"keep-alive overdue a little", mk(governor.ClassKeepAlive, t0.Add(-5*time.Minute+time.Nanosecond)), governor.ClassKeepAlive},
		{"keep-alive overdue exactly the window", mk(governor.ClassKeepAlive, t0.Add(-5*time.Minute)), governor.ClassQuery},
		{"keep-alive overdue longer", mk(governor.ClassKeepAlive, t0.Add(-time.Hour)), governor.ClassQuery},
		{"fill due now", mk(governor.ClassFill, time.Time{}), governor.ClassKeepAlive},
		{"fill overdue a little", mk(governor.ClassFill, t0.Add(-time.Minute)), governor.ClassKeepAlive},
		{"fill overdue the window", mk(governor.ClassFill, t0.Add(-5*time.Minute)), governor.ClassQuery},
	}
	for _, c := range cases {
		if got := s.effClass(c.e, t0); got != c.want {
			t.Errorf("%s: effClass = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestLessOrdersByClassThenAccessForKeepAliveThenDueThenArrival(t *testing.T) {
	s := New(Options{Clock: governor.NewManualClock(t0)})
	var seq uint64
	mk := func(c governor.Class, due, access time.Time) *entry {
		seq++
		return &entry{Task: Task{Kind: "k", Entity: fmt.Sprint(seq), Group: "g", Class: c, Due: due, Access: access}, seq: seq}
	}
	ordered := func(name string, first, second *entry) {
		t.Helper()
		if !s.less(first, second, t0) {
			t.Errorf("%s: the first is not ranked ahead of the second", name)
		}
		if s.less(second, first, t0) {
			t.Errorf("%s: the second is ranked ahead of the first", name)
		}
	}
	due := t0.Add(-time.Minute)
	ordered("a better class wins", mk(governor.ClassInteractive, due, time.Time{}), mk(governor.ClassQuery, due, time.Time{}))
	ordered("a better class wins even with a later due", mk(governor.ClassQuery, t0.Add(-time.Second), time.Time{}), mk(governor.ClassKeepAlive, t0.Add(-time.Minute), time.Time{}))
	ordered("keep-alive: the most recently accessed first", mk(governor.ClassKeepAlive, t0.Add(-time.Second), t0.Add(-time.Minute)), mk(governor.ClassKeepAlive, t0.Add(-2*time.Minute), t0.Add(-time.Hour)))
	ordered("keep-alive with equal access: the earlier due", mk(governor.ClassKeepAlive, t0.Add(-2*time.Minute), t0), mk(governor.ClassKeepAlive, t0.Add(-time.Minute), t0))
	// Access decides only within class 4: for every other class the due time does.
	for _, c := range []governor.Class{governor.ClassInteractive, governor.ClassInvalidated, governor.ClassQuery} {
		ordered(fmt.Sprintf("class %d: the earlier due wins over a more recent access", c),
			mk(c, t0.Add(-2*time.Minute), t0.Add(-time.Hour)), mk(c, t0.Add(-time.Minute), t0))
	}
	ordered("a zero due (due now) is first", mk(governor.ClassQuery, time.Time{}, time.Time{}), mk(governor.ClassQuery, due, time.Time{}))
	ordered("equal due: arrival order", mk(governor.ClassQuery, due, time.Time{}), mk(governor.ClassQuery, due, time.Time{}))
	ordered("equal zero due: arrival order", mk(governor.ClassKeepAlive, time.Time{}, time.Time{}), mk(governor.ClassKeepAlive, time.Time{}, time.Time{}))
	// An aged keep-alive task competes as class 3, behind a class 3 task due earlier.
	ordered("an aged keep-alive task ranks as a query by due",
		mk(governor.ClassQuery, t0.Add(-time.Hour), time.Time{}), mk(governor.ClassKeepAlive, t0.Add(-10*time.Minute), t0))

	same := mk(governor.ClassQuery, due, time.Time{})
	if s.less(same, same, t0) {
		t.Error("a task is ranked ahead of itself")
	}
}

func TestAZeroDueKeepAliveTaskDoesNotAge(t *testing.T) {
	// A zero Due means due now, not due since the beginning of time.
	e := &fakeExec{max: 1}
	s, _ := newSched(t, Options{Workers: 1}, e)
	mustSubmit(t, s, task("keep", governor.ClassKeepAlive, time.Time{}))
	mustSubmit(t, s, task("query", governor.ClassQuery, t0))
	s.Drain(context.Background())
	var got []string
	for _, b := range e.batches() {
		got = append(got, ents(b)...)
	}
	if fmt.Sprint(got) != "[query keep]" {
		t.Fatalf("order = %v, want [query keep]", got)
	}
}
