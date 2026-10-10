package engine_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store/storefault"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/testutil"
)

// sameResult checks that a retry got the original result, marked as a
// replay.
func sameResult(t *testing.T, retry, original engine.Result) {
	t.Helper()
	if !retry.Replayed {
		t.Errorf("the retry %+v is not marked as a replay", retry)
	}
	if original.Replayed {
		t.Errorf("the original %+v is marked as a replay", original)
	}
	if retry.Changed != original.Changed || retry.Note != original.Note || retry.BatchID != original.BatchID ||
		!slices.Equal(retry.EventIDs, original.EventIDs) {
		t.Errorf("the retry %+v differs from the original %+v", retry, original)
	}
}

// countID is the number of lines of the log whose event id is id.
func (h *harness) countID(id event.ID) int {
	n := 0
	for _, e := range h.fileEvents() {
		if e.ID == id {
			n++
		}
	}
	return n
}

func TestIdempotentRetrySameResult(t *testing.T) {
	t.Run("a lone event", func(t *testing.T) {
		h := newHarness(t, nil)
		h.bootstrap()
		c := command.CompleteTask{ID: clientID(), TaskID: taskOf(7, "plan-day")}
		original := h.at(local(9, 5), c)
		lines := h.lines()
		retry := h.do(c)
		sameResult(t, retry, original)
		if !slices.Equal(original.EventIDs, []event.ID{c.ID}) || original.BatchID != "" {
			t.Errorf("the original %+v, want the one event %s", original, c.ID)
		}
		if h.lines() != lines || retry.Version != h.e.Version() {
			t.Errorf("the retry appended (%d lines, want %d) or reports version %+v", h.lines(), lines, retry.Version)
		}
	})
	t.Run("a batch", func(t *testing.T) {
		h := newHarness(t, nil)
		c := bootstrapCmd(clientID())
		original := h.do(c)
		lines := h.lines()
		retry := h.do(c)
		sameResult(t, retry, original)
		if original.BatchID != c.ID || len(original.EventIDs) != lines-1 {
			t.Errorf("the original %+v, want batch %s with %d events before its commit marker", original, c.ID, lines-1)
		}
		if h.lines() != lines {
			t.Errorf("the retry appended: %d lines, want %d", h.lines(), lines)
		}
	})
}

func TestIdempotentRetryAfterRestart(t *testing.T) {
	h := newHarness(t, nil)
	boot := bootstrapCmd(clientID())
	bootResult := h.do(boot)
	complete := command.CompleteTask{ID: clientID(), TaskID: taskOf(7, "plan-day")}
	completed := h.at(local(9, 5), complete)
	lines := h.lines()

	h.reopen()
	sameResult(t, h.do(boot), bootResult)
	sameResult(t, h.do(complete), completed)
	if h.lines() != lines {
		t.Errorf("retries after a restart appended: %d lines, want %d", h.lines(), lines)
	}
}

func TestIdempotentRetryWithEffectiveAtOmittedLater(t *testing.T) {
	h := newHarness(t, nil)
	h.bootstrap()
	c := command.CompleteTask{ID: clientID(), TaskID: taskOf(7, "plan-day")}
	original := h.at(local(9, 5), c)
	sameResult(t, h.at(local(11, 0), c), original)
	h.reopen()
	sameResult(t, h.at(local(15, 0), c), original)
}

