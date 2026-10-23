package groupsvc

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupkit"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

const (
	entMember = "ent-member"
	ref1      = "Zone-A/Item-B~1"
)

// attrDescriber reads the invented primary content "<ref>|<flags>": the flag
// word "terminal" or "gone" sets the matching field; the stamps are fixed.
type attrDescriber struct{}

func (attrDescriber) Describe(content []byte) (engine.Primary, error) {
	ref, flag, _ := strings.Cut(string(content), "|")
	return engine.Primary{
		Ref: ref, Stamp: "s1", Stamp2: "s2", Terminal: flag == "terminal", Gone: flag == "gone",
		Attrs: map[string]string{"label": "lbl-" + ref},
	}, nil
}

// execRig is a rig whose scheduler runs the executors of the three non-primary
// groups over scripted fetchers, with a scripted error policy and identity
// marker.
type execRig struct {
	*rig
	fetch  map[string]*scriptFetcher
	errs   *fakeErrors
	marker *authMarker
}

// newExecRig builds a rig over spec (and st and clk, both nil for fresh ones)
// and wires the executors of the three non-primary groups into its scheduler.
func newExecRig(t *testing.T, spec registry.Spec, st *store.Store, clk *governor.ManualClock) *execRig {
	t.Helper()
	r := newRig(t, spec, st, clk)
	x := &execRig{rig: r, fetch: map[string]*scriptFetcher{}, errs: &fakeErrors{}, marker: &authMarker{}}
	r.svc.cfg.Describer = attrDescriber{}
	r.svc.cfg.Errors = x.errs
	r.svc.cfg.Identity = x.marker
	r.svc.cfg.Governor = new(governor.Governor)
	r.svc.cfg.FallbackPause = 3 * time.Minute
	for _, g := range []string{groupSide, groupStamped, groupPassive} {
		f := newScriptFetcher(g)
		x.fetch[g] = f
		r.svc.fetchers[g] = f
		r.sched.Register(g, &executor{s: r.svc, f: f})
	}
	return x
}

func (r *rig) entity(id string) store.EntityRow {
	r.t.Helper()
	e, _, err := r.st.GetEntity(context.Background(), r.kind(), id)
	if err != nil {
		r.t.Fatal(err)
	}
	return e
}

// hookLog records OnGroupRefreshed calls.
type hookLog struct {
	mu    sync.Mutex
	calls []hookCall
}

type hookCall struct {
	prev *store.GroupRow
	next store.GroupRow
	info RefreshInfo
}

func (h *hookLog) fire(prev *store.GroupRow, next store.GroupRow, info RefreshInfo) {
	h.mu.Lock()
	h.calls = append(h.calls, hookCall{prev, next, info})
	h.mu.Unlock()
}

func (h *hookLog) all() []hookCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]hookCall(nil), h.calls...)
}

// events records the order of hook calls.
type events struct {
	mu  sync.Mutex
	log []string
}

func (e *events) add(s string) {
	e.mu.Lock()
	e.log = append(e.log, s)
	e.mu.Unlock()
}

func (e *events) all() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.log...)
}

// runInteractive queues the group as class 1, runs the scheduler once and
// returns the task's result (what the executor reported for it).
func runInteractive(t *testing.T, r *execRig, id, group string) scheduler.Result {
	t.Helper()
	ctx := context.Background()
	f, err := r.sched.SubmitInteractive(scheduler.Task{Kind: group, Entity: id, Group: group})
	if err != nil {
		t.Fatal(err)
	}
	r.sched.Step(ctx)
	r.sched.Wait()
	select {
	case <-f.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the task never finished")
	}
	return f.Result()
}

