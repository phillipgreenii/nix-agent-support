package otel

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// fakeClock is an injectable, non-advancing clock for the export-health tests.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }
func newFakeClock() *fakeClock               { return &fakeClock{t: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)} }

// TestExportHealth_HealthyByDefault: a fresh tracker reports healthy and a
// steady-healthy export produces no summary line.
func TestExportHealth_HealthyByDefault(t *testing.T) {
	h := newExportHealth(newFakeClock().now, time.Minute)
	if !h.healthy() {
		t.Fatal("fresh tracker should be healthy")
	}
	if line, emit := h.onSuccess(); emit {
		t.Errorf("steady-healthy success should not emit, got %q", line)
	}
}

// TestExportHealth_FirstFailureEmitsUnhealthy: the first failure of a streak is
// always surfaced, names the failure count and the error, and flips healthy().
func TestExportHealth_FirstFailureEmitsUnhealthy(t *testing.T) {
	clk := newFakeClock()
	h := newExportHealth(clk.now, 5*time.Minute)
	line, emit := h.onFailure(errors.New("connection refused"))
	if !emit {
		t.Fatal("first failure of a streak must emit")
	}
	for _, want := range []string{"UNHEALTHY", "1 consecutive", "connection refused"} {
		if !strings.Contains(line, want) {
			t.Errorf("unhealthy line %q missing %q", line, want)
		}
	}
	if h.healthy() {
		t.Error("tracker should be unhealthy after a failure")
	}
}

// TestExportHealth_ThrottlesRepeatedFailures: within the throttle window,
// subsequent failures are counted but NOT emitted; once the window elapses the
// next failure emits again with the accumulated count.
func TestExportHealth_ThrottlesRepeatedFailures(t *testing.T) {
	clk := newFakeClock()
	h := newExportHealth(clk.now, 5*time.Minute)

	if _, emit := h.onFailure(errors.New("boom1")); !emit {
		t.Fatal("failure #1 must emit")
	}
	// Two more failures inside the window: silent.
	clk.advance(time.Minute)
	if line, emit := h.onFailure(errors.New("boom2")); emit {
		t.Errorf("failure #2 within window should be throttled, got %q", line)
	}
	clk.advance(time.Minute)
	if line, emit := h.onFailure(errors.New("boom3")); emit {
		t.Errorf("failure #3 within window should be throttled, got %q", line)
	}
	// Cross the throttle window: next failure emits with the running count.
	clk.advance(5 * time.Minute)
	line, emit := h.onFailure(errors.New("boom4"))
	if !emit {
		t.Fatal("failure after throttle window must emit")
	}
	if !strings.Contains(line, "4 consecutive") {
		t.Errorf("expected accumulated count in %q", line)
	}
	if !strings.Contains(line, "boom4") {
		t.Errorf("expected latest error in %q", line)
	}
}

// TestExportHealth_RecoveryEmitsOnceThenSilent: the first success after an
// unhealthy streak emits a RECOVERED line naming the failure count, and further
// successes are silent. A new failure afterwards starts a fresh streak (count
// resets, so it emits again as failure #1).
func TestExportHealth_RecoveryEmitsOnceThenSilent(t *testing.T) {
	clk := newFakeClock()
	h := newExportHealth(clk.now, 5*time.Minute)

	h.onFailure(errors.New("down"))
	clk.advance(time.Minute)
	h.onFailure(errors.New("still down")) // throttled, count -> 2

	clk.advance(2 * time.Minute)
	line, emit := h.onSuccess()
	if !emit {
		t.Fatal("first success after a streak must emit RECOVERED")
	}
	for _, want := range []string{"RECOVERED", "2 consecutive", "still down"} {
		if !strings.Contains(line, want) {
			t.Errorf("recovery line %q missing %q", line, want)
		}
	}
	if !h.healthy() {
		t.Error("tracker should be healthy after recovery")
	}
	// Steady healthy: no further emission.
	if line, emit := h.onSuccess(); emit {
		t.Errorf("steady-healthy success should be silent, got %q", line)
	}
	// A brand-new failure streak emits again as failure #1.
	clk.advance(time.Minute)
	line, emit = h.onFailure(errors.New("down again"))
	if !emit || !strings.Contains(line, "1 consecutive") {
		t.Errorf("new streak should emit as failure #1, got emit=%v line=%q", emit, line)
	}
}