func TestIdConflictOnDifferentPayload(t *testing.T) {
	planDay, postPlan := taskOf(7, "plan-day"), taskOf(7, "post-plan")
	t.Run("the same id with other content", func(t *testing.T) {
		h := newHarness(t, nil)
		h.bootstrap()
		id := clientID()
		h.at(local(9, 5), command.CompleteTask{ID: id, TaskID: planDay})
		h.reject(command.CompleteTask{ID: id, TaskID: postPlan}, command.ReasonIDConflict)
		h.reject(command.CompleteTask{ID: id, TaskID: planDay, EffectiveAt: testutil.Ptr(local(9, 0))}, command.ReasonIDConflict)
		h.reopen()
		h.reject(command.CompleteTask{ID: id, TaskID: postPlan}, command.ReasonIDConflict)
	})
	t.Run("a batch id reused by another request", func(t *testing.T) {
		h := newHarness(t, nil)
		boot := h.bootstrap()
		h.reject(command.CompleteTask{ID: boot.BatchID, TaskID: planDay}, command.ReasonIDConflict)
	})
	t.Run("the id of a member of a batch", func(t *testing.T) {
		h := newHarness(t, nil)
		boot := h.bootstrap()
		h.reject(command.CompleteTask{ID: boot.EventIDs[0], TaskID: planDay}, command.ReasonIDConflict)
	})
	t.Run("the id of an event stored with no hash", func(t *testing.T) {
		h := newHarness(t, nil)
		h.bootstrap()
		r := h.at(local(9, 5), command.CompleteTask{TaskID: planDay})
		h.reject(command.SkipTask{ID: r.EventIDs[0], TaskID: postPlan, Reason: "x"}, command.ReasonIDConflict)
		// The same content under that id is no retry either: it never had an id.
		h.reject(command.CompleteTask{ID: r.EventIDs[0], TaskID: planDay}, command.ReasonIDConflict)
		h.reopen()
		h.reject(command.SkipTask{ID: r.EventIDs[0], TaskID: postPlan, Reason: "x"}, command.ReasonIDConflict)
	})
	t.Run("the id of a batch stored with no hash", func(t *testing.T) {
		h := newHarness(t, nil)
		h.bootstrap()
		a := h.start(local(9, 0), deepWork)
		h.start(local(9, 10), review)
		sw := h.at(local(9, 20), command.SwitchCycle{To: a})
		if sw.BatchID == "" {
			t.Fatal("the switch has no batch id")
		}
		h.reject(command.CompleteTask{ID: sw.BatchID, TaskID: planDay}, command.ReasonIDConflict)
		h.reopen()
		h.reject(command.CompleteTask{ID: sw.BatchID, TaskID: planDay}, command.ReasonIDConflict)
	})
}

func TestSwitchIsIdempotentByRequestID(t *testing.T) {
	h := newHarness(t, nil)
	h.bootstrap()
	a := h.start(local(9, 0), deepWork)
	h.start(local(9, 10), review)
	c := command.SwitchCycle{ID: clientID(), To: a}
	original := h.at(local(9, 20), c)
	if original.BatchID != c.ID || len(original.EventIDs) != 2 {
		t.Fatalf("the switch %+v, want batch %s holding a pause and a resume", original, c.ID)
	}
	lines := h.lines()
	sameResult(t, h.at(local(9, 30), c), original)
	h.reopen()
	sameResult(t, h.at(local(9, 40), c), original)
	if h.lines() != lines {
		t.Errorf("a retried switch appended: %d lines, want %d", h.lines(), lines)
	}
}

func TestIdempotencyLookupRunsBeforeValidation(t *testing.T) {
	h := newHarness(t, nil)
	h.bootstrap()
	planDay := taskOf(7, "plan-day")
	c := command.CompleteTask{ID: clientID(), TaskID: planDay}
	original := h.at(local(9, 5), c)
	// As a new request it is now refused: the task is resolved.
	h.reject(command.CompleteTask{TaskID: planDay}, command.ReasonTaskAlreadyResolved)
	sameResult(t, h.at(local(9, 30), c), original)
	h.at(localOn(8, 9, 0), rolloverCmd(clientID(), 8))
	sameResult(t, h.do(c), original)
}

