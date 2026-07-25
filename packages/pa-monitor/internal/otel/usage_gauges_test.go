package otel

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// newTestEmitter builds an Emitter backed by a ManualReader so a test can Collect
// the observable gauges and assert their values / attributes directly (no exporter).
func newTestEmitter(t *testing.T) (*Emitter, *metric.ManualReader) {
	t.Helper()
	reader := metric.NewManualReader()
	e, err := NewWithReader(reader)
	if err != nil {
		t.Fatalf("NewWithReader: %v", err)
	}
	return e, reader
}

// collectMetric returns the metricdata.Metric with the given name, or fails.
func collectMetric(t *testing.T, reader *metric.ManualReader, name string) (metricdata.Metrics, bool) {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return m, true
			}
		}
	}
	return metricdata.Metrics{}, false
}

// TestUsageGauges_AccountGlobalNoSessionID is the ADR 0021 §5 contract: the new
// block/week usage.percentage + resets_at gauges are account-global and MUST NOT
// carry a session_id label; they observe the authoritative used_percentage and
// reset epoch.
func TestUsageGauges_AccountGlobalNoSessionID(t *testing.T) {
	e, reader := newTestEmitter(t)

	fivePct := 34.0
	sevPct := 5.0
	fiveRst := time.Unix(1782958200, 0)
	sevRst := time.Unix(1783000000, 0)
	attrs := map[string]string{"plan_tier": "max_20x"}
	e.RecordBlockUsage(&fivePct, fiveRst, attrs)
	e.RecordWeekUsage(&sevPct, sevRst, attrs)

	// block.usage.percentage
	m, ok := collectMetric(t, reader, "pa_monitor.block.usage.percentage")
	if !ok {
		t.Fatal("pa_monitor.block.usage.percentage not emitted")
	}
	g := m.Data.(metricdata.Gauge[float64])
	if len(g.DataPoints) != 1 {
		t.Fatalf("block.usage.percentage points = %d, want 1", len(g.DataPoints))
	}
	dp := g.DataPoints[0]
	if dp.Value != 34.0 {
		t.Errorf("block.usage.percentage = %v, want 34", dp.Value)
	}
	if _, present := dp.Attributes.Value("session_id"); present {
		t.Error("block.usage.percentage carries a session_id label; must be account-global")
	}
	if v, present := dp.Attributes.Value("plan_tier"); !present || v.AsString() != "max_20x" {
		t.Errorf("block.usage.percentage plan_tier = %v present=%v, want max_20x", v.AsString(), present)
	}

	// block.usage.resets_at (epoch, int64)
	rm, ok := collectMetric(t, reader, "pa_monitor.block.usage.resets_at")
	if !ok {
		t.Fatal("pa_monitor.block.usage.resets_at not emitted")
	}
	rg := rm.Data.(metricdata.Gauge[int64])
	if len(rg.DataPoints) != 1 || rg.DataPoints[0].Value != 1782958200 {
		t.Errorf("block.usage.resets_at = %+v, want 1782958200", rg.DataPoints)
	}
	if _, present := rg.DataPoints[0].Attributes.Value("session_id"); present {
		t.Error("block.usage.resets_at carries a session_id label; must be account-global")
	}

	// week.usage.percentage
	wm, ok := collectMetric(t, reader, "pa_monitor.week.usage.percentage")
	if !ok {
		t.Fatal("pa_monitor.week.usage.percentage not emitted")
	}
	wg := wm.Data.(metricdata.Gauge[float64])
	if len(wg.DataPoints) != 1 || wg.DataPoints[0].Value != 5.0 {
		t.Errorf("week.usage.percentage = %+v, want 5", wg.DataPoints)
	}
	if _, present := wg.DataPoints[0].Attributes.Value("session_id"); present {
		t.Error("week.usage.percentage carries a session_id label; must be account-global")
	}

	// week.usage.resets_at
	wrm, ok := collectMetric(t, reader, "pa_monitor.week.usage.resets_at")
	if !ok {
		t.Fatal("pa_monitor.week.usage.resets_at not emitted")
	}
	wrg := wrm.Data.(metricdata.Gauge[int64])
	if len(wrg.DataPoints) != 1 || wrg.DataPoints[0].Value != 1783000000 {
		t.Errorf("week.usage.resets_at = %+v, want 1783000000", wrg.DataPoints)
	}
}