// erroringMetricExporter is a metric Exporter whose Export returns a scripted
// error, letting the decorator test drive failure then recovery.
type erroringMetricExporter struct{ err error }

func (e *erroringMetricExporter) Temporality(k sdkmetric.InstrumentKind) metricdata.Temporality {
	return sdkmetric.DefaultTemporalitySelector(k)
}

func (e *erroringMetricExporter) Aggregation(k sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	return sdkmetric.DefaultAggregationSelector(k)
}

func (e *erroringMetricExporter) Export(context.Context, *metricdata.ResourceMetrics) error {
	return e.err
}
func (e *erroringMetricExporter) ForceFlush(context.Context) error { return nil }
func (e *erroringMetricExporter) Shutdown(context.Context) error   { return nil }

// TestHealthMetricExporter_SwallowsAndSummarizes: the decorator always returns
// nil (swallowing the raw export error so the SDK never spams), records the
// result in the shared tracker, and writes the throttled summary to out.
func TestHealthMetricExporter_SwallowsAndSummarizes(t *testing.T) {
	clk := newFakeClock()
	h := newExportHealth(clk.now, time.Minute)
	var buf bytes.Buffer
	inner := &erroringMetricExporter{err: errors.New("collector refused")}
	dec := &healthMetricExporter{Exporter: inner, health: h, out: &buf}

	if err := dec.Export(context.Background(), &metricdata.ResourceMetrics{}); err != nil {
		t.Fatalf("decorator must swallow the export error, got %v", err)
	}
	if h.healthy() {
		t.Error("tracker should be unhealthy after a failed export")
	}
	if !strings.Contains(buf.String(), "UNHEALTHY") || !strings.Contains(buf.String(), "collector refused") {
		t.Errorf("expected UNHEALTHY summary on stderr, got %q", buf.String())
	}

	// Collector recovers: next export succeeds, decorator emits RECOVERED.
	buf.Reset()
	clk.advance(2 * time.Minute)
	inner.err = nil
	if err := dec.Export(context.Background(), &metricdata.ResourceMetrics{}); err != nil {
		t.Fatalf("successful export must return nil, got %v", err)
	}
	if !h.healthy() {
		t.Error("tracker should be healthy after a successful export")
	}
	if !strings.Contains(buf.String(), "RECOVERED") {
		t.Errorf("expected RECOVERED summary on stderr, got %q", buf.String())
	}
}

// erroringLogExporter is the log-side counterpart to erroringMetricExporter.
type erroringLogExporter struct{ err error }

func (e *erroringLogExporter) Export(context.Context, []sdklog.Record) error { return e.err }
func (e *erroringLogExporter) Shutdown(context.Context) error                { return nil }
func (e *erroringLogExporter) ForceFlush(context.Context) error              { return nil }

// TestHealthLogExporter_SwallowsAndSummarizes mirrors the metric-decorator test
// for the log exporter path.
func TestHealthLogExporter_SwallowsAndSummarizes(t *testing.T) {
	clk := newFakeClock()
	h := newExportHealth(clk.now, time.Minute)
	var buf bytes.Buffer
	inner := &erroringLogExporter{err: errors.New("log endpoint down")}
	dec := &healthLogExporter{Exporter: inner, health: h, out: &buf}

	if err := dec.Export(context.Background(), nil); err != nil {
		t.Fatalf("decorator must swallow the export error, got %v", err)
	}
	if !strings.Contains(buf.String(), "UNHEALTHY") || !strings.Contains(buf.String(), "log endpoint down") {
		t.Errorf("expected UNHEALTHY summary on stderr, got %q", buf.String())
	}
}

