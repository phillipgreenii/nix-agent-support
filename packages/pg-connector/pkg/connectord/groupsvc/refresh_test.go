package groupsvc

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
)

func TestOnGroupRefreshedGetsThePreviousRowTheNewOneAndHowItWasFetched(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		ctx := context.Background()
		x := newEnsureRig(t, spec)
		hooks := &hookLog{}
		x.svc.cfg.OnGroupRefreshed = hooks.fire
		x.keep.keep(ent1)
		x.seedPrim(ent1, true)
		x.start()

		x.ensure(ent1, []string{groupSide}, EnsureOptions{})
		calls := hooks.all()
		if len(calls) != 1 || calls[0].prev != nil {
			t.Fatalf("first fetch hook calls = %+v, want one with no previous row", calls)
		}
		c := calls[0]
		if c.info.Class != governor.ClassInteractive || c.info.Purpose != governor.PurposeInteractive || c.info.Started.IsZero() {
			t.Errorf("info = %+v", c.info)
		}
		if c.next.Group != groupSide || c.next.ID != ent1 || c.next.Generation != 1 || len(c.next.Content) == 0 {
			t.Errorf("next = %+v", c.next)
		}

		if err := x.svc.QueueGroup(ctx, ent1, groupSide, governor.ClassInvalidated, time.Time{}); err != nil {
			t.Fatal(err)
		}
		eventuallyTrue(t, "the class 2 refresh", func() bool { return len(hooks.all()) == 2 })
		c = hooks.all()[1]
		if c.prev == nil || string(c.prev.Content) != string(hooks.all()[0].next.Content) || c.info.Class != governor.ClassInvalidated {
			t.Errorf("second call = %+v, want the first fetch's row as prev and class 2", c)
		}
	})
}

func TestEachClass1And2FetchBooksTheNextBackgroundTaskAtTheNewFreshUntil(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		ctx := context.Background()
		x := newEnsureRig(t, spec)
		const member = "ent-member"
		f := x.fetch[groupSide]
		x.keep.keep(ent1)
		x.putQuery("mine", member)
		x.seedPrim(ent1, true)
		x.seedPrim(member, true)

		x.start()
		x.ensure(ent1, []string{groupSide}, EnsureOptions{})
		x.ensure(member, []string{groupSide}, EnsureOptions{})
		x.stop()
		// A class 1 fetch has completed; its own task no longer runs, and the next
		// cycle is queued: class 4, and class 3 for a member of a configured query.
		if c, queued, running, _ := x.svc.Inspect(ctx, ent1, groupSide); !queued || running || c != governor.ClassKeepAlive {
			t.Errorf("after a class 1 fetch: class %v queued %v running %v, want class 4 queued", c, queued, running)
		}
		if c, queued, _, _ := x.svc.Inspect(ctx, member, groupSide); !queued || c != governor.ClassQuery {
			t.Errorf("member: class %v queued %v, want class 3 queued", c, queued)
		}

		// It is due at fresh_until: not a minute before, then it runs.
		before := f.callCount()
		x.clk.Advance(9 * time.Minute)
		x.sched.Drain(ctx)
		if got := f.callCount(); got != before {
			t.Fatalf("a task due in a minute ran: %d calls, was %d", got, before)
		}
		x.clk.Advance(2 * time.Minute)
		x.sched.Drain(ctx)
		// Both are due: one batch of two, admitted as the best class among them.
		calls := f.callList()
		if got := len(calls); got != before+1 || len(calls[before].Targets) != 2 {
			t.Fatalf("%d calls after the due time, want one more with both entities: %+v", got, calls[before:])
		}
		if c := calls[before].Call; c.Class != governor.ClassQuery || c.Purpose != governor.PurposeRefresh {
			t.Errorf("background call = %+v, want a class 3 refresh (the best class of the batch)", c)
		}
	})
}

func TestBackgroundBookingRules(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		ctx := context.Background()
		x := newEnsureRig(t, spec)
		x.keep.keep(ent1)
		x.seedPrim(ent1, true)
		x.start()

		x.ensure(ent1, []string{groupSide, groupStamped, groupPassive}, EnsureOptions{})
		x.stop()
		if _, queued, _, _ := x.svc.Inspect(ctx, ent1, groupSide); !queued {
			t.Error("the timed group has no background task")
		}
		for _, g := range []string{groupStamped, groupPassive} {
			if c, queued, running, _ := x.svc.Inspect(ctx, ent1, g); queued || running {
				t.Errorf("%s has a background task (class %v): a stamped group with no time limit and a passive group are read through only", g, c)
			}
			if row, ok := x.group(ent1, g); !ok || len(row.Content) == 0 {
				t.Errorf("%s was not fetched on read", g)
			}
		}
		// A refresh that leaves a stamped row without a fresh_until books nothing
		// either, whichever way it gets there.
		if err := x.svc.QueueGroup(ctx, ent1, groupStamped, governor.ClassInvalidated, time.Time{}); err != nil {
			t.Fatal(err)
		}
		x.sched.Drain(ctx)
		if _, queued, _, _ := x.svc.Inspect(ctx, ent1, groupStamped); queued {
			t.Error("a stamped refresh booked a time-based task")
		}
	})
}

func TestTheReadAfterAnInvalidationGoesToOriginOnceAndSharesTheQueuedTask(t *testing.T) {
	forEachKind(t, func(t *testing.T, spec registry.Spec) {
		ctx := context.Background()
		x := newEnsureRig(t, spec)
		f := x.fetch[groupSide]
		x.seedPrim(ent1, true)
		x.seedGroup(freshGroup(x.rig, ent1, groupSide, "d"))
		for i := 0; i < 2; i++ { // twice: still exactly one queued task
			if err := x.svc.InvalidateAndQueue(ctx, ent1, groupSide); err != nil {
				t.Fatal(err)
			}
		}
		row, _ := x.group(ent1, groupSide)
		if got := x.svc.cfg.Rules.Fresh(row, stampsOf(x, ent1), x.clk.Now()); got.IsFresh() {
			t.Error("an invalidated row still reads as fresh")
		}

		// The read after the write goes to origin: either the queued class 2 task is
		// already running (the read joins it) or the read goes first (and takes the
		// queued task over); either way ONE fetch happens and the old content goes.
		x.start()
		res := x.ensure(ent1, []string{groupSide}, EnsureOptions{})
		if g := groupResult(t, res, groupSide); g.Status != Fresh || string(g.Row.Content) == "d" {
			t.Errorf("side = %+v, want fresh content from after the invalidation", g)
		}
		if n := f.callCount(); n != 1 {
			t.Errorf("fetcher called %d times, want the queued task and the read to share one", n)
		}
	})
}