func TestARefreshThatBeganBeforeAnInvalidationStoresNothingAndFiresNoHook(t *testing.T) {
	ctx := context.Background()
	for _, spec := range []registry.Spec{specAlpha, specBeta} {
		t.Run(spec.EntityKind, func(t *testing.T) {
			r := newExecRig(t, spec, nil, nil)
			hooks := &hookLog{}
			r.svc.cfg.OnGroupRefreshed = hooks.fire
			f := r.fetch[groupSide]
			r.seedPrimary(ent1, ref1)
			r.keep.keep(ent1)
			r.seedGroup(staleGroup(r.rig, ent1, groupSide, "old"))
			release := f.block()
			t.Cleanup(release)

			if err := r.svc.QueueGroup(ctx, ent1, groupSide, governor.ClassInvalidated, time.Time{}); err != nil {
				t.Fatal(err)
			}
			r.sched.Step(ctx)
			if !f.waitStarted(1, 10*time.Second) {
				t.Fatal("the refresh never started")
			}
			// A write lands while the fetch is in flight.
			if err := groupkit.Invalidate(ctx, r.st, r.kind(), ent1, groupSide, r.clk.Now()); err != nil {
				t.Fatal(err)
			}
			release()
			r.sched.Wait()

			row, _ := r.group(ent1, groupSide)
			if string(row.Content) != "old" || row.Generation != 2 {
				t.Fatalf("row = %+v, want the old content at the invalidation's generation (the result is discarded)", row)
			}
			if n := len(hooks.all()); n != 0 {
				t.Fatalf("OnGroupRefreshed fired %d times for a superseded refresh", n)
			}
			if n := r.svc.commitCount(ent1, groupSide); n != 0 {
				t.Errorf("commitCount = %d after a superseded refresh, want 0", n)
			}
			// The row is due now, so the cycle refetches it, and that one is stored.
			r.sched.Drain(ctx)
			row, _ = r.group(ent1, groupSide)
			if string(row.Content) == "old" || row.Generation != 3 {
				t.Fatalf("after the retry the row = %+v", row)
			}
			if n := len(hooks.all()); n != 1 {
				t.Errorf("OnGroupRefreshed fired %d times, want 1", n)
			}
			if n := r.svc.commitCount(ent1, groupSide); n != 1 {
				t.Errorf("commitCount = %d after one stored refresh, want 1", n)
			}
		})
	}
}