// TestUsageGauges_UnknownNotEmitted proves an unknown percentage (nil) / zero reset
// is NOT observed at all — a nil pct must not surface as 0, and a zero reset must
// not surface as a 1970 epoch. Only the known window emits.
func TestUsageGauges_UnknownNotEmitted(t *testing.T) {
	e, reader := newTestEmitter(t)

	// five_hour known, seven_day entirely unknown (Phase 0 case).
	fivePct := 34.0
	e.RecordBlockUsage(&fivePct, time.Unix(1782958200, 0), nil)
	e.RecordWeekUsage(nil, time.Time{}, nil)

	if m, ok := collectMetric(t, reader, "pa_monitor.week.usage.percentage"); ok {
		g := m.Data.(metricdata.Gauge[float64])
		if len(g.DataPoints) != 0 {
			t.Errorf("week.usage.percentage emitted %d points, want 0 (unknown != 0)", len(g.DataPoints))
		}
	}
	if m, ok := collectMetric(t, reader, "pa_monitor.week.usage.resets_at"); ok {
		g := m.Data.(metricdata.Gauge[int64])
		if len(g.DataPoints) != 0 {
			t.Errorf("week.usage.resets_at emitted %d points, want 0 (unknown != 1970)", len(g.DataPoints))
		}
	}
	// block.usage.percentage IS known -> must appear.
	if m, ok := collectMetric(t, reader, "pa_monitor.block.usage.percentage"); !ok {
		t.Error("block.usage.percentage missing")
	} else {
		g := m.Data.(metricdata.Gauge[float64])
		if len(g.DataPoints) != 1 || g.DataPoints[0].Value != 34.0 {
			t.Errorf("block.usage.percentage = %+v, want single 34", g.DataPoints)
		}
	}
}

// TestLimitHitCounters_BornAtZero proves the block/week limit-hit counters are
// Add(0)-initialised at emitter startup (ADR 0024 R7): the zero series must
// exist immediately after registerMetrics so increase() over a range that
// includes creation captures the first edge (a counter born at its first Add(1)
// would miss the initial increment across the range boundary).
func TestLimitHitCounters_BornAtZero(t *testing.T) {
	_, reader := newTestEmitter(t)

	for _, name := range []string{
		"pa_monitor.block.usage.limit_hits_total",
		"pa_monitor.week.usage.limit_hits_total",
	} {
		m, ok := collectMetric(t, reader, name)
		if !ok {
			t.Fatalf("%s not present after startup; Add(0) birthing missing", name)
		}
		sum, ok := m.Data.(metricdata.Sum[int64])
		if !ok {
			t.Fatalf("%s is %T, want metricdata.Sum[int64]", name, m.Data)
		}
		if len(sum.DataPoints) != 1 {
			t.Fatalf("%s data points = %d, want 1 (the born-at-zero series)", name, len(sum.DataPoints))
		}
		if sum.DataPoints[0].Value != 0 {
			t.Errorf("%s born at %d, want 0", name, sum.DataPoints[0].Value)
		}
	}
}

// TestUsageGauges_NilSafe: the new methods must accept a nil receiver.
func TestUsageGauges_NilSafe(t *testing.T) {
	var e *Emitter
	p := 10.0
	e.RecordBlockUsage(&p, time.Unix(1, 0), nil)
	e.RecordWeekUsage(nil, time.Time{}, map[string]string{"plan_tier": "x"})
}
