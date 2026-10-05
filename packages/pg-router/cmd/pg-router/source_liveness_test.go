package main

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/event"
	"github.com/phillipgreenii/pg-router/internal/metrics"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
	"github.com/phillipgreenii/pg-router/internal/query"
)

// Per-source last-success / expected-interval gauges (bead pg2-tv11a,
// DEC-OBS-4): which sources are exported, and that bootCore wires the live
// success/pause hooks to them.

type periodSourceQuery struct {
	query.Meta
	err error
}

func (periodSourceQuery) Validate() error        { return nil }
func (periodSourceQuery) BackingCommand() string { return "" }
func (p periodSourceQuery) Run(context.Context, query.Env) ([]event.Event, error) {
	return nil, p.err
}

func periodSource(name string, every time.Duration) query.Source {
	return query.Source{Name: name, Query: periodSourceQuery{Meta: query.Meta{EmitTypes: []string{"t1"}, Trig: query.PeriodTrigger{Every: every}}}}
}

func TestPullSourceIntervals_OnlyPeriodSourcesWithAnInterval(t *testing.T) {
	cfg := config.Config{
		PollInterval: 10 * time.Second,
		Queries: query.SourceSet{
			periodSource("fast", time.Minute),
			periodSource("builtin-default", 0), // Every == 0 fires on PollInterval
			{Name: "threshold", Query: periodSourceQuery{Meta: query.Meta{Trig: query.ThresholdTrigger{Binds: []string{"t1"}, Count: 1}}}},
			{Name: "manual", Query: periodSourceQuery{Meta: query.Meta{Trig: query.ManualTrigger{}}}},
			{Name: "threshold-override", Query: periodSourceQuery{Meta: query.Meta{Trig: query.ThresholdTrigger{Binds: []string{"t1"}, Count: 1}}}},
		},
		ExpectedIntervalOverrides: map[string]int64{"threshold-override": 120000},
	}
	got := pullSourceIntervals(cfg)
	want := map[string]time.Duration{
		"fast":               time.Minute,
		"builtin-default":    10 * time.Second,
		"threshold-override": 2 * time.Minute,
	}
	if len(got) != len(want) {
		t.Fatalf("pullSourceIntervals = %v, want %v (threshold and manual sources without an expected_interval have no cadence to alert on)", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("interval[%s] = %v, want %v", k, got[k], v)
		}
	}
}

// A selector-excluded source is removed from cfg.Queries before bootCore, so
// it is never exported.
func TestPullSourceIntervals_ExcludedSourceIsAbsent(t *testing.T) {
	cfg := config.Config{
		PollInterval: 10 * time.Second,
		Queries:      query.SourceSet{periodSource("kept", time.Minute), periodSource("dropped", time.Minute)},
	}
	cfg, excluded, err := applySelectors(cfg, runSelectors{Disable: []string{"query:dropped"}})
	if err != nil {
		t.Fatalf("applySelectors: %v", err)
	}
	if len(excluded.Sources) != 1 || excluded.Sources[0] != "dropped" {
		t.Fatalf("excluded.Sources = %v, want [dropped]", excluded.Sources)
	}
	got := pullSourceIntervals(cfg)
	if _, ok := got["dropped"]; ok || got["kept"] != time.Minute {
		t.Fatalf("pullSourceIntervals = %v, want only kept", got)
	}
}

func floatGaugeBySource(t *testing.T, reader *sdkmetric.ManualReader, name string) map[string]float64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	out := map[string]float64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			g, ok := m.Data.(metricdata.Gauge[float64])
			if !ok {
				t.Fatalf("%s is %T, want a float64 gauge", name, m.Data)
			}
			for _, dp := range g.DataPoints {
				v, _ := dp.Attributes.Value(attribute.Key("source"))
				out[v.AsString()] = dp.Value
			}
		}
	}
	return out
}

// bootCore must initialise each pull source's last-success to process start
// (not 0), export its expected interval, and let a produce pass drive the
// gauge: success advances it, failure leaves it.
func TestBootCore_exportsSourceLivenessGaugesAndFeedsThem(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	cfg := config.Config{
		LogDir:        shortDir(t),
		MeterProvider: mp,
		PollInterval:  10 * time.Second,
		Queries: query.SourceSet{
			periodSource("healthy", time.Minute),
			{Name: "broken", Query: periodSourceQuery{
				Meta: query.Meta{EmitTypes: []string{"t1"}, Trig: query.PeriodTrigger{Every: 30 * time.Minute}},
				err:  context.DeadlineExceeded,
			}},
		},
	}
	o := &orchestrator.Orchestrator{Cfg: cfg}
	ctx := context.Background()
	before := time.Now()
	svc, q, _, storeClose, err := bootCore(ctx, cfg, o, cfg.Roles, runExclusions{}, core.RunModeDrainAndExit)
	if err != nil {
		t.Fatalf("bootCore: %v", err)
	}
	defer func() { _ = storeClose() }()
	defer func() { _ = svc.Close() }()

	init := floatGaugeBySource(t, reader, metrics.MetricSourceLastSuccess)
	for _, src := range []string{"healthy", "broken"} {
		if init[src] < float64(before.Unix()) || init[src] > float64(time.Now().Unix())+1 {
			t.Fatalf("last-success[%s] = %v at boot, want process start (~now), never 0", src, init[src])
		}
	}
	iv := floatGaugeBySource(t, reader, metrics.MetricSourceExpectedInterval)
	if iv["healthy"] != 60 || iv["broken"] != 1800 {
		t.Fatalf("expected-interval = %v, want healthy 60s and broken 1800s", iv)
	}

	time.Sleep(20 * time.Millisecond)
	if _, err := o.ProduceTick(ctx, q); err != nil {
		t.Fatalf("ProduceTick: %v", err)
	}
	after := floatGaugeBySource(t, reader, metrics.MetricSourceLastSuccess)
	if after["healthy"] <= init["healthy"] {
		t.Errorf("healthy last-success %v did not advance past %v after a successful pass", after["healthy"], init["healthy"])
	}
	if after["broken"] != init["broken"] {
		t.Errorf("broken last-success moved %v -> %v on a failed pass; a failure must not advance it", init["broken"], after["broken"])
	}
}