func TestAClass2RefreshBooksTheNextCycleAndALaterClass2TaskIsLeftAlone(t *testing.T) {
	ctx := context.Background()
	r := newExecRig(t, specAlpha, nil, nil)
	f := r.fetch[groupSide]
	r.seedPrimary(ent1, ref1)
	r.keep.keep(ent1)
	r.seedGroup(staleGroup(r.rig, ent1, groupSide, "old"))

	if err := r.svc.QueueGroup(ctx, ent1, groupSide, governor.ClassInvalidated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	r.sched.Drain(ctx)
	if f.callCount() != 1 {
		t.Fatalf("calls = %d", f.callCount())
	}
	if c, queued, running, _ := r.svc.Inspect(ctx, ent1, groupSide); !queued || running || c != governor.ClassKeepAlive {
		t.Fatalf("after a class 2 fetch: class %v queued %v running %v, want the class 4 cycle", c, queued, running)
	}

	// While a refresh runs, a SECOND class 2 task is queued: its booking leaves it alone.
	release := f.block()
	t.Cleanup(release)
	if err := r.svc.QueueGroup(ctx, ent1, groupSide, governor.ClassInvalidated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	r.sched.Step(ctx)
	if !f.waitStarted(2, 10*time.Second) {
		t.Fatal("the second refresh never started")
	}
	if c, queued, running, _ := r.svc.Inspect(ctx, ent1, groupSide); !running || queued || c != governor.ClassInvalidated {
		t.Fatalf("while running: class %v queued %v running %v", c, queued, running)
	}
	if err := r.svc.QueueGroup(ctx, ent1, groupSide, governor.ClassInvalidated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	release()
	r.sched.Wait()
	if c, queued, running, _ := r.svc.Inspect(ctx, ent1, groupSide); !queued || running || c != governor.ClassInvalidated {
		t.Fatalf("after the run: class %v queued %v running %v, want the queued class 2 task untouched", c, queued, running)
	}
	// It still runs as class 2 (a class 2 task fetches although the row is fresh).
	r.sched.Drain(ctx)
	if got := f.callCount(); got != 3 {
		t.Errorf("calls = %d, want 3", got)
	}
}

func TestClassScopingAClass2TaskFetchesAFreshRowAndAClass3TaskIsSkipped(t *testing.T) {
	ctx := context.Background()
	r := newExecRig(t, specAlpha, nil, nil)
	f := r.fetch[groupSide]
	r.seedPrimary(ent1, ref1)
	r.keep.keep(ent1)
	r.seedGroup(freshGroup(r.rig, ent1, groupSide, "d"))

	if err := r.svc.QueueGroup(ctx, ent1, groupSide, governor.ClassQuery, time.Time{}); err != nil {
		t.Fatal(err)
	}
	r.sched.Drain(ctx)
	if n := f.callCount(); n != 0 {
		t.Fatalf("a class 3 task fetched a fresh row (%d calls)", n)
	}
	if err := r.svc.QueueGroup(ctx, ent1, groupSide, governor.ClassInvalidated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	r.sched.Drain(ctx)
	if n := f.callCount(); n != 1 {
		t.Fatalf("a class 2 task over a fresh row made %d calls, want 1", n)
	}
}

func TestRescheduleGroupReplacesBackgroundTasksButNeverClass1Or2(t *testing.T) {
	ctx := context.Background()
	r := newExecRig(t, specAlpha, nil, nil)
	f := r.fetch[groupSide]
	r.putQuery("mine", entMember)
	r.seedPrimary(ent1, ref1)
	r.keep.keep(ent1)
	r.seedGroup(staleGroup(r.rig, ent1, groupSide, "old"))
	r.seedPrimary(entMember, "Zone-A/Item-C~2")
	hour := r.clk.Now().Add(time.Hour)

	// Nothing queued: one is queued, class 4 (class 3 for a member of a query).
	if err := r.svc.RescheduleGroup(ctx, ent1, groupSide, hour); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.RescheduleGroup(ctx, entMember, groupSide, hour); err != nil {
		t.Fatal(err)
	}
	if c, queued, _, _ := r.svc.Inspect(ctx, ent1, groupSide); !queued || c != governor.ClassKeepAlive {
		t.Errorf("class %v queued %v, want class 4", c, queued)
	}
	if c, queued, _, _ := r.svc.Inspect(ctx, entMember, groupSide); !queued || c != governor.ClassQuery {
		t.Errorf("member: class %v queued %v, want class 3", c, queued)
	}
	// It replaces in full, a later due time included: nothing runs before the hour.
	r.sched.Drain(ctx)
	if f.callCount() != 0 {
		t.Fatal("a task rescheduled an hour out ran now")
	}
	r.clk.Advance(time.Hour + time.Second)
	r.sched.Drain(ctx)
	if f.targetCount() != 2 {
		t.Fatalf("%d targets fetched after the due time, want 2", f.targetCount())
	}

	// A queued class 2 task is neither downgraded nor postponed.
	if err := r.svc.QueueGroup(ctx, ent1, groupSide, governor.ClassInvalidated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.RescheduleGroup(ctx, ent1, groupSide, r.clk.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if c, queued, _, _ := r.svc.Inspect(ctx, ent1, groupSide); !queued || c != governor.ClassInvalidated {
		t.Fatalf("class %v queued %v, want the class 2 task kept", c, queued)
	}
	before := f.callCount()
	r.sched.Drain(ctx)
	if f.callCount() != before+1 {
		t.Error("the class 2 task did not run now")
	}

	// A RUNNING class 2 task counts too: Inspect reports it and nothing is queued.
	release := f.block()
	t.Cleanup(release)
	if err := r.svc.QueueGroup(ctx, ent1, groupSide, governor.ClassInvalidated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	r.sched.Step(ctx)
	if !f.waitStarted(before+2, 10*time.Second) {
		t.Fatal("the refresh never started")
	}
	if err := r.svc.RescheduleGroup(ctx, ent1, groupSide, r.clk.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if c, queued, running, _ := r.svc.Inspect(ctx, ent1, groupSide); !running || queued || c != governor.ClassInvalidated {
		t.Errorf("running class 2: class %v queued %v running %v", c, queued, running)
	}
	release()
	r.sched.Wait()
}

func TestAnEntityOutsideTheKeepAliveSetIsSkippedByTheExecutor(t *testing.T) {
	ctx := context.Background()
	r := newExecRig(t, specAlpha, nil, nil)
	f := r.fetch[groupSide]
	r.seedEntity(ent1, r.clk.Now().Add(-48*time.Hour)) // last access older than the keep-alive window
	r.seedGroup(store.GroupRow{ID: ent1, Group: r.svc.cfg.Spec.PrimaryGroup, Content: []byte(ref1), Generation: 1})
	r.seedGroup(staleGroup(r.rig, ent1, groupSide, "old"))

	if err := r.svc.QueueGroup(ctx, ent1, groupSide, governor.ClassKeepAlive, time.Time{}); err != nil {
		t.Fatal(err)
	}
	r.sched.Drain(ctx)
	if n := f.callCount(); n != 0 {
		t.Fatalf("a background task refreshed an entity outside the keep-alive set (%d calls)", n)
	}
	if r.sched.Queued(ent1, groupSide) {
		t.Error("the skipped task was kept")
	}
	// An interactive read still fetches it.
	if res := runInteractive(t, r, ent1, groupSide); res.Err != nil {
		t.Fatal(res.Err)
	}
	if f.callCount() != 1 {
		t.Error("the interactive read did not fetch")
	}
}

func TestAfterARestartRebuildQueuesNoOriginCallForFreshGroupsAndCatchesUpTheRest(t *testing.T) {
	ctx := context.Background()
	const (
		entFresh, entMissing, entStale, entIdle = "ent-fresh", "ent-missing", "ent-stale", "ent-idle"
	)
	r1 := newExecRig(t, specAlpha, nil, nil)
	for _, id := range []string{entFresh, entMissing, entStale} {
		r1.seedPrimary(id, ref1)
	}
	r1.seedGroup(freshGroup(r1.rig, entFresh, groupSide, "d"))
	r1.seedGroup(freshGroup(r1.rig, entFresh, groupStamped, "f"))
	r1.seedGroup(staleGroup(r1.rig, entStale, groupSide, "r"))
	// entMissing has no row at all. A fourth entity is outside the keep-alive set.
	r1.seedEntity(entIdle, r1.clk.Now().Add(-72*time.Hour))
	r1.seedGroup(store.GroupRow{ID: entIdle, Group: r1.svc.cfg.Spec.PrimaryGroup, Content: []byte(ref1), Generation: 1})
	r1.seedGroup(staleGroup(r1.rig, entIdle, groupSide, "idle"))

	// The restart: a NEW service and scheduler over the same store and clock.
	r2 := newExecRig(t, specAlpha, r1.st, r1.clk)
	r2.keep.keep(entFresh, entMissing, entStale)
	f := r2.fetch[groupSide]
	n, err := r2.svc.Rebuild(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The side row of entFresh (due later), the missing one (due now) and the stale
	// one (due now); the stamped group has no time limit; entIdle is outside the set.
	if n != 3 {
		t.Fatalf("Rebuild queued %d tasks, want 3", n)
	}
	r2.sched.Drain(ctx)
	calls := f.callList()
	if len(calls) != 1 || len(calls[0].Targets) != 2 {
		t.Fatalf("catch-up calls = %+v, want one call for the missing and the stale entity", calls)
	}
	if c := calls[0].Call; c.Class != governor.ClassKeepAlive || c.Purpose != governor.PurposeRefresh {
		t.Errorf("catch-up call = %+v, want a class 4 refresh", c)
	}
	if got := r2.fetch[groupStamped].callCount(); got != 0 {
		t.Errorf("the stamped group was fetched after the restart (%d calls)", got)
	}
	// The booked cycle for entFresh is due at its fresh_until.
	r2.clk.Advance(11 * time.Minute)
	r2.sched.Drain(ctx)
	fetched := map[string]bool{}
	for _, c := range f.callList() {
		for _, tg := range c.Targets {
			fetched[tg.ID] = true
		}
	}
	if !fetched[entFresh] {
		t.Errorf("entFresh was not refreshed after its due time: %v", fetched)
	}
	if fetched[entIdle] {
		t.Error("an entity outside the keep-alive set was refreshed")
	}
}

func TestOnGroupStoringRunsOnceInsideTheTransactionBeforeOnGroupRefreshed(t *testing.T) {
	for _, spec := range []registry.Spec{specAlpha, specBeta} {
		t.Run(spec.EntityKind, func(t *testing.T) {
			r := newExecRig(t, spec, nil, nil)
			ev := &events{}
			var gotPrev *store.GroupRow
			var gotNext store.GroupRow
			var gotInfo RefreshInfo
			r.svc.cfg.OnGroupRefreshed = func(*store.GroupRow, store.GroupRow, RefreshInfo) { ev.add("refreshed") }
			r.svc.cfg.OnGroupStoring = func(ctx context.Context, tx *store.Tx, prev *store.GroupRow, next store.GroupRow, info RefreshInfo) error {
				ev.add("storing")
				gotPrev, gotNext, gotInfo = prev, next, info
				// Only the transaction's own methods: this would deadlock on the store.
				row, ok, err := tx.GetGroup(ctx, spec.EntityKind, next.ID, next.Group)
				if err != nil || !ok || row.Generation != next.Generation || string(row.Content) != string(next.Content) {
					t.Errorf("inside the transaction the row = %+v ok=%v err=%v, want next", row, ok, err)
				}
				if _, err := tx.BumpEntityVersion(ctx, spec.EntityKind, next.ID); err != nil {
					t.Errorf("a hook writing through tx: %v", err)
				}
				return nil
			}
			r.seedPrimary(ent1, ref1)
			r.seedGroup(staleGroup(r.rig, ent1, groupSide, "old"))

			if res := runInteractive(t, r, ent1, groupSide); res.Err != nil {
				t.Fatal(res.Err)
			}
			if got := ev.all(); len(got) != 2 || got[0] != "storing" || got[1] != "refreshed" {
				t.Fatalf("events = %v, want storing once, then refreshed once", got)
			}
			if gotPrev == nil || string(gotPrev.Content) != "old" || gotNext.Generation != 2 || gotNext.Group != groupSide {
				t.Errorf("prev = %+v, next = %+v", gotPrev, gotNext)
			}
			if gotInfo.Class != governor.ClassInteractive || gotInfo.Purpose != governor.PurposeInteractive {
				t.Errorf("info = %+v", gotInfo)
			}
			// What the hook wrote through tx committed with the group.
			if e := r.entity(ent1); e.Version != 1 {
				t.Errorf("entity version = %d, want the hook's bump to have committed", e.Version)
			}
		})
	}
}

func TestASupersededRefreshCallsNeitherOnGroupStoringNorOnGroupRefreshed(t *testing.T) {
	ctx := context.Background()
	r := newExecRig(t, specAlpha, nil, nil)
	ev := &events{}
	r.svc.cfg.OnGroupRefreshed = func(*store.GroupRow, store.GroupRow, RefreshInfo) { ev.add("refreshed") }
	r.svc.cfg.OnGroupStoring = func(context.Context, *store.Tx, *store.GroupRow, store.GroupRow, RefreshInfo) error {
		ev.add("storing")
		return nil
	}
	f := r.fetch[groupSide]
	r.seedPrimary(ent1, ref1)
	r.seedGroup(staleGroup(r.rig, ent1, groupSide, "old"))
	release := f.block()
	t.Cleanup(release)

	if err := r.svc.QueueGroup(ctx, ent1, groupSide, governor.ClassInvalidated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	r.sched.Step(ctx)
	if !f.waitStarted(1, 10*time.Second) {
		t.Fatal("the refresh never started")
	}
	if err := groupkit.Invalidate(ctx, r.st, r.kind(), ent1, groupSide, r.clk.Now()); err != nil {
		t.Fatal(err)
	}
	release()
	r.sched.Wait()

	if got := ev.all(); len(got) != 0 {
		t.Fatalf("hooks ran for a superseded refresh: %v", got)
	}
	if row, _ := r.group(ent1, groupSide); string(row.Content) != "old" {
		t.Errorf("row = %+v, want the old content", row)
	}
}

func TestAnOnGroupStoringErrorRollsTheWriteBackAndFailsTheTask(t *testing.T) {
	boom := errors.New("boom")
	r := newExecRig(t, specAlpha, nil, nil)
	ev := &events{}
	r.svc.cfg.OnGroupRefreshed = func(*store.GroupRow, store.GroupRow, RefreshInfo) { ev.add("refreshed") }
	r.svc.cfg.OnGroupStoring = func(ctx context.Context, tx *store.Tx, _ *store.GroupRow, next store.GroupRow, _ RefreshInfo) error {
		// A write through tx before failing: it must roll back with the group.
		if _, err := tx.BumpEntityVersion(ctx, r.kind(), next.ID); err != nil {
			return err
		}
		return boom
	}
	r.seedPrimary(ent1, ref1)
	r.seedGroup(staleGroup(r.rig, ent1, groupSide, "old"))
	before, _ := r.group(ent1, groupSide)

	res := runInteractive(t, r, ent1, groupSide)
	if !errors.Is(res.Err, boom) {
		t.Fatalf("task result error = %v, want the hook's error", res.Err)
	}
	after, _ := r.group(ent1, groupSide)
	if string(after.Content) != "old" || after.Generation != before.Generation {
		t.Errorf("row = %+v, want the previous row in place (was %+v)", after, before)
	}
	if e := r.entity(ent1); e.Version != 0 {
		t.Errorf("entity version = %d: the hook's own write must roll back too", e.Version)
	}
	if got := ev.all(); len(got) != 0 {
		t.Errorf("OnGroupRefreshed ran after a failed store: %v", got)
	}
}

// goneCall is one OnGone call.
type goneCall struct {
	id      string
	purpose governor.Purpose
}

func TestAGroupFetchThatSaysGoneCallsOnGoneOnceWithItsPurposeAndNotAgain(t *testing.T) {
	r := newExecRig(t, specAlpha, nil, nil)
	var mu sync.Mutex
	var calls []goneCall
	r.svc.cfg.OnGone = func(ctx context.Context, tx *store.Tx, id string, p governor.Purpose) error {
		// Inside the transaction the entity is already tombstoned.
		e, ok, err := tx.GetEntity(ctx, r.kind(), id)
		if err != nil || !ok || e.TombstonedAt.IsZero() {
			t.Errorf("inside the transaction the entity = %+v ok=%v err=%v", e, ok, err)
		}
		mu.Lock()
		calls = append(calls, goneCall{id, p})
		mu.Unlock()
		return nil
	}
	r.seedPrimary(ent1, ref1)
	r.fetch[groupSide].setGone(ent1, true)

	if res := runInteractive(t, r, ent1, groupSide); res.Err != nil {
		t.Fatal(res.Err)
	}
	if e := r.entity(ent1); e.TombstonedAt.IsZero() {
		t.Fatal("the entity was not tombstoned")
	}
	mu.Lock()
	if len(calls) != 1 || calls[0] != (goneCall{ent1, governor.PurposeInteractive}) {
		t.Fatalf("OnGone calls = %+v, want one for the entity with the interactive purpose", calls)
	}
	mu.Unlock()

	// A second tombstone of the same entity does not call the hook.
	if res := runInteractive(t, r, ent1, groupSide); res.Err != nil {
		t.Fatal(res.Err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Errorf("OnGone calls after a repeated tombstone = %+v, want still one", calls)
	}
}

func TestAnOnGoneErrorLeavesTheEntityVisibleAndFailsTheTask(t *testing.T) {
	boom := errors.New("boom")
	r := newExecRig(t, specAlpha, nil, nil)
	r.svc.cfg.OnGone = func(context.Context, *store.Tx, string, governor.Purpose) error { return boom }
	r.seedPrimary(ent1, ref1)
	r.fetch[groupSide].setGone(ent1, true)

	res := runInteractive(t, r, ent1, groupSide)
	if !errors.Is(res.Err, boom) {
		t.Fatalf("task result error = %v, want the hook's error", res.Err)
	}
	if e := r.entity(ent1); !e.TombstonedAt.IsZero() {
		t.Errorf("entity = %+v, want it still visible", e)
	}
}

func TestAGoneAnswerCancelsTheQueuedTasksOfTheEntityAndLeavesItsStoredGroups(t *testing.T) {
	ctx := context.Background()
	r := newExecRig(t, specAlpha, nil, nil)
	r.keep.keep(ent1)
	r.seedPrimary(ent1, ref1)
	r.seedGroup(freshGroup(r.rig, ent1, groupStamped, "c"))
	r.fetch[groupSide].setGone(ent1, true)
	// A background task of another group is queued for the entity.
	if err := r.svc.QueueGroup(ctx, ent1, groupStamped, governor.ClassKeepAlive, r.clk.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	if res := runInteractive(t, r, ent1, groupSide); res.Err != nil {
		t.Fatal(res.Err)
	}
	if e := r.entity(ent1); e.TombstonedAt.IsZero() || !e.TombstonedAt.Equal(r.clk.Now()) {
		t.Errorf("entity = %+v, want TombstonedAt = now", e)
	}
	if r.sched.Queued(ent1, groupStamped) {
		t.Error("a queued task of the tombstoned entity survived")
	}
	if row, ok := r.group(ent1, groupStamped); !ok || string(row.Content) != "c" {
		t.Errorf("stored groups must be left for expiry: %+v %v", row, ok)
	}
	// A background refresh of a tombstoned entity is skipped.
	if err := r.svc.QueueGroup(ctx, ent1, groupSide, governor.ClassKeepAlive, time.Time{}); err != nil {
		t.Fatal(err)
	}
	before := r.fetch[groupSide].callCount()
	r.sched.Drain(ctx)
	if r.fetch[groupSide].callCount() != before {
		t.Error("a background task fetched a tombstoned entity")
	}
}

func TestTheOriginCallIsAdmittedAsACallMetaOverTargetsBuiltFromThePrimaryContent(t *testing.T) {
	r := newExecRig(t, specAlpha, nil, nil)
	f := r.fetch[groupSide]
	r.seedPrimary(ent1, ref1+"|terminal")

	if res := runInteractive(t, r, ent1, groupSide); res.Err != nil {
		t.Fatal(res.Err)
	}
	calls := f.callList()
	if len(calls) != 1 {
		t.Fatalf("calls = %+v, want one", calls)
	}
	wantMeta := CallMeta{Class: governor.ClassInteractive, Purpose: governor.PurposeInteractive, Group: groupSide, BatchSize: 1}
	if calls[0].Call != wantMeta {
		t.Errorf("call meta = %+v, want %+v", calls[0].Call, wantMeta)
	}
	if len(calls[0].Targets) != 1 {
		t.Fatalf("targets = %+v, want one", calls[0].Targets)
	}
	tg := calls[0].Targets[0]
	if tg.ID != ent1 || tg.Ref != ref1 || tg.Stamp != "s1" || tg.Stamp2 != "s2" || !tg.Terminal || tg.Attrs["label"] != "lbl-"+ref1 {
		t.Errorf("target = %+v", tg)
	}
}

func TestAPrimaryWithNoReferenceOrMarkedGoneFailsItsTargetAndIsNotFetched(t *testing.T) {
	for _, content := range []string{"|plain", ref1 + "|gone"} {
		t.Run(content, func(t *testing.T) {
			r := newExecRig(t, specAlpha, nil, nil)
			r.seedPrimary(ent1, content)

			res := runInteractive(t, r, ent1, groupSide)
			want := "groupsvc: " + ent1 + ": " + errNoReference.Error()
			if res.Err == nil || res.Err.Error() != want {
				t.Fatalf("task result error = %v, want %q", res.Err, want)
			}
			if !errors.Is(res.Err, errNoReference) {
				t.Errorf("error %v does not wrap the no-reference sentinel", res.Err)
			}
			if n := r.fetch[groupSide].callCount(); n != 0 {
				t.Errorf("a target with no usable reference was fetched (%d calls)", n)
			}
		})
	}
}

func TestOnePerTargetFailureDoesNotStopTheRestOfTheBatch(t *testing.T) {
	ctx := context.Background()
	r := newExecRig(t, specAlpha, nil, nil)
	r.keep.keep(ent1, "ent-2")
	r.seedPrimary(ent1, "|plain")
	r.seedPrimary("ent-2", ref1)

	f1, err := r.sched.SubmitInteractive(scheduler.Task{Kind: groupSide, Entity: ent1, Group: groupSide})
	if err != nil {
		t.Fatal(err)
	}
	f2, err := r.sched.SubmitInteractive(scheduler.Task{Kind: groupSide, Entity: "ent-2", Group: groupSide})
	if err != nil {
		t.Fatal(err)
	}
	r.sched.Step(ctx)
	r.sched.Wait()
	<-f1.Done()
	<-f2.Done()
	if f1.Result().Err == nil {
		t.Error("the target with no reference did not fail")
	}
	if err := f2.Result().Err; err != nil {
		t.Errorf("the other target failed: %v", err)
	}
	if _, ok := r.group("ent-2", groupSide); !ok {
		t.Error("the other target was not stored")
	}
}

func TestARateLimitAnswerIsReadThroughTheErrorPolicyAndIsNotAnAuthenticationFailure(t *testing.T) {
	r := newExecRig(t, specAlpha, nil, nil)
	f := r.fetch[groupSide]
	r.seedPrimary(ent1, ref1)
	r.seedGroup(staleGroup(r.rig, ent1, groupSide, "old"))
	// The origin turns every refusal into an authentication error: the text tells.
	f.failWith(errors.Join(errors.New("quota exceeded"), errTestAuth))

	res := runInteractive(t, r, ent1, groupSide)
	if !errors.Is(res.Err, errTestRateLimited) || errors.Is(res.Err, errTestAuth) {
		t.Fatalf("task result error = %v, want a rate-limit error that is not an authentication failure", res.Err)
	}
	cs := r.errs.calls()
	if len(cs) != 1 || cs[0].gov != r.svc.cfg.Governor || cs[0].pause != 3*time.Minute {
		t.Fatalf("Classify calls = %+v, want one with the configured governor and fallback pause", cs)
	}
	if marks, _ := r.marker.counts(); marks != 0 {
		t.Errorf("MarkUnauthenticated called %d times for a rate limit", marks)
	}
	if row, _ := r.group(ent1, groupSide); row.LastError == "" || string(row.Content) != "old" {
		t.Errorf("row = %+v, want last_error recorded and the content kept", row)
	}
}

func TestARefusalOfTheCredentialsMarksTheIdentityAndASuccessClearsIt(t *testing.T) {
	r := newExecRig(t, specAlpha, nil, nil)
	f := r.fetch[groupSide]
	r.seedPrimary(ent1, ref1)
	r.seedGroup(staleGroup(r.rig, ent1, groupSide, "old"))
	f.failWith(errTestAuth)

	if res := runInteractive(t, r, ent1, groupSide); !errors.Is(res.Err, errTestAuth) {
		t.Fatalf("task result error = %v, want the authentication error", res.Err)
	}
	if marks, clears := r.marker.counts(); marks != 1 || clears != 0 {
		t.Fatalf("marks %d, clears %d, want 1 and 0", marks, clears)
	}
	f.failWith(nil)
	if res := runInteractive(t, r, ent1, groupSide); res.Err != nil {
		t.Fatal(res.Err)
	}
	if marks, clears := r.marker.counts(); marks != 1 || clears != 1 {
		t.Fatalf("marks %d, clears %d after a success, want 1 and 1", marks, clears)
	}
}

func TestAGovernorRefusalIsNotRecordedAsTheRowsLastError(t *testing.T) {
	r := newExecRig(t, specAlpha, nil, nil)
	r.seedPrimary(ent1, ref1)
	r.seedGroup(staleGroup(r.rig, ent1, groupSide, "old"))
	refusal := &governor.Refusal{Reason: "test: the budget says no"}
	r.fetch[groupSide].failWith(refusal)

	res := runInteractive(t, r, ent1, groupSide)
	if !errors.Is(res.Err, governor.ErrRefused) {
		t.Fatalf("task result error = %v, want the refusal", res.Err)
	}
	if row, _ := r.group(ent1, groupSide); row.LastError != "" {
		t.Errorf("last_error = %q, a refusal is the budget speaking and is not recorded", row.LastError)
	}
	if marks, _ := r.marker.counts(); marks != 0 {
		t.Errorf("MarkUnauthenticated called %d times for a refusal", marks)
	}
}

func TestAFetcherThatReturnsNoContentOrNoAnswerIsAFailureThatKeepsTheOldRow(t *testing.T) {
	r := newExecRig(t, specAlpha, nil, nil)
	f := r.fetch[groupSide]
	r.seedPrimary(ent1, ref1)
	r.seedGroup(staleGroup(r.rig, ent1, groupSide, "old"))
	f.setOutcome(func(Target, int) Outcome { return Outcome{} })

	res := runInteractive(t, r, ent1, groupSide)
	want := "groupsvc: the " + groupSide + " fetcher returned no content for " + ent1
	if res.Err == nil || res.Err.Error() != want {
		t.Fatalf("task result error = %v, want %q", res.Err, want)
	}
	if row, _ := r.group(ent1, groupSide); string(row.Content) != "old" || row.LastError != want {
		t.Errorf("row = %+v, want the old content with the failure as last_error", row)
	}
}

func TestOutcomeFreshUntilAndStampsOverrideTheRulesAndAnEmptyOneTakesTheTargetsOwn(t *testing.T) {
	r := newExecRig(t, specAlpha, nil, nil)
	f := r.fetch[groupSide]
	r.seedPrimary(ent1, ref1)
	until := r.clk.Now().Add(7 * time.Minute)
	f.setOutcome(func(tg Target, _ int) Outcome {
		return Outcome{Content: []byte("side"), FreshUntil: until, Stamp: "stamp-x", Stamp2: tg.Stamp2, LastError: "non-fatal"}
	})

	if res := runInteractive(t, r, ent1, groupSide); res.Err != nil {
		t.Fatal(res.Err)
	}
	row, _ := r.group(ent1, groupSide)
	a, b := row.Stamps()
	if !row.FreshUntil.Equal(until) || a != "stamp-x" || b != "s2" || row.Fingerprint != groupkit.Fingerprint([]byte("side")) || row.LastError != "non-fatal" {
		t.Errorf("row = %+v, stamps = %q %q", row, a, b)
	}

	// An outcome that names no stamps stores the target's own.
	f.setOutcome(func(Target, int) Outcome { return Outcome{Content: []byte("side-2")} })
	if res := runInteractive(t, r, ent1, groupSide); res.Err != nil {
		t.Fatal(res.Err)
	}
	row, _ = r.group(ent1, groupSide)
	if a, b := row.Stamps(); a != "s1" || b != "s2" {
		t.Errorf("stamps = %q %q, want the target's s1 s2", a, b)
	}
	if r.svc.commitCount(ent1, groupSide) != 2 {
		t.Errorf("commitCount = %d, want 2", r.svc.commitCount(ent1, groupSide))
	}
}
