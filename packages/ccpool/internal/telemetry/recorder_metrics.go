// recorder_metrics.go: the two counters of ccpool's recorder reads. Every fact
// read retries (internal/facts); these count the retries and the reads that
// still failed after the last attempt, so an operator can see a flapping or
// absent recorder without reading logs. Like the capacity gauge, this file is
// kept apart from metrics.go, and the instruments are fetched from the provider
// telemetry.Init installed at record time (the meter caches them by name).
package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Exact OTLP names of the recorder-read counters. Attribute: op (the read's
// operation name, a small fixed set: query, hello, sessions.facts, ...).
const (
	RecorderRetriesMetric      = "ccpool_recorder_retries_total"
	RecorderReadFailuresMetric = "ccpool_recorder_read_failures_total"
)

// RecordRecorderRetryTo adds one to ccpool_recorder_retries_total{op} on meter.
func RecordRecorderRetryTo(ctx context.Context, meter metric.Meter, op string) error {
	c, err := meter.Int64Counter(RecorderRetriesMetric,
		metric.WithDescription("Recorder reads retried after a failed attempt, by operation."))
	if err != nil {
		return err
	}
	c.Add(ctx, 1, metric.WithAttributes(attribute.String("op", op)))
	return nil
}

// RecordRecorderReadFailureTo adds one to ccpool_recorder_read_failures_total{op}
// on meter.
func RecordRecorderReadFailureTo(ctx context.Context, meter metric.Meter, op string) error {
	c, err := meter.Int64Counter(RecorderReadFailuresMetric,
		metric.WithDescription("Recorder reads that failed after their last attempt, by operation."))
	if err != nil {
		return err
	}
	c.Add(ctx, 1, metric.WithAttributes(attribute.String("op", op)))
	return nil
}

// RecordRecorderRetry records one retry against the installed provider. A
// registration failure is dropped: a metric never fails a read.
func RecordRecorderRetry(op string) {
	_ = RecordRecorderRetryTo(context.Background(), MeterProvider().Meter(scopeName), op)
}

// RecordRecorderReadFailure records one final failure against the installed
// provider (see RecordRecorderRetry).
func RecordRecorderReadFailure(op string) {
	_ = RecordRecorderReadFailureTo(context.Background(), MeterProvider().Meter(scopeName), op)
}
