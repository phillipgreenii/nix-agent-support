package otel

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// defaultHealthThrottle bounds how often a still-UNHEALTHY summary is repeated
// while an outage persists. The first failure of a streak and the recovery are
// always surfaced; intermediate failures collapse into at most one line per
// window, so a multi-hour outage produces a handful of lines rather than one
// per export attempt.
const defaultHealthThrottle = 5 * time.Minute

// exportHealth tracks OTLP export success/failure and renders a throttled,
// human-readable summary. It is the unit-tested core of the export-health
// surface (pg2-waji); the OTel wiring around it is deliberately thin.
//
// Motivation: when the OTLP collector is down, the only prior signal was the
// SDK's own per-attempt "failed to upload metrics" line in launchd-stderr.log
// — easy to miss, and emitted once per export forever while every metric/log
// was silently dropped (the multi-week pg2-e9ga gap). This tracker replaces
// that unmanaged spam with (a) one line when export first goes UNHEALTHY, (b)
// at most one line per throttle window while it stays unhealthy, and (c) one
// RECOVERED line when export succeeds again.
//
// It records neither metric data nor context — it is pure state plus string
// rendering — so tests drive it with a fake clock and assert on the output.
// All methods are safe for concurrent use.
type exportHealth struct {
	now      func() time.Time
	throttle time.Duration

	mu               sync.Mutex
	consecutiveFails int       // failures in the current unhealthy streak; 0 == healthy
	streakStart      time.Time // time of the first failure in the current streak
	lastErr          string    // most recent non-empty export error text
	lastSummaryAt    time.Time // throttle anchor for repeated UNHEALTHY lines
	lastSuccessAt    time.Time // time of the most recent successful export
}

// newExportHealth builds a tracker. now defaults to time.Now and throttle to
// defaultHealthThrottle when the zero/nil value is supplied.
func newExportHealth(now func() time.Time, throttle time.Duration) *exportHealth {
	if now == nil {
		now = time.Now
	}
	if throttle <= 0 {
		throttle = defaultHealthThrottle
	}
	return &exportHealth{now: now, throttle: throttle}
}

// onFailure records one failed export attempt. It returns a summary line and
// true when the throttle policy says the failure should be surfaced (the first
// failure of a streak, or once per throttle window thereafter); otherwise it
// returns ("", false).
func (h *exportHealth) onFailure(err error) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.consecutiveFails++
	if h.consecutiveFails == 1 {
		h.streakStart = now
	}
	if err != nil {
		h.lastErr = err.Error()
	}
	firstOfStreak := h.consecutiveFails == 1
	throttleElapsed := now.Sub(h.lastSummaryAt) >= h.throttle
	if !firstOfStreak && !throttleElapsed {
		return "", false
	}
	h.lastSummaryAt = now
	return h.unhealthyLineLocked(now), true
}

// onSuccess records one successful export attempt. When it ends an unhealthy
// streak it returns a recovery line and true; on a steady-healthy export it
// returns ("", false).
func (h *exportHealth) onSuccess() (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.lastSuccessAt = now
	if h.consecutiveFails == 0 {
		return "", false
	}
	n := h.consecutiveFails
	start := h.streakStart
	lastErr := h.lastErr
	h.consecutiveFails = 0
	h.streakStart = time.Time{}
	h.lastSummaryAt = time.Time{}
	line := fmt.Sprintf(
		"pa-monitor: OTel export RECOVERED after %d consecutive failure(s); was unhealthy for %s (since %s); last error: %s",
		n, now.Sub(start).Round(time.Second), start.Format(time.RFC3339), lastErr,
	)
	return line, true
}

// unhealthyLineLocked renders the UNHEALTHY summary. Caller holds h.mu.
func (h *exportHealth) unhealthyLineLocked(now time.Time) string {
	return fmt.Sprintf(
		"pa-monitor: OTel export UNHEALTHY: %d consecutive failure(s) since %s (%s ago); last error: %s",
		h.consecutiveFails, h.streakStart.Format(time.RFC3339), now.Sub(h.streakStart).Round(time.Second), h.lastErr,
	)
}

