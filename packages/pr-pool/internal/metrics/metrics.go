// Package metrics emits the three metrics the core's catalog MUST declare
// (INV-OBS-1): queue depth (gauge, per type), failure rate (counter, per
// failure class), and unconsumed-expired (counter, per type — the "no event
// misses" signal, INV-DISP-3). OTel is the default emission transport for
// metrics only (a neutral standard, not a mandated backend — GOAL-MIN-1); the
// concrete sink is a deployment binding via INTF-MON.
//
// The Emitter implements eventqueue.Observer, so the queue drives the metrics
// end to end: unconsumed-expired fires from the queue's ttl-expiry path, and
// the depth gauge reads the queue's live per-type depth on collect. Failure
// rate is fed from handler session-status failures via RecordFailure.
//
// This is bead pg2-hvlyj.18 (plan item 5.6). Its statement coverage is gated at
// >=80% by the `pr-pool-go-tests` flake check (bead pg2-hvlyj.19).
package metrics

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/phillipgreenii/pr-pool/internal/eventqueue"
)

// Metric names (the declared catalog, INV-OBS-1).
const (
	MetricQueueDepth        = "pr_pool.queue_depth"
	MetricFailures          = "pr_pool.failures"
	MetricUnconsumedExpired = "pr_pool.unconsumed_expired"
)

// Emitter emits the core's declared metric catalog over an OTel meter. It
// implements eventqueue.Observer.
type Emitter struct {
	failures   metric.Int64Counter
	unconsumed metric.Int64Counter
}

// Ensure the queue can drive it.
var _ eventqueue.Observer = (*Emitter)(nil)

// New registers the three instruments on a meter from mp and returns an Emitter.
// depthFn supplies the current per-type queue depth (typically queue.DepthByType)
// — the observable gauge reads it on each collect, so the gauge tracks
// enqueue/accept/expire without the queue pushing updates.
func New(mp metric.MeterProvider, depthFn func() map[string]int) (*Emitter, error) {
	m := mp.Meter("github.com/phillipgreenii/pr-pool")

	failures, err := m.Int64Counter(
		MetricFailures,
		metric.WithUnit("{failure}"),
		metric.WithDescription("handler-reported failures, per failure class (INV-OBS-1)"),
	)
	if err != nil {
		return nil, err
	}
	unconsumed, err := m.Int64Counter(
		MetricUnconsumedExpired,
		metric.WithUnit("{event}"),
		metric.WithDescription("events that reached ttl with no handler accepting them, per type (INV-DISP-3)"),
	)
	if err != nil {
		return nil, err
	}
	if _, err := m.Int64ObservableGauge(
		MetricQueueDepth,
		metric.WithUnit("{event}"),
		metric.WithDescription("retained non-expired events in the queue, per type (INV-OBS-1)"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			for typ, depth := range depthFn() {
				o.Observe(int64(depth), metric.WithAttributes(attribute.String("type", typ)))
			}
			return nil
		}),
	); err != nil {
		return nil, err
	}

	return &Emitter{failures: failures, unconsumed: unconsumed}, nil
}

// OnEnqueue / OnAccept are no-ops: queue depth is observed via the gauge
// callback (reading the live depth), not pushed per event.
func (e *Emitter) OnEnqueue(eventqueue.Event) {}
func (e *Emitter) OnAccept(_, _ string)       {}

// OnUnconsumedExpired increments the unconsumed-expired counter for the event's
// type — the concrete "no event misses" signal (INV-DISP-3), fired from the
// queue's ttl-expiry path.
func (e *Emitter) OnUnconsumedExpired(evtType string) {
	e.unconsumed.Add(context.Background(), 1, metric.WithAttributes(attribute.String("type", evtType)))
}

// RecordFailure increments the failure-rate counter for a handler-reported
// failure class (INV-FAIL-1: retryable / resource-limit / unavailable /
// critical), fed from session-status failures.
func (e *Emitter) RecordFailure(class string) {
	e.failures.Add(context.Background(), 1, metric.WithAttributes(attribute.String("class", class)))
}
