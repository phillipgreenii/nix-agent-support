package telemetry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
)

func TestTrackedEntitiesReportsEachClassAndSkipsOnError(t *testing.T) {
	f := newChangeFixture(t)
	c, fail := TrackedCounts{Members: 7, OtherOpen: 3, ClosedGrace: 2}, false
	f.build(func(o *Options) {
		o.Tracked = func(context.Context) (TrackedCounts, error) {
			if fail {
				return TrackedCounts{}, errors.New("store closed")
			}
			return c, nil
		}
	})
	m := f.metric(MetricTrackedEntities)
	for class, want := range map[string]int64{ClassMembers: 7, ClassOtherOpen: 3, ClassClosedGrace: 2} {
		if v := gaugeInt(t, m, "class="+class); v != want {
			t.Errorf("tracked.entities[%s] = %d, want %d", class, v, want)
		}
	}
	if sets := labelSets(t, m); len(sets) != 3 {
		t.Errorf("tracked.entities label sets = %v, want exactly the three classes", sets)
	}
	// A class that empties reports zero, not a vanished series.
	c = TrackedCounts{}
	if v := gaugeInt(t, f.metric(MetricTrackedEntities), "class="+ClassMembers); v != 0 {
		t.Errorf("tracked.entities[members] after the set emptied = %d, want 0", v)
	}
	fail = true
	if _, ok := f.collect()[MetricTrackedEntities]; ok {
		t.Error("tracked.entities must be unreported while its source fails")
	}
}

func TestTrackedEntitiesIsUnreportedWithoutASource(t *testing.T) {
	f := newChangeFixture(t)
	f.build(nil)
	if _, ok := f.collect()[MetricTrackedEntities]; ok {
		t.Error("tracked.entities reported with its source unset")
	}
}

func TestTrackedLimitIsItsOwnGaugeReportedOnlyWhenConfigured(t *testing.T) {
	f := newChangeFixture(t)
	f.build(func(o *Options) { o.TrackedLimit = 900 })
	if v := gaugeInt(t, f.metric(MetricTrackedLimit), ""); v != 900 {
		t.Errorf("tracked.limit = %d, want the configured 900", v)
	}

	g := newChangeFixture(t)
	g.build(nil)
	if _, ok := g.collect()[MetricTrackedLimit]; ok {
		t.Error("tracked.limit reported with no limit configured: an alert would divide by zero")
	}
}

func TestGovernorBacklogCountsOnlyTheTasksDueNowPerClass(t *testing.T) {
	f := newFixture(t)
	now := f.clock.Now()
	submit := func(entity string, class governor.Class, due time.Time) {
		t.Helper()
		if err := f.sched.Submit(scheduler.Task{Entity: entity, Group: "summary", Class: class, Due: due}); err != nil {
			t.Fatal(err)
		}
	}
	submit("a", governor.ClassKeepAlive, time.Time{})
	submit("b", governor.ClassKeepAlive, now.Add(-time.Minute))
	submit("c", governor.ClassKeepAlive, now.Add(time.Hour)) // queued, not due: depth but no backlog
	submit("d", governor.ClassQuery, now)

	m := f.collect()[MetricGovernorBacklog]
	for class, want := range map[string]int64{"interactive": 0, "invalidated": 0, "query": 1, "keepalive": 2, "fill": 0} {
		if v := gaugeInt(t, m, "class="+class); v != want {
			t.Errorf("governor.backlog[%s] = %d, want %d", class, v, want)
		}
	}
	if v := gaugeInt(t, f.collect()[MetricQueueDepth], "class=keepalive"); v != 3 {
		t.Errorf("queue.depth[keepalive] = %d, want 3 (the backlog is a subset of the depth)", v)
	}
	f.clock.Advance(2 * time.Hour)
	if v := gaugeInt(t, f.collect()[MetricGovernorBacklog], "class=keepalive"); v != 3 {
		t.Errorf("governor.backlog[keepalive] two hours on = %d, want 3 (the late task is now due)", v)
	}
}