func TestStoreUnavailableThenRetrySucceedsOnce(t *testing.T) {
	planDay := taskOf(7, "plan-day")
	t.Run("the response was lost", func(t *testing.T) {
		h := newHarness(t, nil)
		h.bootstrap()
		c := command.CompleteTask{ID: clientID(), TaskID: planDay}
		original := h.at(local(9, 5), c) // the client never saw this
		sameResult(t, h.do(c), original)
		if n := h.countID(c.ID); n != 1 {
			t.Errorf("event %s is %d times in the log, want once", c.ID, n)
		}
	})
	t.Run("the append reached the disk and the store went read-only", func(t *testing.T) {
		h := newHarness(t, nil)
		h.bootstrap()
		lines := h.lines()
		c := command.CompleteTask{ID: clientID(), TaskID: planDay}
		h.injectOnLog(storefault.OpSync, storefault.OpTruncate) // the fsync fails and the line cannot be taken out
		h.clock.Set(local(9, 5))
		_, err := h.try(c)
		r := testutil.RejectionOf(t, err, command.ReasonStoreUnavailable)
		if !hasPrefixAndGuidance(r.Message, store.ReasonAppendSync, unknownReadOnly) {
			t.Errorf("message %q, want the READ-ONLY sentence and the retry guidance", r.Message)
		}
		if h.lines() != lines+1 {
			t.Fatalf("the file has %d lines, want the line of the failed append still there (%d)", h.lines(), lines+1)
		}
		// In this process the store is read-only and the request was never recorded.
		_, err = h.try(c)
		testutil.RejectionOf(t, err, command.ReasonStoreUnavailable)

		h.reopen()
		retry := h.do(c)
		if !retry.Replayed || !slices.Equal(retry.EventIDs, []event.ID{c.ID}) {
			t.Errorf("the retry after the restart %+v, want the original event %s as a replay", retry, c.ID)
		}
		if n := h.countID(c.ID); n != 1 || h.task(planDay).Status != projection.Completed {
			t.Errorf("event %s is %d times in the log and the task is %s", c.ID, n, h.task(planDay).Status)
		}
	})
	t.Run("the append was rolled back", func(t *testing.T) {
		h := newHarness(t, nil)
		h.bootstrap()
		c := command.CompleteTask{ID: clientID(), TaskID: planDay}
		h.fs.Inject(storefault.Rule{Op: storefault.OpWrite, Name: logName, Partial: 9})
		h.clock.Set(local(9, 5))
		if r := h.reject(c, command.ReasonStoreUnavailable); r.Message != unknownOutcome {
			t.Errorf("message %q, want %q", r.Message, unknownOutcome)
		}
		first := h.do(c)
		if first.Replayed || !first.Changed {
			t.Errorf("the first retry %+v, want it handled as new", first)
		}
		sameResult(t, h.do(c), first)
		if n := h.countID(c.ID); n != 1 {
			t.Errorf("event %s is %d times in the log, want once", c.ID, n)
		}
	})
}

func TestDryRunNeverReadsOrWritesTheIdempotencyIndexOrNoOpCache(t *testing.T) {
	h := newHarness(t, nil)
	h.bootstrap()
	durable := clientID()
	h.at(local(9, 0), command.ChangeProfile{ID: durable, Profile: "on-call"})
	a := h.start(local(9, 5), deepWork)
	noOp := clientID()
	h.at(local(9, 10), command.ResumeCycle{ID: noOp, CycleID: a})
	lines, version := h.lines(), h.e.Version()

	for name, c := range map[string]command.Command{
		"the id of a durable request, same content":  command.ChangeProfile{ID: durable, Profile: "on-call", DryRun: true},
		"the id of a durable request, other content": command.ChangeProfile{ID: durable, Profile: "normal", DryRun: true},
		"the id of a cached no-op":                   command.ChangeProfile{ID: noOp, Profile: "normal", DryRun: true},
	} {
		r := h.do(c)
		if r.Preview == nil || r.Replayed || r.Changed {
			t.Errorf("%s: Result %+v, want a preview", name, r)
		}
	}

	fresh := clientID()
	if r := h.do(command.ChangeProfile{ID: fresh, Profile: "normal", DryRun: true}); r.Preview == nil {
		t.Errorf("a dry run with a fresh id: %+v", r)
	}
	if h.lines() != lines || h.e.Version() != version {
		t.Fatal("a dry run appended")
	}
	if r := h.at(local(9, 20), command.CompleteTask{ID: fresh, TaskID: taskOf(7, "plan-day")}); !r.Changed || r.Replayed {
		t.Errorf("a real request with the dry run's id %+v, want it handled as new", r)
	}
}

