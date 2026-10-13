package groupsvc_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc/groupsvctest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// hookLog records OnGroupRefreshed calls.
type hookLog struct {
	mu    sync.Mutex
	calls []hookCall
}

type hookCall struct {
	prev *store.GroupRow
	next store.GroupRow
	info groupsvc.RefreshInfo
}

func (h *hookLog) fire(prev *store.GroupRow, next store.GroupRow, info groupsvc.RefreshInfo) {
	h.mu.Lock()
	h.calls = append(h.calls, hookCall{prev, next, info})
	h.mu.Unlock()
}

func (h *hookLog) all() []hookCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]hookCall(nil), h.calls...)
}

func TestARefreshThatBeganBeforeAnInvalidationStoresNothingAndFiresNoHook(t *testing.T) {
	ctx := context.Background()
	hooks := &hookLog{}
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{OnGroupRefreshed: hooks.fire})
	f := register(r, groups.Detail)
	seed(r, node, staleRow(r, node, groups.Detail, "old"))
	release := f[groups.Detail].Block()
	t.Cleanup(release)

	if err := r.Svc.QueueGroup(ctx, node, groups.Detail, governor.ClassInvalidated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	r.Sched.Step(ctx)
	if !f[groups.Detail].WaitStarted(1, 10*time.Second) {
		t.Fatal("the refresh never started")
	}
	// A write lands while the fetch is in flight.
	if err := groups.Invalidate(ctx, r.Store, entity.KindPR, node, groups.Detail, r.Clock.Now()); err != nil {
		t.Fatal(err)
	}
	release()
	r.Sched.Wait()

	row, _ := r.Group(node, groups.Detail)
	if string(row.Content) != "old" || row.Generation != 2 {
		t.Fatalf("row = %+v, want the old content at the invalidation's generation (the result is discarded)", row)
	}
	if n := len(hooks.all()); n != 0 {
		t.Fatalf("OnGroupRefreshed fired %d times for a superseded refresh", n)
	}
	// The row is due now, so the cycle refetches it, and that one is stored.
	r.Sched.Drain(ctx)
	row, _ = r.Group(node, groups.Detail)
	if string(row.Content) == "old" || row.Generation != 3 {
		t.Fatalf("after the retry the row = %+v", row)
	}
	if n := len(hooks.all()); n != 1 {
		t.Errorf("OnGroupRefreshed fired %d times, want 1", n)
	}
}

func TestOnGroupRefreshedGetsThePreviousRowTheNewOneAndHowItWasFetched(t *testing.T) {
	ctx := context.Background()
	hooks := &hookLog{}
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{OnGroupRefreshed: hooks.fire})
	register(r, groups.Detail)
	seed(r, node)

	r.Start()
	ensure(t, r, node, []string{groups.Detail}, groupsvc.EnsureOptions{})
	calls := hooks.all()
	if len(calls) != 1 || calls[0].prev != nil {
		t.Fatalf("first fetch hook calls = %+v, want one with no previous row", calls)
	}
	c := calls[0]
	if c.info.Class != governor.ClassInteractive || c.info.Purpose != governor.PurposeInteractive || c.info.Started.IsZero() {
		t.Errorf("info = %+v", c.info)
	}
	if c.next.Group != groups.Detail || c.next.ID != node || c.next.Generation != 1 || len(c.next.Content) == 0 {
		t.Errorf("next = %+v", c.next)
	}

	if err := r.Svc.QueueGroup(ctx, node, groups.Detail, governor.ClassInvalidated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the class 2 refresh", func() bool { return len(hooks.all()) == 2 })
	c = hooks.all()[1]
	if c.prev == nil || string(c.prev.Content) != string(hooks.all()[0].next.Content) || c.info.Class != governor.ClassInvalidated {
		t.Errorf("second call = %+v, want the first fetch's row as prev and class 2", c)
	}
}

