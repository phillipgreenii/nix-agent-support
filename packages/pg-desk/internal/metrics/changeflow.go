package metrics

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
)

// The change-flow catalog (entity-change-flow design, observability). Label
// vocabulary: type, kind, origin, consumer.
const (
	// MetricChangeRecords is the number of change_log records currently
	// retained, by type, kind and origin. A gauge, not a counter: the log is
	// pruned, so the count can fall.
	MetricChangeRecords = "pg_desk_change_log_records"
	// MetricHydrations counts hydrations by type. The totals are persisted by
	// the changes/refresh CLI processes and read here, so this is an
	// observable counter whose value comes from the store, not from serve.
	MetricHydrations = "pg_desk_hydrations"
	// MetricHydrationFailures counts hydrations that errored or came back
	// degraded, by type (persisted, like MetricHydrations).
	MetricHydrationFailures = "pg_desk_hydration_failures"
	// MetricOCCRetries counts optimistic-concurrency retries, by type
	// (persisted, like MetricHydrations).
	MetricOCCRetries = "pg_desk_occ_retries"
	// MetricDueBacklog is the number of active entities whose hydrated_at is
	// older than the sweep bound D (or that were never hydrated), by type.
	MetricDueBacklog = "pg_desk_due_backlog"
	// MetricConsumerLag is max(seq) - cursor per consumer and type.
	MetricConsumerLag = "pg_desk_consumer_lag"
	// MetricRepeatedDegraded is the number of entities with repeated
	// degraded hydrations (changes.RepeatedDegraded), by type.
	MetricRepeatedDegraded = "pg_desk_repeated_degraded_entities"
)

// ChangeFlowFunc supplies the change-flow observation at collect time. It
// MUST return a zero Flow (Migrated false) for a store that is not on the new
// schema: the catalog then emits no change-flow series rather than failing the
// scrape.
type ChangeFlowFunc func() (changes.Flow, error)

// RegisterChangeFlow registers the change-flow instruments on a meter from mp.
// One callback reads the observation once per scrape and feeds every
// instrument, so the families always agree with each other. It is separate
// from New so the existing dashboard catalog is untouched.
func RegisterChangeFlow(mp metric.MeterProvider, fn ChangeFlowFunc) error {
	m := mp.Meter("github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk")

	records, err := m.Int64ObservableGauge(MetricChangeRecords,
		metric.WithDescription("change_log records currently retained, by type, kind and origin"))
	if err != nil {
		return err
	}
	hydrations, err := m.Int64ObservableCounter(MetricHydrations,
		metric.WithDescription("entity hydrations by type (persisted by the changes/refresh processes)"))
	if err != nil {
		return err
	}
	failures, err := m.Int64ObservableCounter(MetricHydrationFailures,
		metric.WithDescription("hydrations that errored or returned degraded, by type (persisted)"))
	if err != nil {
		return err
	}
	retries, err := m.Int64ObservableCounter(MetricOCCRetries,
		metric.WithDescription("optimistic-concurrency retries on entity writes, by type (persisted)"))
	if err != nil {
		return err
	}
	due, err := m.Int64ObservableGauge(MetricDueBacklog,
		metric.WithDescription("active entities with hydrated_at older than the sweep max age (or never hydrated), by type"))
	if err != nil {
		return err
	}
	lag, err := m.Int64ObservableGauge(MetricConsumerLag,
		metric.WithDescription("max(change_log seq) - consumer cursor, by consumer and type"))
	if err != nil {
		return err
	}
	repeated, err := m.Int64ObservableGauge(MetricRepeatedDegraded,
		metric.WithDescription("entities with repeated degraded hydrations, by type"))
	if err != nil {
		return err
	}

	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		flow, err := fn()
		if err != nil {
			return err
		}
		if !flow.Migrated {
			return nil // degrade: no change-flow series on an unmigrated store
		}
		for _, t := range flow.Types {
			typ := attribute.String("type", t.Type)
			for _, r := range t.Records {
				o.ObserveInt64(records, r.Count, metric.WithAttributes(typ,
					attribute.String("kind", r.Kind), attribute.String("origin", r.Origin)))
			}
			o.ObserveInt64(hydrations, t.Stats.Hydrations, metric.WithAttributes(typ))
			o.ObserveInt64(failures, t.Stats.Failures, metric.WithAttributes(typ))
			o.ObserveInt64(retries, t.Stats.OCCRetries, metric.WithAttributes(typ))
			o.ObserveInt64(due, int64(t.Due), metric.WithAttributes(typ))
			o.ObserveInt64(repeated, int64(len(t.Repeated)), metric.WithAttributes(typ))
		}
		for _, c := range flow.Consumers {
			o.ObserveInt64(lag, c.Lag, metric.WithAttributes(
				attribute.String("type", c.Type), attribute.String("consumer", c.Name),
			))
		}
		return nil
	}, records, hydrations, failures, retries, due, lag, repeated)
	return err
}
