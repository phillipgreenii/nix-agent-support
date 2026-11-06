package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/calstate"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/query"
)

func calOrch(t *testing.T, statePath string, names ...string) *Orchestrator {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	var srcs query.SourceSet
	for _, n := range names {
		srcs = append(srcs, query.Source{Name: n, Query: query.TimerQuery{
			Meta: query.Meta{EmitTypes: []string{"scan.due"}, Trig: query.CalendarTrigger{
				At: query.ClockTime{Hour: 2}, Loc: loc, OnMissed: "run-now",
			}},
			Metadata: map[string]string{"lens": "security"},
		}})
	}
	o := newOrch(fastCfg(t), srcs)
	o.Bindings = core.NewBindings("scan.due")
	o.CalendarStatePath = statePath
	return o
}

func calSlot(d int) time.Time {
	loc, _ := time.LoadLocation("America/New_York")
	return time.Date(2027, 6, d, 2, 0, 0, 0, loc)
}

func at(o *Orchestrator, t time.Time) { o.calNow = func() time.Time { return t } }

// Two Orchestrators on one state dir: the second does not fire a slot the first
// already handled (no double fire) and still fires the next one (no loss).
func TestProduceTick_calendarRestartAcrossSlot(t *testing.T) {
	path := filepath.Join(t.TempDir(), calstate.FileName)
	q := newTestQueue(t)

	o1 := calOrch(t, path, "weekly")
	o1.ProcessStart = calSlot(9).Add(-time.Hour)
	at(o1, calSlot(9).Add(10*time.Hour))
	if _, err := o1.ProduceTick(context.Background(), q); err != nil { // first start
		t.Fatal(err)
	}
	at(o1, calSlot(10).Add(time.Second))
	if _, err := o1.ProduceTick(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if q.DepthByType()["scan.due"] != 1 {
		t.Fatalf("depth=%v", q.DepthByType())
	}

	o2 := calOrch(t, path, "weekly")
	o2.ProcessStart = calSlot(10).Add(time.Minute)
	at(o2, calSlot(10).Add(2*time.Minute))
	if _, err := o2.ProduceTick(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if q.DepthByType()["scan.due"] != 1 {
		t.Fatalf("double fire across a restart: %v", q.DepthByType())
	}
	at(o2, calSlot(11).Add(time.Second))
	if _, err := o2.ProduceTick(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if q.DepthByType()["scan.due"] != 2 {
		t.Fatalf("lost the next slot: %v", q.DepthByType())
	}
	recs, err := calstate.Read(path)
	if err != nil || !recs["weekly"].LastSlot.Equal(calSlot(11)) {
		t.Fatalf("state file = %+v err=%v", recs, err)
	}
}

// Start loads the state, prunes entries for queries no longer configured as
// calendar queries, and clamps a remembered slot in the future.
func TestProduceTick_calendarStartLoadsPrunesAndClamps(t *testing.T) {
	path := filepath.Join(t.TempDir(), calstate.FileName)
	err := calstate.Write(path, map[string]calstate.Record{
		"weekly":  {LastSlot: calSlot(20), Outcome: calstate.OutcomeFired},
		"removed": {LastSlot: calSlot(9), Outcome: calstate.OutcomeFired},
	})
	if err != nil {
		t.Fatal(err)
	}
	o := calOrch(t, path, "weekly")
	o.ProcessStart = calSlot(1)
	at(o, calSlot(10).Add(time.Hour))
	q := newTestQueue(t)
	rpt, err := o.ProduceTick(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if !rpt.Calendar["weekly"].Clamped || q.DepthByType()["scan.due"] != 0 {
		t.Fatalf("report=%+v depth=%v", rpt.Calendar["weekly"], q.DepthByType())
	}
	recs, err := calstate.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, still := recs["removed"]; still || !recs["weekly"].LastSlot.Equal(calSlot(10)) {
		t.Fatalf("state after start = %+v", recs)
	}
}

// A truncated state file counts as no state (a first start): nothing fires.
func TestProduceTick_calendarTruncatedStateIsFirstStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), calstate.FileName)
	if err := os.WriteFile(path, []byte(`{"version":1,"queries":{"weekly":{"last_s`), 0o644); err != nil {
		t.Fatal(err)
	}
	o := calOrch(t, path, "weekly")
	at(o, calSlot(10).Add(time.Hour))
	q := newTestQueue(t)
	rpt, err := o.ProduceTick(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if !rpt.Calendar["weekly"].FirstStart || q.DepthByType()["scan.due"] != 0 {
		t.Fatalf("report=%+v depth=%v", rpt.Calendar["weekly"], q.DepthByType())
	}
}

// The sampler seam reaches the pass: a sleep span over the slot is a missed slot.
func TestProduceTick_calendarSamplerSpanMakesSlotMissed(t *testing.T) {
	path := filepath.Join(t.TempDir(), calstate.FileName)
	if err := calstate.Write(path, map[string]calstate.Record{"weekly": {LastSlot: calSlot(9), Outcome: calstate.OutcomeFired}}); err != nil {
		t.Fatal(err)
	}
	o := calOrch(t, path, "weekly")
	o.ProcessStart = calSlot(1)
	wake := calSlot(10).Add(time.Hour)
	var mono time.Duration
	s := calstate.NewSampler(10*time.Second, func() calstate.Reading { return calstate.Reading{Wall: wake, Mono: mono} })
	s.Observe(calstate.Reading{Wall: calSlot(10).Add(-time.Hour), Mono: 0})
	mono = 10 * time.Second
	o.Sampler = s
	at(o, wake)
	q := newTestQueue(t)
	rpt, err := o.ProduceTick(context.Background(), q) // Sample() reads the wake
	if err != nil {
		t.Fatal(err)
	}
	cr := rpt.Calendar["weekly"]
	if !cr.Missed || cr.MissedReason != calstate.ReasonHostAsleep || q.DepthByType()["scan.due"] != 1 {
		t.Fatalf("report=%+v", cr)
	}
}
