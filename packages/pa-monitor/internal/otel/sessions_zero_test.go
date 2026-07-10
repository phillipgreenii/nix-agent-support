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
// (keyed by the composite (kind, is_terminal) tuple): a tuple present last tick
// but absent now MUST be observed at 0 exactly once, then drop out. ADR 0024 D5
// added is_terminal so the dashboard can filter terminal errors; carry-forward-
// zero MUST still key off the FULL composite tuple.
func TestSessionsErrored_CarryForwardZero(t *testing.T) {
	e, reader := newTestEmitter(t)

	// Tick 1: two composite series — (rate_limit, terminal)=4 and
	// (server_error, non-terminal)=3. Both attributes must appear.
	e.RecordSessionsErrored(map[ErroredKey]int{
		{Kind: "rate_limit", IsTerminal: true}:    4,
		{Kind: "server_error", IsTerminal: false}: 3,
	})
	m, ok := collectMetric(t, reader, "pa_monitor.sessions.errored")
	if !ok {
		t.Fatal("pa_monitor.sessions.errored not emitted on tick 1")
	}
	g := m.Data.(metricdata.Gauge[int64])
	if dp, found := findInt64Point(g.DataPoints, map[string]string{"kind": "rate_limit", "is_terminal": "true"}); !found || dp.Value != 4 {
		t.Fatalf("tick1 (rate_limit,terminal) = {found:%v value:%d}, want {true 4}; points=%+v", found, dp.Value, g.DataPoints)
	}
	if dp, found := findInt64Point(g.DataPoints, map[string]string{"kind": "server_error", "is_terminal": "false"}); !found || dp.Value != 3 {
		t.Fatalf("tick1 (server_error,non-terminal) = {found:%v value:%d}, want {true 3}", found, dp.Value)
	}

	// Tick 2: server_error gone, rate_limit stays. The orphaned
	// (server_error, non-terminal) tuple MUST be observed at 0.
	e.RecordSessionsErrored(map[ErroredKey]int{
		{Kind: "rate_limit", IsTerminal: true}: 4,
	})
	m, ok = collectMetric(t, reader, "pa_monitor.sessions.errored")
	if !ok {
		t.Fatal("pa_monitor.sessions.errored not emitted on tick 2")
	}
	g = m.Data.(metricdata.Gauge[int64])
	dp, found := findInt64Point(g.DataPoints, map[string]string{"kind": "server_error", "is_terminal": "false"})
	if !found {
		t.Fatalf("tick2: orphaned (server_error,non-terminal) absent; want observed at 0; points=%+v", g.DataPoints)
	}
	if dp.Value != 0 {
		t.Fatalf("tick2: orphaned (server_error,non-terminal) = %d, want 0 (carry-forward-zero)", dp.Value)
	}
	if dp, found := findInt64Point(g.DataPoints, map[string]string{"kind": "rate_limit", "is_terminal": "true"}); !found || dp.Value != 4 {
		t.Fatalf("tick2 (rate_limit,terminal) = {found:%v value:%d}, want {true 4}", found, dp.Value)
	}

	// Tick 3: server_error still absent — must NOT be re-emitted (zeroed once).
	e.RecordSessionsErrored(map[ErroredKey]int{
		{Kind: "rate_limit", IsTerminal: true}: 4,
	})
	if m, ok := collectMetric(t, reader, "pa_monitor.sessions.errored"); ok {
		g := m.Data.(metricdata.Gauge[int64])
		if dp, found := findInt64Point(g.DataPoints, map[string]string{"kind": "server_error", "is_terminal": "false"}); found {
			t.Fatalf("tick3: (server_error,non-terminal) re-emitted (value %d); want absent after zeroing once", dp.Value)
		}
	}
}

