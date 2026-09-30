package main

import (
	"errors"
	"testing"

	"github.com/phillipgreenii/ccpool/internal/session"
	"github.com/phillipgreenii/ccpool/internal/telemetry"
)

// TestParseCapacityArgs_emitMetricsIsOptIn proves --emit-metrics is off by
// default: the dispatch/admission-gate path runs exactly `capacity --json`
// (pg-router-ccpool-handler's CLIRunner.Capacity) and must never emit the
// ccpool_pool_capacity gauge (bead pg2-om899.6).
func TestParseCapacityArgs_emitMetricsIsOptIn(t *testing.T) {
	for _, tc := range []struct {
		args       []string
		json, emit bool
	}{
		{nil, false, false},
		{[]string{"--json"}, true, false},
		{[]string{"--emit-metrics"}, false, true},
		{[]string{"--json", "--emit-metrics"}, true, true},
	} {
		got := parseCapacityArgs(tc.args)
		if got.json != tc.json || got.emitMetrics != tc.emit {
			t.Errorf("parseCapacityArgs(%q) = %+v, want json=%v emitMetrics=%v", tc.args, got, tc.json, tc.emit)
		}
	}
}

// capacitySpy records every emitCapacityMetrics recorder call.
type capacitySpy struct {
	roots []string
	got   []telemetry.PoolCapacity
	err   error
}

func (s *capacitySpy) record(root string, c telemetry.PoolCapacity) error {
	s.roots = append(s.roots, root)
	s.got = append(s.got, c)
	return s.err
}

func TestEmitCapacityMetrics_offRecordsNothing(t *testing.T) {
	spy := &capacitySpy{}
	if err := emitCapacityMetrics(false, "/p/pg-router-ccpool-review", session.Capacity{MaxSessions: 1}, spy.record); err != nil {
		t.Fatal(err)
	}
	if len(spy.roots) != 0 {
		t.Fatalf("emit=false must not record; got %v", spy.roots)
	}
}

// TestEmitCapacityMetrics_recordsPoolRootAndEveryDim: with the flag set, the
// recorder gets the canonical pool root (whose basename becomes the pool
// attribute) and every dimension mapped field-for-field (distinct values so a
// swapped field is caught).
func TestEmitCapacityMetrics_recordsPoolRootAndEveryDim(t *testing.T) {
	spy := &capacitySpy{}
	c := session.Capacity{MaxSessions: 6, Live: 3, Preserved: 1, Counted: 2, Free: 4}
	if err := emitCapacityMetrics(true, "/p/pg-router-ccpool-review", c, spy.record); err != nil {
		t.Fatal(err)
	}
	want := telemetry.PoolCapacity{MaxSessions: 6, Live: 3, Preserved: 1, Counted: 2, Free: 4}
	if len(spy.got) != 1 || spy.roots[0] != "/p/pg-router-ccpool-review" || spy.got[0] != want {
		t.Fatalf("recorded roots=%v got=%+v, want one call with /p/pg-router-ccpool-review %+v", spy.roots, spy.got, want)
	}
}

func TestEmitCapacityMetrics_propagatesRecordError(t *testing.T) {
	spy := &capacitySpy{err: errors.New("boom")}
	if err := emitCapacityMetrics(true, "", session.Capacity{}, spy.record); err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v, want boom", err)
	}
}

// TestRenderCapacityText exercises the pure renderer directly (mirrors
// state_test.go's TestRenderState_human): no config/store/tmux involved (the
// "Unit tests MUST be isolated" global constraint).
func TestRenderCapacityText(t *testing.T) {
	c := session.Capacity{MaxSessions: 6, Live: 3, Preserved: 1, Counted: 2, Free: 4}
	want := "free=4 counted=2 preserved=1 live=3 max=6\n"
	if got := renderCapacityText(c); got != want {
		t.Fatalf("renderCapacityText(%+v) = %q, want %q", c, got, want)
	}
}

func TestRenderCapacityText_freeZero(t *testing.T) {
	c := session.Capacity{MaxSessions: 2, Live: 3, Preserved: 0, Counted: 3, Free: 0}
	want := "free=0 counted=3 preserved=0 live=3 max=2\n"
	if got := renderCapacityText(c); got != want {
		t.Fatalf("renderCapacityText(%+v) = %q, want %q", c, got, want)
	}
}