func TestRetriedNoOpReturnsTheOriginalResult(t *testing.T) {
	h := newHarness(t, nil)
	h.bootstrap()
	a := h.start(local(9, 0), deepWork)
	h.at(local(9, 10), command.PauseCycle{CycleID: a})
	c := command.PauseCycle{ID: clientID(), CycleID: a}
	original := h.at(local(9, 20), c)
	if original.Changed || original.Note == "" {
		t.Fatalf("the pause of a paused cycle %+v, want a no-op", original)
	}
	h.at(local(9, 30), command.ResumeCycle{CycleID: a})
	lines := h.lines()
	retry := h.at(local(9, 40), c) // evaluated now it would pause the cycle
	sameResult(t, retry, original)
	if retry.Changed || len(retry.EventIDs) != 0 || h.lines() != lines {
		t.Errorf("the retried no-op %+v appended (%d lines, want %d)", retry, h.lines(), lines)
	}
}

func TestNoOpIdConflictOnDifferentPayload(t *testing.T) {
	h := newHarness(t, nil)
	h.bootstrap()
	a := h.start(local(9, 0), deepWork)
	id := clientID()
	h.at(local(9, 10), command.ResumeCycle{ID: id, CycleID: a})
	h.reject(command.ResumeCycle{ID: id, CycleID: a, EffectiveAt: testutil.Ptr(local(9, 5))}, command.ReasonIDConflict)
	h.reject(command.StopCycle{ID: id, CycleID: a}, command.ReasonIDConflict)
}

func TestNoOpCacheIsBoundedAndLeastRecentlyUsed(t *testing.T) {
	h := newHarness(t, nil)
	h.bootstrap()
	a := h.start(local(9, 0), deepWork)
	h.at(local(9, 10), command.PauseCycle{CycleID: a})
	h.clock.Set(local(9, 20))

	const size = 1024
	ids := make([]event.ID, size+1)
	pause := func(i int) command.PauseCycle { return command.PauseCycle{ID: ids[i], CycleID: a} }
	other := func(i int) command.PauseCycle {
		c := pause(i)
		c.EffectiveAt = testutil.Ptr(local(9, 15))
		return c
	}
	for i := range size {
		ids[i] = clientID()
		h.do(pause(i))
	}
	// Touch the oldest, so the second oldest is now the least recently used.
	if r := h.do(pause(0)); !r.Replayed {
		t.Fatalf("a retry of a cached no-op %+v is not a replay", r)
	}
	ids[size] = clientID()
	h.do(pause(size)) // the 1025th evicts ids[1]

	for _, i := range []int{0, 2, size - 1, size} {
		h.reject(other(i), command.ReasonIDConflict) // still cached
	}
	r := h.do(other(1)) // evicted: evaluated again as a new request
	if r.Replayed || r.Changed || r.Note == "" {
		t.Errorf("the evicted id %+v, want a fresh no-op", r)
	}
}