func TestEachClass1And2FetchBooksTheNextBackgroundTaskAtTheNewFreshUntil(t *testing.T) {
	ctx := context.Background()
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	f := register(r, groups.Detail)
	r.PutQuery("mine", "PR_member")
	seed(r, node)
	r.SeedSummary("PR_member", r.Clock.Now())

	r.Start()
	ensure(t, r, node, []string{groups.Detail}, groupsvc.EnsureOptions{})
	ensure(t, r, "PR_member", []string{groups.Detail}, groupsvc.EnsureOptions{})
	// A class 1 fetch has completed; its own task no longer runs, and the next
	// cycle is queued: class 4, and class 3 for a member of a configured query.
	if c, queued, running, _ := r.Svc.Inspect(ctx, node, groups.Detail); !queued || running || c != governor.ClassKeepAlive {
		t.Errorf("after a class 1 fetch: class %v queued %v running %v, want class 4 queued", c, queued, running)
	}
	if c, queued, _, _ := r.Svc.Inspect(ctx, "PR_member", groups.Detail); !queued || c != governor.ClassQuery {
		t.Errorf("member: class %v queued %v, want class 3 queued", c, queued)
	}

	// It is due at fresh_until: not a minute before, then it runs.
	before := f[groups.Detail].CallCount()
	r.Clock.Advance(9 * time.Minute)
	r.Sched.Drain(ctx)
	if got := f[groups.Detail].CallCount(); got != before {
		t.Fatalf("a task due in a minute ran: %d calls, was %d", got, before)
	}
	r.Clock.Advance(2 * time.Minute)
	r.Sched.Drain(ctx)
	// Both are due: one batch of two, admitted as the best class among them.
	calls := f[groups.Detail].Calls()
	if got := len(calls); got != before+1 || len(calls[before].Targets) != 2 {
		t.Fatalf("%d calls after the due time, want one more with both entities: %+v", got, calls[before:])
	}
	if c := calls[before]; c.Class != governor.ClassQuery || c.Purpose != governor.PurposeRefresh {
		t.Errorf("background call = %+v, want a class 3 refresh (the best class of the batch)", c)
	}
}

