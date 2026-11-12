package main

import (
	"context"
	"io"
	"sync/atomic"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/metrics"
)

func pinLoad1(t *testing.T, v float64, ok bool) {
	t.Helper()
	prev := load1
	load1 = func() (float64, bool) { return v, ok }
	t.Cleanup(func() { load1 = prev })
}

// Above the threshold the real tick skips the producer pass (the polled source
// is not polled) yet still dispatches queued work; at or below it, polling
// resumes on the very next tick.
func TestRunOneTick_loadBackoffSkipsProducingAboveThreshold(t *testing.T) {
	q, err := eventqueue.New(eventqueue.NewMemStore())
	if err != nil {
		t.Fatal(err)
	}
	var polls atomic.Int64
	o := limitTickOrchestrator(&polls)
	cfg := config.Config{Queries: o.Cfg.Queries, LoadBackoffThreshold: 50}
	tick := func() { runOneTick(context.Background(), cfg, o, &core.Service{}, q, "", io.Discard) }

	pinLoad1(t, 94, true)
	tick()
	tick()
	if got := polls.Load(); got != 0 {
		t.Fatalf("polled %d time(s) at load1 94 > 50; the producer pass must be skipped", got)
	}

	pinLoad1(t, 50, true) // exactly at the threshold: not above, so produce
	tick()
	if got := polls.Load(); got != 1 {
		t.Fatalf("polls = %d at load1 == threshold, want 1 (backoff resumes at or below)", got)
	}

	pinLoad1(t, 80, true)
	tick()
	if got := polls.Load(); got != 1 {
		t.Fatalf("polls = %d after load rose again, want still 1", got)
	}
}

func TestRunOneTick_loadBackoffDisabledOrUnreadableStillProduces(t *testing.T) {
	for name, tc := range map[string]struct {
		threshold float64
		load      float64
		ok        bool
	}{
		"disabled by zero threshold": {0, 500, true},
		"unreadable load fails open": {50, 0, false},
	} {
		t.Run(name, func(t *testing.T) {
			q, err := eventqueue.New(eventqueue.NewMemStore())
			if err != nil {
				t.Fatal(err)
			}
			var polls atomic.Int64
			o := limitTickOrchestrator(&polls)
			cfg := config.Config{Queries: o.Cfg.Queries, LoadBackoffThreshold: tc.threshold}
			pinLoad1(t, tc.load, tc.ok)
			runOneTick(context.Background(), cfg, o, &core.Service{}, q, "", io.Discard)
			if got := polls.Load(); got != 1 {
				t.Fatalf("polls = %d, want 1", got)
			}
		})
	}
}

// The tick path reports the load-backoff decision to the metrics emitter: the
// gauge is 1 and the counter advances on a skipped tick, and the gauge returns
// to 0 on the first tick at or below the threshold.
func TestRunOneTick_loadBackoffExportsMetric(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	emitter, err := metrics.New(t.Context(), mp, func() map[string]int { return nil })
	if err != nil {
		t.Fatal(err)
	}
	q, err := eventqueue.New(eventqueue.NewMemStore())
	if err != nil {
		t.Fatal(err)
	}
	var polls atomic.Int64
	o := limitTickOrchestrator(&polls)
	o.LoadBackoffObserver = emitter
	cfg := config.Config{Queries: o.Cfg.Queries, LoadBackoffThreshold: 50}
	tick := func() { runOneTick(context.Background(), cfg, o, &core.Service{}, q, "", io.Discard) }
	read := func() (gauge, skipped int64) {
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatal(err)
		}
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				switch d := m.Data.(type) {
				case metricdata.Gauge[int64]:
					if m.Name == metrics.MetricProducerLoadBackoff {
						gauge = d.DataPoints[0].Value
					}
				case metricdata.Sum[int64]:
					if m.Name == metrics.MetricProducerTicksSkipped {
						skipped = d.DataPoints[0].Value
					}
				}
			}
		}
		return
	}

	pinLoad1(t, 94, true)
	tick()
	tick()
	if g, s := read(); g != 1 || s != 2 {
		t.Fatalf("under load: gauge=%d skipped=%d, want 1 and 2", g, s)
	}
	pinLoad1(t, 10, true)
	tick()
	if g, s := read(); g != 0 || s != 2 {
		t.Fatalf("after load fell: gauge=%d skipped=%d, want 0 and 2", g, s)
	}
}