// TestExportDecoratorNoopWhenInstrumentsNil is the disabled-path contract
// (pg2-sewtz): recordExport itself must no-op whenever EITHER instrument is
// nil (not just when both are), and a decorator built without instruments
// set (the state before New back-patches them) must still swallow the error
// and write the health line exactly as before this feature existed.
func TestExportDecoratorNoopWhenInstrumentsNil(t *testing.T) {
	// Helper-level: nil/nil, and each single-nil combination, must not panic.
	recordExport(nil, nil, "metric", time.Millisecond, nil)
	recordExport(nil, nil, "log", time.Millisecond, errors.New("boom"))

	e, _ := newTestEmitter(t)
	if e.exportDur == nil || e.exportAttempts == nil {
		t.Fatal("newTestEmitter's registerMetrics must create exportDur/exportAttempts")
	}
	recordExport(nil, e.exportAttempts, "metric", time.Millisecond, nil)
	recordExport(e.exportDur, nil, "metric", time.Millisecond, nil)

	// Decorator-level: zero-value dur/attempts fields (pre-back-patch) must
	// leave the pre-existing swallow + health-line behavior unchanged.
	clk := newFakeClock()
	h := newExportHealth(clk.now, time.Minute)
	var buf bytes.Buffer
	inner := &erroringMetricExporter{err: errors.New("boom")}
	dec := &healthMetricExporter{Exporter: inner, health: h, out: &buf}

	if err := dec.Export(context.Background(), &metricdata.ResourceMetrics{}); err != nil {
		t.Fatalf("decorator must swallow even with nil instruments, got %v", err)
	}
	if !strings.Contains(buf.String(), "UNHEALTHY") {
		t.Errorf("expected UNHEALTHY summary even with nil instruments, got %q", buf.String())
	}
}

// TestExportDecoratorRecordsWhenInstrumentsSet drives the metric decorator
// through a failure then a success with the REAL exportDur/exportAttempts
// instruments (via newTestEmitter's ManualReader-backed Emitter, reused from
// emitter_test.go) and asserts: Export still swallows the error (returns
// nil) and still writes the health line (UNCHANGED behavior), AND the
// duration histogram + attempts counter now record one data point per call
// with signal="metric" and the right outcome.
func TestExportDecoratorRecordsWhenInstrumentsSet(t *testing.T) {
	e, reader := newTestEmitter(t)

	clk := newFakeClock()
	h := newExportHealth(clk.now, time.Minute)
	var buf bytes.Buffer
	inner := &erroringMetricExporter{err: errors.New("collector refused")}
	dec := &healthMetricExporter{
		Exporter: inner,
		health:   h,
		out:      &buf,
		dur:      e.exportDur,
		attempts: e.exportAttempts,
	}

	if err := dec.Export(context.Background(), &metricdata.ResourceMetrics{}); err != nil {
		t.Fatalf("decorator must swallow the export error, got %v", err)
	}
	clk.advance(2 * time.Minute)
	inner.err = nil
	if err := dec.Export(context.Background(), &metricdata.ResourceMetrics{}); err != nil {
		t.Fatalf("successful export must return nil, got %v", err)
	}
	if !strings.Contains(buf.String(), "RECOVERED") {
		t.Errorf("expected RECOVERED summary on stderr (health-line behavior unchanged), got %q", buf.String())
	}

	durMetric, ok := collectMetric(t, reader, "pa_monitor.otel.export.duration")
	if !ok {
		t.Fatal("pa_monitor.otel.export.duration not emitted")
	}
	durHist, ok := durMetric.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("export.duration is %T, want metricdata.Histogram[float64]", durMetric.Data)
	}
	var metricSignalCount uint64
	for _, dp := range durHist.DataPoints {
		if sig, present := dp.Attributes.Value("signal"); present && sig.AsString() == "metric" {
			metricSignalCount += dp.Count
		}
	}
	if metricSignalCount != 2 {
		t.Errorf("export.duration signal=metric count = %d, want 2 (one per Export call)", metricSignalCount)
	}

	attemptsMetric, ok := collectMetric(t, reader, "pa_monitor.otel.export.attempts_total")
	if !ok {
		t.Fatal("pa_monitor.otel.export.attempts_total not emitted")
	}
	attemptsSum, ok := attemptsMetric.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("export.attempts_total is %T, want metricdata.Sum[int64]", attemptsMetric.Data)
	}
	outcomes := map[string]int64{}
	for _, dp := range attemptsSum.DataPoints {
		sig, _ := dp.Attributes.Value("signal")
		if sig.AsString() != "metric" {
			continue
		}
		outcome, _ := dp.Attributes.Value("outcome")
		outcomes[outcome.AsString()] += dp.Value
	}
	if outcomes["failure"] != 1 || outcomes["success"] != 1 {
		t.Errorf("export.attempts_total signal=metric outcomes = %+v, want failure=1 success=1", outcomes)
	}
}

