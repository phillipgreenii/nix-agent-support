package otel

import (
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// findInt64Point returns the first int64 gauge data point whose attribute set
// contains every key=value pair in want, or reports absence.
func findInt64Point(dps []metricdata.DataPoint[int64], want map[string]string) (metricdata.DataPoint[int64], bool) {
	for _, dp := range dps {
		match := true
		for k, v := range want {
			got, present := dp.Attributes.Value(attribute.Key(k))
			if !present || got.AsString() != v {
				match = false
				break
			}
		}
		if match {
			return dp, true
		}
	}
	return metricdata.DataPoint[int64]{}, false
}

// TestSessionsCount_CarryForwardZero is the ADR 0024 D4 / R6 acceptance test:
// when a FULL label-tuple present on a prior tick vanishes, the gauge MUST
// observe it once at 0 (not merely stop reporting it), so Prometheus drops the
// orphaned series instead of retaining its last value (OTLP has no staleness
// markers). The fixture deliberately carries workspace.scope + plan_tier so a
// naive bare-`state` zero would NOT match the orphan tuple and would fail here.
func TestSessionsCount_CarryForwardZero(t *testing.T) {
	e, reader := newTestEmitter(t)
	base := map[string]string{"plan_tier": "max_20x"}

	workingTuple := map[string]string{"state": "working", "workspace.scope": "ziprecruiter", "plan_tier": "max_20x"}
	idleTuple := map[string]string{"state": "idle", "workspace.scope": "ziprecruiter", "plan_tier": "max_20x"}

	// Tick 1: both working and idle live.
	e.RecordSessionGroups([]SessionGroup{
		{Count: 1, Labels: map[string]string{"state": "working", "workspace.scope": "ziprecruiter"}},
		{Count: 1, Labels: map[string]string{"state": "idle", "workspace.scope": "ziprecruiter"}},
	}, base)

	m, ok := collectMetric(t, reader, "pa_monitor.sessions.count")
	if !ok {
		t.Fatal("pa_monitor.sessions.count not emitted on tick 1")
	}
	g := m.Data.(metricdata.Gauge[int64])
	if dp, found := findInt64Point(g.DataPoints, workingTuple); !found || dp.Value != 1 {
		t.Fatalf("tick1 working tuple = {found:%v value:%d}, want {true 1}; points=%+v", found, dp.Value, g.DataPoints)
	}
	if dp, found := findInt64Point(g.DataPoints, idleTuple); !found || dp.Value != 1 {
		t.Fatalf("tick1 idle tuple = {found:%v value:%d}, want {true 1}", found, dp.Value)
	}

	// Tick 2: working flips to idle — only the idle tuple is reported now.
	e.RecordSessionGroups([]SessionGroup{
		{Count: 2, Labels: map[string]string{"state": "idle", "workspace.scope": "ziprecruiter"}},
	}, base)

	m, ok = collectMetric(t, reader, "pa_monitor.sessions.count")
	if !ok {
		t.Fatal("pa_monitor.sessions.count not emitted on tick 2")
	}
	g = m.Data.(metricdata.Gauge[int64])
	// The orphaned FULL working tuple MUST be present at 0 — not absent, not stale 1.
	dp, found := findInt64Point(g.DataPoints, workingTuple)
	if !found {
		t.Fatalf("tick2: orphaned working tuple absent; want it observed at 0; points=%+v", g.DataPoints)
	}
	if dp.Value != 0 {
		t.Fatalf("tick2: orphaned working tuple = %d, want 0 (carry-forward-zero)", dp.Value)
	}
	// The surviving idle tuple carries its new count.
	if dp, found := findInt64Point(g.DataPoints, idleTuple); !found || dp.Value != 2 {
		t.Fatalf("tick2 idle tuple = {found:%v value:%d}, want {true 2}", found, dp.Value)
	}
}

// TestSessionsCount_ZeroedOnce proves the carry-forward zero fires EXACTLY
// once: after the vanished tuple has been observed at 0, a subsequent tick that
// still omits it MUST NOT re-emit it (it is forgotten, so the series ages out
// rather than pinning at 0 forever).
func TestSessionsCount_ZeroedOnce(t *testing.T) {
	e, reader := newTestEmitter(t)
	base := map[string]string{"plan_tier": "max_20x"}
	workingTuple := map[string]string{"state": "working", "workspace.scope": "ziprecruiter", "plan_tier": "max_20x"}

	// Tick 1: working + idle.
	e.RecordSessionGroups([]SessionGroup{
		{Count: 1, Labels: map[string]string{"state": "working", "workspace.scope": "ziprecruiter"}},
		{Count: 1, Labels: map[string]string{"state": "idle", "workspace.scope": "ziprecruiter"}},
	}, base)
	collectMetric(t, reader, "pa_monitor.sessions.count")

	// Tick 2: only idle — working zeroed once.
	e.RecordSessionGroups([]SessionGroup{
		{Count: 2, Labels: map[string]string{"state": "idle", "workspace.scope": "ziprecruiter"}},
	}, base)
	m, _ := collectMetric(t, reader, "pa_monitor.sessions.count")
	g := m.Data.(metricdata.Gauge[int64])
	if dp, found := findInt64Point(g.DataPoints, workingTuple); !found || dp.Value != 0 {
		t.Fatalf("tick2 working tuple = {found:%v value:%d}, want {true 0}", found, dp.Value)
	}

	// Tick 3: still only idle — working must NOT be re-emitted (zeroed once).
	e.RecordSessionGroups([]SessionGroup{
		{Count: 2, Labels: map[string]string{"state": "idle", "workspace.scope": "ziprecruiter"}},
	}, base)
	m, _ = collectMetric(t, reader, "pa_monitor.sessions.count")
	g = m.Data.(metricdata.Gauge[int64])
	if dp, found := findInt64Point(g.DataPoints, workingTuple); found {
		t.Fatalf("tick3: working tuple re-emitted (value %d); want absent after zeroing once", dp.Value)
	}
}

// TestSessionsErrored_CarryForwardZero is the RecordSessionsErrored counterpart
// (keyed by `kind`): a kind present last tick but absent now MUST be observed
// at 0 exactly once, then drop out.
func TestSessionsErrored_CarryForwardZero(t *testing.T) {
	e, reader := newTestEmitter(t)

	// Tick 1: rate_limit=4.
	e.RecordSessionsErrored(map[string]int{"rate_limit": 4})
	m, ok := collectMetric(t, reader, "pa_monitor.sessions.errored")
	if !ok {
		t.Fatal("pa_monitor.sessions.errored not emitted on tick 1")
	}
	g := m.Data.(metricdata.Gauge[int64])
	if dp, found := findInt64Point(g.DataPoints, map[string]string{"kind": "rate_limit"}); !found || dp.Value != 4 {
		t.Fatalf("tick1 rate_limit = {found:%v value:%d}, want {true 4}", found, dp.Value)
	}

	// Tick 2: empty — rate_limit vanished, must be observed at 0.
	e.RecordSessionsErrored(map[string]int{})
	m, ok = collectMetric(t, reader, "pa_monitor.sessions.errored")
	if !ok {
		t.Fatal("pa_monitor.sessions.errored not emitted on tick 2")
	}
	g = m.Data.(metricdata.Gauge[int64])
	dp, found := findInt64Point(g.DataPoints, map[string]string{"kind": "rate_limit"})
	if !found {
		t.Fatalf("tick2: orphaned rate_limit kind absent; want observed at 0; points=%+v", g.DataPoints)
	}
	if dp.Value != 0 {
		t.Fatalf("tick2: orphaned rate_limit = %d, want 0 (carry-forward-zero)", dp.Value)
	}

	// Tick 3: still empty — rate_limit must NOT be re-emitted (zeroed once). With
	// no observations the gauge is omitted entirely, which trivially satisfies
	// "not re-emitted"; if present it must not carry the rate_limit series.
	e.RecordSessionsErrored(map[string]int{})
	if m, ok := collectMetric(t, reader, "pa_monitor.sessions.errored"); ok {
		g := m.Data.(metricdata.Gauge[int64])
		if dp, found := findInt64Point(g.DataPoints, map[string]string{"kind": "rate_limit"}); found {
			t.Fatalf("tick3: rate_limit re-emitted (value %d); want absent after zeroing once", dp.Value)
		}
	}
}
