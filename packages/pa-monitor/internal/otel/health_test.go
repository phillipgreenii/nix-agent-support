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