func TestNoOpIsEvaluatedAgainAfterARestart(t *testing.T) {
	h := newHarness(t, nil)
	h.bootstrap()
	a := h.start(local(9, 0), deepWork)
	c := command.ResumeCycle{ID: clientID(), CycleID: a}
	original := h.at(local(9, 10), c)
	if original.Changed {
		t.Fatalf("the resume of a running cycle %+v, want a no-op", original)
	}
	h.at(local(9, 20), command.PauseCycle{CycleID: a})
	sameResult(t, h.at(local(9, 30), c), original)

	h.reopen()
	lines := h.lines()
	r := h.at(local(9, 40), c)
	if !r.Changed || r.Replayed || !slices.Equal(r.EventIDs, []event.ID{c.ID}) || h.lines() != lines+1 {
		t.Errorf("after a restart the request %+v, want it evaluated again and appended", r)
	}
	if got := h.cycle(a).Status; got != projection.Running {
		t.Errorf("the cycle is %s, want running", got)
	}
}

func TestRepeatedStartWithTheSameIDReturnsTheOriginal(t *testing.T) {
	h := newHarness(t, nil)
	h.bootstrap()
	c := command.StartCycle{ID: clientID(), Type: deepWork}
	original := h.at(local(9, 0), c)
	if !slices.Equal(original.EventIDs, []event.ID{c.ID}) {
		t.Fatalf("the start %+v, want the event %s", original, c.ID)
	}
	sameResult(t, h.at(local(9, 10), c), original)
	h.reopen()
	sameResult(t, h.at(local(9, 20), c), original)
	if n := len(h.e.Snapshot().Model.Cycles()); n != 1 {
		t.Errorf("%d cycles, want one", n)
	}
}

func TestRetryOfADurableRequestReturnsTheOriginalWhileReadOnly(t *testing.T) {
	h := newHarness(t, nil)
	h.bootstrap()
	c := command.CompleteTask{ID: clientID(), TaskID: taskOf(7, "plan-day")}
	original := h.at(local(9, 5), c)
	a := h.start(local(9, 10), deepWork)
	noOp := command.ResumeCycle{ID: clientID(), CycleID: a}
	noOpResult := h.at(local(9, 15), noOp)

	h.injectOnLog(storefault.OpSync)
	h.clock.Set(local(9, 20))
	_, err := h.try(command.SkipTask{TaskID: taskOf(7, "post-plan"), Reason: "not today"})
	testutil.RejectionOf(t, err, command.ReasonStoreUnavailable)
	if !h.e.Health().ReadOnly {
		t.Fatal("the store is not read-only")
	}

	sameResult(t, h.do(c), original)
	sameResult(t, h.do(noOp), noOpResult)
	r := h.reject(command.StopCycle{CycleID: a}, command.ReasonStoreUnavailable)
	if r.Message != readOnlySentence(store.ReasonAppendSync) {
		t.Errorf("a new mutation: message %q", r.Message)
	}
	dry := rolloverCmd(clientID(), 8)
	dry.DryRun = true
	if got := h.do(dry); got.Preview == nil {
		t.Errorf("a dry run while read-only %+v, want a preview", got)
	}
	if !reflect.DeepEqual(h.e.Health(), engine.Health{ReadOnly: true, Reason: store.ReasonAppendSync, Since: local(9, 20)}) {
		t.Errorf("Health %+v", h.e.Health())
	}
}

func TestIdConflictOfABatchMemberNamesItsBatch(t *testing.T) {
	h := newHarness(t, nil)
	boot := h.bootstrap()
	member := boot.EventIDs[0]
	r := h.reject(command.CompleteTask{ID: member, TaskID: taskOf(7, "plan-day")}, command.ReasonIDConflict)
	if !strings.Contains(r.Message, string(boot.BatchID)) {
		t.Errorf("message %q does not name the batch %s the event is a member of", r.Message, boot.BatchID)
	}
	if strings.Contains(r.Message, "no request with an id produced") {
		t.Errorf("message %q says no request with an id produced the event, but the bootstrap carried one", r.Message)
	}
	h.reopen()
	if r2 := h.reject(command.CompleteTask{ID: member, TaskID: taskOf(7, "plan-day")}, command.ReasonIDConflict); r2.Message != r.Message {
		t.Errorf("after a restart the message is %q, want %q", r2.Message, r.Message)
	}
}