// TestSessionsErrored_IsTerminalSplitsSameKind pins the over-count fix (ADR 0024
// D5): the SAME kind with mixed terminality was previously folded into one
// `kind` series (the errored panel showed ~7 vs 4 terminal). is_terminal MUST
// split them into two distinct series so the dashboard can filter to terminal.
func TestSessionsErrored_IsTerminalSplitsSameKind(t *testing.T) {
	e, reader := newTestEmitter(t)

	e.RecordSessionsErrored(map[ErroredKey]int{
		{Kind: "rate_limit", IsTerminal: true}:  4,
		{Kind: "rate_limit", IsTerminal: false}: 3,
	})
	m, ok := collectMetric(t, reader, "pa_monitor.sessions.errored")
	if !ok {
		t.Fatal("pa_monitor.sessions.errored not emitted")
	}
	g := m.Data.(metricdata.Gauge[int64])
	if dp, found := findInt64Point(g.DataPoints, map[string]string{"kind": "rate_limit", "is_terminal": "true"}); !found || dp.Value != 4 {
		t.Errorf("(rate_limit,terminal) = {found:%v value:%d}, want {true 4}; points=%+v", found, dp.Value, g.DataPoints)
	}
	if dp, found := findInt64Point(g.DataPoints, map[string]string{"kind": "rate_limit", "is_terminal": "false"}); !found || dp.Value != 3 {
		t.Errorf("(rate_limit,non-terminal) = {found:%v value:%d}, want {true 3}; points=%+v", found, dp.Value, g.DataPoints)
	}
}

// TestNudgeDeferred_CarryForwardZero is the ADR 0024 D5 deferral-visibility
// acceptance test: pa_monitor.nudge.deferred{cause=window_pending} reports the
// current count of sessions where auto-resume is deliberately WAITING on a
// window, and drops to 0 (carry-forward-zero, zeroed once) when the window
// clears — so "auto-resume is waiting" is distinguishable from "broken".
func TestNudgeDeferred_CarryForwardZero(t *testing.T) {
	e, reader := newTestEmitter(t)

	// Tick 1: 3 sessions deferred, waiting on the window reset.
	e.RecordNudgeDeferred(map[string]int{"window_pending": 3})
	m, ok := collectMetric(t, reader, "pa_monitor.nudge.deferred")
	if !ok {
		t.Fatal("pa_monitor.nudge.deferred not emitted on tick 1")
	}
	g := m.Data.(metricdata.Gauge[int64])
	if dp, found := findInt64Point(g.DataPoints, map[string]string{"cause": "window_pending"}); !found || dp.Value != 3 {
		t.Fatalf("tick1 window_pending = {found:%v value:%d}, want {true 3}; points=%+v", found, dp.Value, g.DataPoints)
	}

	// Tick 2: window cleared, none deferred. window_pending MUST be observed at 0.
	e.RecordNudgeDeferred(map[string]int{})
	m, ok = collectMetric(t, reader, "pa_monitor.nudge.deferred")
	if !ok {
		t.Fatal("pa_monitor.nudge.deferred not emitted on tick 2")
	}
	g = m.Data.(metricdata.Gauge[int64])
	dp, found := findInt64Point(g.DataPoints, map[string]string{"cause": "window_pending"})
	if !found {
		t.Fatalf("tick2: window_pending absent; want observed at 0; points=%+v", g.DataPoints)
	}
	if dp.Value != 0 {
		t.Fatalf("tick2: window_pending = %d, want 0 (carry-forward-zero)", dp.Value)
	}

	// Tick 3: still cleared — must NOT be re-emitted (zeroed once).
	e.RecordNudgeDeferred(map[string]int{})
	if m, ok := collectMetric(t, reader, "pa_monitor.nudge.deferred"); ok {
		g := m.Data.(metricdata.Gauge[int64])
		if dp, found := findInt64Point(g.DataPoints, map[string]string{"cause": "window_pending"}); found {
			t.Fatalf("tick3: window_pending re-emitted (value %d); want absent after zeroing once", dp.Value)
		}
	}
}