func TestAClass2RefreshBooksTheNextCycleAndALaterClass2TaskIsLeftAlone(t *testing.T) {
	ctx := context.Background()
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	f := register(r, groups.Detail)
	seed(r, node, staleRow(r, node, groups.Detail, "old"))

	if err := r.Svc.QueueGroup(ctx, node, groups.Detail, governor.ClassInvalidated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	r.Sched.Drain(ctx)
	if f[groups.Detail].CallCount() != 1 {
		t.Fatalf("calls = %d", f[groups.Detail].CallCount())
	}
	if c, queued, running, _ := r.Svc.Inspect(ctx, node, groups.Detail); !queued || running || c != governor.ClassKeepAlive {
		t.Fatalf("after a class 2 fetch: class %v queued %v running %v, want the class 4 cycle", c, queued, running)
	}

	// While a refresh runs, a SECOND class 2 task is queued: its booking leaves it alone.
	release := f[groups.Detail].Block()
	t.Cleanup(release)
	if err := r.Svc.QueueGroup(ctx, node, groups.Detail, governor.ClassInvalidated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	r.Sched.Step(ctx)
	if !f[groups.Detail].WaitStarted(2, 10*time.Second) {
		t.Fatal("the second refresh never started")
	}
	if c, queued, running, _ := r.Svc.Inspect(ctx, node, groups.Detail); !running || queued || c != governor.ClassInvalidated {
		t.Fatalf("while running: class %v queued %v running %v", c, queued, running)
	}
	if err := r.Svc.QueueGroup(ctx, node, groups.Detail, governor.ClassInvalidated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	release()
	r.Sched.Wait()
	if c, queued, running, _ := r.Svc.Inspect(ctx, node, groups.Detail); !queued || running || c != governor.ClassInvalidated {
		t.Fatalf("after the run: class %v queued %v running %v, want the queued class 2 task untouched", c, queued, running)
	}
	// It still runs as class 2 (a class 2 task fetches although the row is fresh).
	r.Sched.Drain(ctx)
	if got := f[groups.Detail].CallCount(); got != 3 {
		t.Errorf("calls = %d, want 3", got)
	}
}

func TestClassScopingAClass2TaskFetchesAFreshRowAndAClass3TaskIsSkipped(t *testing.T) {
	ctx := context.Background()
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	f := register(r, groups.Detail)
	seed(r, node, freshRow(r, node, groups.Detail, "d"))

	if err := r.Svc.QueueGroup(ctx, node, groups.Detail, governor.ClassQuery, time.Time{}); err != nil {
		t.Fatal(err)
	}
	r.Sched.Drain(ctx)
	if n := f[groups.Detail].CallCount(); n != 0 {
		t.Fatalf("a class 3 task fetched a fresh row (%d calls)", n)
	}
	if err := r.Svc.QueueGroup(ctx, node, groups.Detail, governor.ClassInvalidated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	r.Sched.Drain(ctx)
	if n := f[groups.Detail].CallCount(); n != 1 {
		t.Fatalf("a class 2 task over a fresh row made %d calls, want 1", n)
	}
}

func TestRescheduleGroupReplacesBackgroundTasksButNeverClass1Or2(t *testing.T) {
	ctx := context.Background()
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	f := register(r, groups.Detail)
	r.PutQuery("mine", "PR_member")
	seed(r, node, staleRow(r, node, groups.Detail, "old"))
	r.SeedSummary("PR_member", r.Clock.Now())
	hour := r.Clock.Now().Add(time.Hour)

	// Nothing queued: one is queued, class 4 (class 3 for a member of a query).
	if err := r.Svc.RescheduleGroup(ctx, node, groups.Detail, hour); err != nil {
		t.Fatal(err)
	}
	if err := r.Svc.RescheduleGroup(ctx, "PR_member", groups.Detail, hour); err != nil {
		t.Fatal(err)
	}
	if c, queued, _, _ := r.Svc.Inspect(ctx, node, groups.Detail); !queued || c != governor.ClassKeepAlive {
		t.Errorf("class %v queued %v, want class 4", c, queued)
	}
	if c, queued, _, _ := r.Svc.Inspect(ctx, "PR_member", groups.Detail); !queued || c != governor.ClassQuery {
		t.Errorf("member: class %v queued %v, want class 3", c, queued)
	}
	// It replaces in full, a later due time included: nothing runs before the hour.
	r.Sched.Drain(ctx)
	if f[groups.Detail].CallCount() != 0 {
		t.Fatal("a task rescheduled an hour out ran now")
	}
	r.Clock.Advance(time.Hour + time.Second)
	r.Sched.Drain(ctx)
	if f[groups.Detail].TargetCount() != 2 {
		t.Fatalf("%d targets fetched after the due time, want 2", f[groups.Detail].TargetCount())
	}

	// A queued class 2 task is neither downgraded nor postponed.
	if err := r.Svc.QueueGroup(ctx, node, groups.Detail, governor.ClassInvalidated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := r.Svc.RescheduleGroup(ctx, node, groups.Detail, r.Clock.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if c, queued, _, _ := r.Svc.Inspect(ctx, node, groups.Detail); !queued || c != governor.ClassInvalidated {
		t.Fatalf("class %v queued %v, want the class 2 task kept", c, queued)
	}
	before := f[groups.Detail].CallCount()
	r.Sched.Drain(ctx)
	if f[groups.Detail].CallCount() != before+1 {
		t.Error("the class 2 task did not run now")
	}

	// A RUNNING class 2 task counts too: Inspect reports it and nothing is queued.
	release := f[groups.Detail].Block()
	t.Cleanup(release)
	if err := r.Svc.QueueGroup(ctx, node, groups.Detail, governor.ClassInvalidated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	r.Sched.Step(ctx)
	if !f[groups.Detail].WaitStarted(before+2, 10*time.Second) {
		t.Fatal("the refresh never started")
	}
	if err := r.Svc.RescheduleGroup(ctx, node, groups.Detail, r.Clock.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if c, queued, running, _ := r.Svc.Inspect(ctx, node, groups.Detail); !running || queued || c != governor.ClassInvalidated {
		t.Errorf("running class 2: class %v queued %v running %v", c, queued, running)
	}
	release()
	r.Sched.Wait()
}

func TestBackgroundBookingRules(t *testing.T) {
	ctx := context.Background()
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	register(r, groups.Detail, groups.Pending, groups.Files, groups.Commits)
	seed(r, node)
	r.Start()

	ensure(t, r, node, []string{groups.Detail, groups.Pending, groups.Files, groups.Commits}, groupsvc.EnsureOptions{})
	if _, queued, _, _ := r.Svc.Inspect(ctx, node, groups.Detail); !queued {
		t.Error("detail has no background task")
	}
	for _, g := range []string{groups.Pending, groups.Files, groups.Commits} {
		if c, queued, running, _ := r.Svc.Inspect(ctx, node, g); queued || running {
			t.Errorf("%s has a background task (class %v): pending is read through only, files and commits have no time limit", g, c)
		}
		if row, ok := r.Group(node, g); !ok || len(row.Content) == 0 {
			t.Errorf("%s was not fetched on read", g)
		}
	}
	// A refresh that leaves a stamped row without a fresh_until books nothing
	// either, whichever way it gets there.
	if err := r.Svc.QueueGroup(ctx, node, groups.Files, governor.ClassInvalidated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	r.Sched.Drain(ctx)
	if _, queued, _, _ := r.Svc.Inspect(ctx, node, groups.Files); queued {
		t.Error("a files refresh booked a time-based task")
	}
}

func TestAnEntityOutsideTheKeepAliveSetIsSkippedByTheExecutor(t *testing.T) {
	ctx := context.Background()
	r := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	f := register(r, groups.Detail)
	r.SeedSummary(node, r.Clock.Now().Add(-48*time.Hour)) // last access older than keepalive_window
	r.SeedGroup(staleRow(r, node, groups.Detail, "old"))

	if err := r.Svc.QueueGroup(ctx, node, groups.Detail, governor.ClassKeepAlive, time.Time{}); err != nil {
		t.Fatal(err)
	}
	r.Sched.Drain(ctx)
	if n := f[groups.Detail].CallCount(); n != 0 {
		t.Fatalf("a background task refreshed an entity outside the keep-alive set (%d calls)", n)
	}
	if r.Sched.Queued(node, groups.Detail) {
		t.Error("the skipped task was kept")
	}
	// An interactive read still fetches it (and keeps it alive).
	r.Start()
	ensure(t, r, node, []string{groups.Detail}, groupsvc.EnsureOptions{})
	if f[groups.Detail].CallCount() != 1 {
		t.Error("the interactive read did not fetch")
	}
}

func TestAfterARestartRebuildQueuesNoOriginCallForFreshGroupsAndCatchesUpTheRest(t *testing.T) {
	ctx := context.Background()
	r1 := groupsvctest.NewRig(t, groupsvctest.RigOptions{})
	register(r1, groups.Detail, groups.Conversation, groups.Runs, groups.Files)
	seed(r1, node, freshRow(r1, node, groups.Detail, "d"), staleRow(r1, node, groups.Runs, "r"), freshRow(r1, node, groups.Files, "f"))
	// conversation: no row at all. A second entity is outside the keep-alive set.
	r1.SeedSummary("PR_idle", r1.Clock.Now().Add(-72*time.Hour))
	r1.SeedGroup(staleRow(r1, "PR_idle", groups.Detail, "idle"))

	// The restart: a NEW service and scheduler over the same store and clock.
	r2 := groupsvctest.NewRig(t, groupsvctest.RigOptions{Store: r1.Store, Clock: r1.Clock})
	f := register(r2, groups.Detail, groups.Conversation, groups.Runs, groups.Files)
	if _, err := r2.Summary.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	n, err := r2.Svc.Rebuild(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// detail (due later), conversation (missing: due now), runs (stale: due now); files has
	// no time limit; PR_idle is outside the set.
	if n != 3 {
		t.Fatalf("Rebuild queued %d tasks, want 3", n)
	}
	r2.Sched.Drain(ctx)
	if f[groups.Detail].CallCount() != 0 || f[groups.Files].CallCount() != 0 {
		t.Errorf("fresh groups were fetched after the restart: detail %d, files %d", f[groups.Detail].CallCount(), f[groups.Files].CallCount())
	}
	if f[groups.Conversation].CallCount() != 1 || f[groups.Runs].CallCount() != 1 {
		t.Errorf("catch-up: conversation %d, runs %d, want 1 each", f[groups.Conversation].CallCount(), f[groups.Runs].CallCount())
	}
	if c := f[groups.Conversation].Calls()[0]; c.Class != governor.ClassKeepAlive || c.Purpose != governor.PurposeRefresh {
		t.Errorf("catch-up call = %+v, want a class 4 refresh", c)
	}
	// The booked cycle for detail is due at its fresh_until.
	r2.Clock.Advance(11 * time.Minute)
	r2.Sched.Drain(ctx)
	if f[groups.Detail].CallCount() != 1 {
		t.Errorf("detail calls = %d after its due time, want 1", f[groups.Detail].CallCount())
	}
	for _, c := range f[groups.Detail].Calls() {
		if c.Targets[0].NodeID == "PR_idle" {
			t.Error("an entity outside the keep-alive set was refreshed")
		}
	}
}

func TestAfterALongOutageTheCatchUpIsLimitedByTheGovernor(t *testing.T) {
	ctx := context.Background()
	const (
		entities = 30
		capPts   = 4
		batch    = 5
	)
	clk := governor.NewManualClock(groupsvctest.T0)
	gov := newGov(t, clk, capPts)
	r1 := groupsvctest.NewRig(t, groupsvctest.RigOptions{Clock: clk})
	ids := make([]string, entities)
	for i := range ids {
		ids[i] = "PR_" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		r1.SeedSummary(ids[i], clk.Now())
		// The summary stays fresh for the whole test: only the group is stale.
		sum, _ := r1.Group(ids[i], groups.Summary)
		sum.FreshUntil = clk.Now().Add(1000 * time.Hour)
		r1.SeedGroup(sum)
		r1.SeedGroup(staleRow(r1, ids[i], groups.Detail, "old"))
	}

	r2 := groupsvctest.NewRig(t, groupsvctest.RigOptions{Store: r1.Store, Clock: clk, Gov: gov, Workers: 4})
	f := groupsvctest.NewFetcher(groups.Detail)
	f.Batch, f.Gov = batch, gov
	r2.Register(f)
	if n, err := r2.Svc.Rebuild(ctx); err != nil || n != entities {
		t.Fatalf("Rebuild = %d, %v, want %d", n, err, entities)
	}
	r2.Sched.Drain(ctx)
	if got := f.CallCount(); got != capPts || spent(gov) != capPts {
		t.Fatalf("first window: %d calls, spent %d, want exactly the cap %d", got, spent(gov), capPts)
	}
	for window := 2; window <= 3; window++ {
		clk.Advance(time.Hour + time.Second)
		r2.Sched.Drain(ctx)
		if spent(gov) > capPts {
			t.Fatalf("window %d: spent %d past the cap", window, spent(gov))
		}
		if got := f.CallCount(); got != capPts*window {
			t.Fatalf("window %d: %d calls in all, want %d", window, got, capPts*window)
		}
	}
	for _, c := range f.Calls() {
		if len(c.Targets) > batch {
			t.Fatalf("a batch of %d targets exceeds the fetcher's maximum %d", len(c.Targets), batch)
		}
	}
	if f.TargetCount() != capPts*3*batch {
		t.Errorf("fetched %d targets in three windows, want %d", f.TargetCount(), capPts*3*batch)
	}
}