// TestExportDecoratorLogRecordsWhenInstrumentsSet is the log-decorator
// counterpart, asserting signal="log" on the shared instruments and the same
// unchanged swallow / health-line contract.
func TestExportDecoratorLogRecordsWhenInstrumentsSet(t *testing.T) {
	e, reader := newTestEmitter(t)

	clk := newFakeClock()
	h := newExportHealth(clk.now, time.Minute)
	var buf bytes.Buffer
	inner := &erroringLogExporter{err: errors.New("log endpoint down")}
	dec := &healthLogExporter{
		Exporter: inner,
		health:   h,
		out:      &buf,
		dur:      e.exportDur,
		attempts: e.exportAttempts,
	}

	if err := dec.Export(context.Background(), nil); err != nil {
		t.Fatalf("decorator must swallow the export error, got %v", err)
	}
	clk.advance(2 * time.Minute)
	inner.err = nil
	if err := dec.Export(context.Background(), nil); err != nil {
		t.Fatalf("successful export must return nil, got %v", err)
	}
	if !strings.Contains(buf.String(), "RECOVERED") {
		t.Errorf("expected RECOVERED summary on stderr (health-line behavior unchanged), got %q", buf.String())
	}

	// export.duration histogram: mirror the metric-decorator assertion above,
	// but for signal="log" — one data point per Export call (failure + success).
	durMetric, ok := collectMetric(t, reader, "pa_monitor.otel.export.duration")
	if !ok {
		t.Fatal("pa_monitor.otel.export.duration not emitted")
	}
	durHist, ok := durMetric.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("export.duration is %T, want metricdata.Histogram[float64]", durMetric.Data)
	}
	var logSignalCount uint64
	for _, dp := range durHist.DataPoints {
		if sig, present := dp.Attributes.Value("signal"); present && sig.AsString() == "log" {
			logSignalCount += dp.Count
		}
	}
	if logSignalCount != 2 {
		t.Errorf("export.duration signal=log count = %d, want 2 (one per Export call)", logSignalCount)
	}

	attemptsMetric, ok := collectMetric(t, reader, "pa_monitor.otel.export.attempts_total")
	if !ok {
		t.Fatal("pa_monitor.otel.export.attempts_total not emitted")
	}
	attemptsSum, ok := attemptsMetric.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("export.attempts_total is %T, want metricdata.Sum[int64]", attemptsMetric.Data)
	}
	outcomes := map[string]int64{}
	for _, dp := range attemptsSum.DataPoints {
		sig, _ := dp.Attributes.Value("signal")
		if sig.AsString() != "log" {
			continue
		}
		outcome, _ := dp.Attributes.Value("outcome")
		outcomes[outcome.AsString()] += dp.Value
	}
	if outcomes["failure"] != 1 || outcomes["success"] != 1 {
		t.Errorf("export.attempts_total signal=log outcomes = %+v, want failure=1 success=1", outcomes)
	}
}

// TestExportDecoratorNoDeadlockOnReaderTriggeredExport exercises the REAL
// call chain the back-patch relies on being safe (per the task-5 brief):
// a PeriodicReader collects and then calls dec.Export, which now records
// into an instrument registered on the SAME MeterProvider that reader
// serves. If recording re-entered the reader/aggregation lock this would
// hang; ForceFlush is run on a goroutine with a hard timeout so a deadlock
// fails the test instead of hanging the suite.
func TestExportDecoratorNoDeadlockOnReaderTriggeredExport(t *testing.T) {
	clk := newFakeClock()
	h := newExportHealth(clk.now, time.Minute)
	var buf bytes.Buffer
	inner := &erroringMetricExporter{}
	dec := &healthMetricExporter{Exporter: inner, health: h, out: &buf}

	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(dec)))
	defer func() { _ = mp.Shutdown(context.Background()) }()

	meter := mp.Meter("test-deadlock")
	dur, err := meter.Float64Histogram("test.export.duration")
	if err != nil {
		t.Fatalf("Float64Histogram: %v", err)
	}
	attempts, err := meter.Int64Counter("test.export.attempts")
	if err != nil {
		t.Fatalf("Int64Counter: %v", err)
	}
	dec.dur, dec.attempts = dur, attempts

	done := make(chan error, 1)
	go func() { done <- mp.ForceFlush(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ForceFlush: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ForceFlush deadlocked: recording into the reader's own MeterProvider from inside Export is not safe")
	}
}
