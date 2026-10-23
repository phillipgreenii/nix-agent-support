package groupsvc

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
)

func TestRebuildQueuesTheDueGroupsOfTheKeepAliveSetOnly(t *testing.T) {
	for _, spec := range []struct {
		name string
		spec func() *rig
	}{
		{"alpha", func() *rig { return newRig(t, specAlpha, nil, nil) }},
		{"beta", func() *rig { return newRig(t, specBeta, nil, nil) }},
	} {
		t.Run(spec.name, func(t *testing.T) {
			r := spec.spec()
			ctx := context.Background()
			// ent-1 is a member: side is fresh (due later), stamped has no time
			// limit, passive is outside the background cycle.
			r.putQuery("mine", ent1)
			r.seedPrimary(ent1, "p")
			r.seedGroup(freshGroup(r, ent1, groupSide, "d"))
			r.seedGroup(freshGroup(r, ent1, groupStamped, "f"))
			// ent-2 is kept alive by the connector: side is stale (due now).
			r.seedPrimary("ent-2", "p")
			r.keep.keep("ent-2")
			r.seedGroup(staleGroup(r, "ent-2", groupSide, "old"))
			// ent-3 is outside the set; ent-4 is kept alive but has no primary content.
			r.seedPrimary("ent-3", "p")
			r.seedGroup(staleGroup(r, "ent-3", groupSide, "idle"))
			r.seedEntity("ent-4", r.clk.Now())
			r.keep.keep("ent-4")

			n, err := r.svc.Rebuild(ctx)
			if err != nil {
				t.Fatal(err)
			}
			// ent-1 side (due later) and ent-2 side (due now): the stamped row has
			// no time limit and passive is not background.
			if n != 2 {
				t.Fatalf("Rebuild queued %d tasks, want 2", n)
			}
			if c, queued, _, _ := r.svc.Inspect(ctx, ent1, groupSide); !queued || c != governor.ClassQuery {
				t.Errorf("member: class %v queued %v, want a class 3 task", c, queued)
			}
			if c, queued, _, _ := r.svc.Inspect(ctx, "ent-2", groupSide); !queued || c != governor.ClassKeepAlive {
				t.Errorf("kept alive: class %v queued %v, want a class 4 task", c, queued)
			}
			for _, tc := range []struct{ id, group string }{
				{ent1, groupStamped}, {ent1, groupPassive}, {"ent-3", groupSide}, {"ent-4", groupSide}, {"ent-2", groupStamped},
			} {
				if _, queued, _, _ := r.svc.Inspect(ctx, tc.id, tc.group); queued {
					t.Errorf("%s/%s was queued", tc.id, tc.group)
				}
			}
			// Nothing ran and nothing was fetched: Rebuild only queues.
			if !r.sched.Queued(ent1, groupSide) || !r.sched.Queued("ent-2", groupSide) {
				t.Error("the tasks are not in the scheduler's queue")
			}
		})
	}
}

// recordingExecutor stands in for the executor the later steps move: it records
// the entities of the tasks it is handed.
type recordingExecutor struct {
	mu  sync.Mutex
	ran []string
}

func (e *recordingExecutor) MaxBatch() int { return 1 }

func (e *recordingExecutor) Spend(governor.Class, int) governor.Request { return governor.Request{} }

func (e *recordingExecutor) Execute(_ context.Context, items []scheduler.Item) []scheduler.Result {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, it := range items {
		e.ran = append(e.ran, it.Task.Entity)
	}
	return make([]scheduler.Result, len(items))
}

func (e *recordingExecutor) entities() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.ran...)
}

func TestRebuildBooksAMissingGroupDueNowAndAFreshOneAtItsFreshUntil(t *testing.T) {
	r := newRig(t, specAlpha, nil, nil)
	ctx := context.Background()
	exec := &recordingExecutor{}
	r.sched.Register(groupSide, exec)
	r.seedPrimary(ent1, "p")
	r.keep.keep(ent1)
	r.seedGroup(freshGroup(r, ent1, groupSide, "d"))
	r.seedPrimary("ent-2", "p") // no side row at all
	r.keep.keep("ent-2")

	if n, err := r.svc.Rebuild(ctx); err != nil || n != 2 {
		t.Fatalf("Rebuild = %d, %v, want 2", n, err)
	}
	// Nothing is due before the fresh row's fresh_until; the missing row is due now.
	r.sched.Drain(ctx)
	if got := exec.entities(); len(got) != 1 || got[0] != "ent-2" {
		t.Errorf("ran %v before the fresh row is due, want only ent-2", got)
	}
	r.clk.Advance(11 * time.Minute)
	r.sched.Drain(ctx)
	if got := exec.entities(); len(got) != 2 || got[1] != ent1 {
		t.Errorf("ran %v after fresh_until, want ent-2 then ent-1", got)
	}
}

func TestRebuildReportsAPrimaryContentItCannotDecode(t *testing.T) {
	r := newRig(t, specAlpha, nil, nil)
	r.seedPrimary(ent1, badContent)
	r.keep.keep(ent1)
	_, err := r.svc.Rebuild(context.Background())
	if err == nil || !strings.Contains(err.Error(), ent1) {
		t.Errorf("Rebuild = %v, want an error naming the entity", err)
	}
}