// healthy reports whether the most recent export attempt succeeded (no active
// failure streak). Safe for concurrent use.
func (h *exportHealth) healthy() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.consecutiveFails == 0
}

// record applies one export result to the tracker and writes any resulting
// summary line to out. Shared by the metric and log exporter decorators.
func (h *exportHealth) record(err error, out io.Writer) {
	var (
		line string
		emit bool
	)
	if err != nil {
		line, emit = h.onFailure(err)
	} else {
		line, emit = h.onSuccess()
	}
	if emit && out != nil {
		fmt.Fprintln(out, line)
	}
}

// recordExport is the shared instrumentation Strategy for both export
// decorators (pg2-sewtz): it times one OTLP export call and records the
// outcome to the two back-patched instruments (Task 1's exportDur /
// exportAttempts). It is a deliberate no-op whenever EITHER instrument is
// nil — the state before New's back-patch assigns them (or when OTel
// instrumentation of itself is otherwise disabled) — so callers never need
// to guard the call site.
func recordExport(dur metric.Float64Histogram, attempts metric.Int64Counter, signal string, d time.Duration, err error) {
	if dur == nil || attempts == nil {
		return
	}
	outcome := "success"
	if err != nil {
		outcome = "failure"
	}
	dur.Record(context.Background(), durMS(d),
		metric.WithAttributes(attribute.String("signal", signal)))
	attempts.Add(context.Background(), 1,
		metric.WithAttributes(attribute.String("signal", signal), attribute.String("outcome", outcome)))
}

// healthMetricExporter decorates a metric Exporter (Decorator pattern), feeding
// each export result to the shared exportHealth tracker and SWALLOWING the
// underlying error.
//
// Swallowing is intentional. The metric data is already lost when the collector
// is down — returning the error to the PeriodicReader changes nothing about
// delivery; it only makes the SDK route the error to the global error handler,
// whose default behaviour is the per-attempt stderr line this feature replaces.
// Returning nil suppresses that raw spam and lets the tracker own reporting via
// its throttled UNHEALTHY / RECOVERED summary. The full error text is preserved
// in the tracker and surfaced in the summary line. Non-Export methods
// (Temporality, Aggregation, ForceFlush, Shutdown) pass through the embedded
// exporter unchanged.
type healthMetricExporter struct {
	sdkmetric.Exporter
	health   *exportHealth
	out      io.Writer
	dur      metric.Float64Histogram // nil until back-patched by New (pg2-sewtz)
	attempts metric.Int64Counter     // nil until back-patched by New (pg2-sewtz)
}

func (e *healthMetricExporter) Export(ctx context.Context, rm *metricdata.ResourceMetrics) error {
	start := time.Now()
	err := e.Exporter.Export(ctx, rm)
	recordExport(e.dur, e.attempts, "metric", time.Since(start), err)
	e.health.record(err, e.out)
	return nil
}

// healthLogExporter is the log-exporter counterpart to healthMetricExporter,
// with the same swallow-and-track contract. Sharing the tracker with the metric
// decorator is safe: both target the same OTLP endpoint, so they transition
// together and the "consecutive failures" count reflects overall export health.
type healthLogExporter struct {
	sdklog.Exporter
	health   *exportHealth
	out      io.Writer
	dur      metric.Float64Histogram // nil until back-patched by New (pg2-sewtz)
	attempts metric.Int64Counter     // nil until back-patched by New (pg2-sewtz)
}

func (e *healthLogExporter) Export(ctx context.Context, records []sdklog.Record) error {
	start := time.Now()
	err := e.Exporter.Export(ctx, records)
	recordExport(e.dur, e.attempts, "log", time.Since(start), err)
	e.health.record(err, e.out)
	return nil
}
