package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// TestMetricsRegistration_NamesTypesAndLabels drives newMetricsInstruments
// directly against a fresh, test-local SDK meter (a ManualReader, no OTLP
// network involved) and asserts each of the seven D6 instruments is
// registered under exactly its specified name, aggregation type
// (Sum=counter, Gauge=gauge), and label set. This is independent of the
// package-level lazy singleton (ensureInstruments/instrumentsOnce) — it
// never touches it — so it is unaffected by whatever order Go runs the
// other tests in this package in.
func TestMetricsRegistration_NamesTypesAndLabels(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() { _ = mp.Shutdown(context.Background()) }()

	inst, err := newMetricsInstruments(mp.Meter("test"))
	if err != nil {
		t.Fatalf("newMetricsInstruments: %v", err)
	}

	ctx := context.Background()
	inst.retriesTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("class", "network")))
	inst.retryExhaustedTotal.Add(ctx, 1)
	inst.cancelTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "success")))
	inst.reapClosuresTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", "idle_ttl")))
	inst.reapPhantomPrunedTotal.Add(ctx, 1)
	inst.sessionsPreservedForHuman.Record(ctx, 3)
	inst.launchOutcomeTotal.Add(ctx, 1,
		metric.WithAttributes(attribute.String("route", "resume"), attribute.String("outcome", "success")))

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	got := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			got[m.Name] = m
		}
	}

	cases := []struct {
		name        string
		wantCounter bool // true = Sum (counter), false = Gauge
		wantLabels  []string
	}{
		{"ccpool_retries_total", true, []string{"class"}},
		{"ccpool_retry_exhausted_total", true, nil},
		{"ccpool_cancel_total", true, []string{"outcome"}},
		{"ccpool_reap_closures_total", true, []string{"reason"}},
		{"ccpool_reap_phantom_pruned_total", true, nil},
		{"ccpool_sessions_preserved_for_human", false, nil},
		{"ccpool_launch_outcome_total", true, []string{"route", "outcome"}},
	}

	for _, c := range cases {
		m, ok := got[c.name]
		if !ok {
			t.Errorf("instrument %q not registered/collected", c.name)
			continue
		}

		var attrs attribute.Set
		switch data := m.Data.(type) {
		case metricdata.Sum[int64]:
			if !c.wantCounter {
				t.Errorf("%s: got Sum (counter), want Gauge", c.name)
			}
			if len(data.DataPoints) != 1 {
				t.Errorf("%s: got %d data points, want 1", c.name, len(data.DataPoints))
				continue
			}
			attrs = data.DataPoints[0].Attributes
		case metricdata.Gauge[int64]:
			if c.wantCounter {
				t.Errorf("%s: got Gauge, want Sum (counter)", c.name)
			}
			if len(data.DataPoints) != 1 {
				t.Errorf("%s: got %d data points, want 1", c.name, len(data.DataPoints))
				continue
			}
			attrs = data.DataPoints[0].Attributes
		default:
			t.Errorf("%s: unexpected aggregation type %T", c.name, m.Data)
			continue
		}

		if got, want := attrs.Len(), len(c.wantLabels); got != want {
			t.Errorf("%s: got %d labels, want %d (%v)", c.name, got, want, c.wantLabels)
		}
		for _, label := range c.wantLabels {
			if !attrs.HasValue(attribute.Key(label)) {
				t.Errorf("%s: missing expected label %q", c.name, label)
			}
		}
	}
}

// TestMetrics_NoInit_RecordFunctionsAreSafe calls every Record* function
// before telemetry.Init has ever run in this process, mirroring
// TestMeterProvider_NoInit_ReturnsUsableNoop in telemetry_test.go: the
// package-level MeterProvider defaults to a usable no-op even pre-Init, so
// every Record* call here MUST complete without panicking.
func TestMetrics_NoInit_RecordFunctionsAreSafe(t *testing.T) {
	recordAllForTest()
}

// TestMetrics_AfterNoopInit_RecordFunctionsAreSafe calls Init with no OTLP
// endpoint configured (installing the no-op MeterProvider explicitly, per
// Init's own documented no-endpoint behaviour — mirrors
// TestInit_NoEndpoint_InstallsNoopLoggerProvider in telemetry_test.go) and
// then exercises every Record* function, asserting the same no-panic safety.
func TestMetrics_AfterNoopInit_RecordFunctionsAreSafe(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	shutdown := Init(context.Background())
	defer func() { _ = shutdown(context.Background()) }()

	recordAllForTest()
}

// recordAllForTest exercises every exported Record* function once with
// representative arguments. Shared by both no-op/safe-fallback tests above.
func recordAllForTest() {
	RecordRetry("network")
	RecordRetryExhausted()
	RecordCancel("success")
	RecordReapClosure("idle_ttl")
	RecordReapPhantomPruned()
	RecordSessionsPreservedForHuman(1)
	RecordLaunchOutcome("resume", "success")
}
