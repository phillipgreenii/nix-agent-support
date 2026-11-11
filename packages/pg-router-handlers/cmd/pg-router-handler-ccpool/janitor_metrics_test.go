package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

type gaugePoint struct {
	name   string
	value  float64
	labels map[string]string
}

func spyEmit(out *[]gaugePoint) gaugeEmitFn {
	return func(_ context.Context, name string, v float64, l map[string]string) error {
		*out = append(*out, gaugePoint{name, v, l})
		return nil
	}
}

func find(pts []gaugePoint, name, pass string) (gaugePoint, bool) {
	for _, p := range pts {
		if p.name == name && (pass == "" || p.labels["pass"] == pass) {
			return p, true
		}
	}
	return gaugePoint{}, false
}

func TestEmitJanitorGaugesCompleted(t *testing.T) {
	var pts []gaugePoint
	now := time.Unix(1700000000, 0)
	stats := map[string]*passStat{passOrphan: {Runs: 2, Reclaimed: 3, Errors: 1}}
	emitJanitorGauges(context.Background(), spyEmit(&pts), now, janitorCompleted, 90*time.Second, stats, nil)
	if p, ok := find(pts, gaugeJanitorLastSuccess, ""); !ok || p.value != 1700000000 {
		t.Errorf("last success = %+v ok=%v", p, ok)
	}
	if p, _ := find(pts, gaugeJanitorLastRun, ""); p.value != 90 {
		t.Errorf("last run = %v", p.value)
	}
	if p, _ := find(pts, gaugeJanitorDeadlineHit, ""); p.value != 0 {
		t.Errorf("deadline hit = %v", p.value)
	}
	if p, _ := find(pts, gaugeJanitorReclaimed, passOrphan); p.value != 3 {
		t.Errorf("reclaimed orphan = %v", p.value)
	}
	if p, _ := find(pts, gaugeJanitorPassErrors, passOrphan); p.value != 1 {
		t.Errorf("errors orphan = %v", p.value)
	}
	// A pass that did not run reports an explicit zero.
	if p, ok := find(pts, gaugeJanitorPassErrors, passLockGC); !ok || p.value != 0 {
		t.Errorf("lock_gc errors = %+v ok=%v", p, ok)
	}
}

func TestEmitJanitorGaugesDeadlineDoesNotAdvanceSuccess(t *testing.T) {
	var pts []gaugePoint
	emitJanitorGauges(context.Background(), spyEmit(&pts), time.Now(), janitorDeadline, time.Minute, nil, []string{"a", "b"})
	if p, ok := find(pts, gaugeJanitorRoleSuccess, ""); !ok || p.labels["role"] != "a" {
		t.Errorf("a deadline run must still mark finished roles, got %+v ok=%v", p, ok)
	}
	if _, ok := find(pts, gaugeJanitorLastSuccess, ""); ok {
		t.Error("a deadline run must not move last_success")
	}
	if p, _ := find(pts, gaugeJanitorDeadlineHit, ""); p.value != 1 {
		t.Errorf("deadline hit = %v, want 1", p.value)
	}
}

func TestEmitJanitorGaugesNilAndFailureAreHarmless(t *testing.T) {
	emitJanitorGauges(context.Background(), nil, time.Now(), janitorCompleted, 0, nil, nil)
	fail := func(context.Context, string, float64, map[string]string) error { return errors.New("boom") }
	emitJanitorGauges(context.Background(), fail, time.Now(), janitorCompleted, 0, nil, nil)
}

func TestEmitOrphanGauge(t *testing.T) {
	var pts []gaugePoint
	orphanGaugeEmit = spyEmit(&pts)
	t.Cleanup(func() { orphanGaugeEmit = nil })
	emitOrphanGauge(t.Context(), "worker", orphanEventReclaimed, time.Unix(42, 0))
	if len(pts) != 1 || pts[0].name != gaugeOrphanLastEvent || pts[0].value != 42 ||
		pts[0].labels["role"] != "worker" || pts[0].labels["kind"] != orphanEventReclaimed {
		t.Errorf("points = %+v", pts)
	}
}
