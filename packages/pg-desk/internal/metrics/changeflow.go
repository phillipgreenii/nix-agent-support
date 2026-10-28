package metrics

import (
	"context"
	"fmt"
	"sort"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
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
	// MetricDueBacklog is the number of hydrations owed, by type: for thread,
	// the active entities whose hydrated_at is older than the sweep bound D (or
	// that were never hydrated); for issue (no age sweep), the watched active
	// entities never hydrated plus the failed-ingest entries; 0 for pr.
	MetricDueBacklog = "pg_desk_due_backlog"
	// MetricConsumerLag is max(seq) - cursor per consumer and type.
	MetricConsumerLag = "pg_desk_consumer_lag"
	// MetricRepeatedDegraded is the number of entities with repeated
	// degraded hydrations (changes.RepeatedDegraded), by type.
	MetricRepeatedDegraded = "pg_desk_repeated_degraded_entities"
	// MetricSweepBoundViolated is 1 for a (type, tier) whose sweep sizing
	// bound active_count / max_per_poll x poll_interval <= max_age is
	// violated and 0 when it holds. A tier whose poll interval is unknown has
	// NO series (never a false violation), and the issue type, which has no age
	// sweep, has no series at all. Labels: type, tier (local|remote).
	MetricSweepBoundViolated = "pg_desk_sweep_bound_violated"
	// MetricIngestFailed is the number of failed-ingest entries (change-feed
	// rows that could not be hydrated and were recorded instead of lost), by
	// backend instance. Derived from the ingest_failed table at scrape time.
	MetricIngestFailed = "pg_desk_ingest_failed"
	// MetricIngestFailedOldestAge is the age in seconds of the oldest
	// failed-ingest entry of a backend instance, 0 when its table slice is
	// empty (never absent for a configured owning instance, so an empty table
	// reads as 0 and an absent series means the store could not answer).
	MetricIngestFailedOldestAge = "pg_desk_ingest_failed_oldest_age_seconds"
)

// IngestFailedBackend is one backend instance's slice of the failed-ingest
// table: its entry count and the age in seconds of its oldest entry (0 when
// it has none).
type IngestFailedBackend struct {
	Backend          string
	Count            int64
	OldestAgeSeconds int64
}

// IngestFailedFunc supplies the failed-ingest observation at collect time.
type IngestFailedFunc func() ([]IngestFailedBackend, error)

// IngestFailedFromStore reads the ingest_failed table of st at every call. It
// returns one element per owning instance (zero-filled when that instance has
// no entry) plus one per other instance that holds entries, sorted by backend.
// When there is no instance at all it returns a single element with an empty
// backend and zeros, so the series exist (and read 0) on a deployment with no
// owning backend; an absent series therefore always means the read failed.
func IngestFailedFromStore(st *store.Store, owning []string, now func() time.Time) IngestFailedFunc {
	return func() ([]IngestFailedBackend, error) {
		fails, err := st.ListIngestFailures("")
		if err != nil {
			return nil, err
		}
		at := now()
		by := map[string]*IngestFailedBackend{}
		for _, inst := range owning {
			by[inst] = &IngestFailedBackend{Backend: inst}
		}
		oldest := map[string]time.Time{}
		for _, f := range fails {
			b := by[f.Instance]
			if b == nil {
				b = &IngestFailedBackend{Backend: f.Instance}
				by[f.Instance] = b
			}
			b.Count++
			first, err := time.Parse(time.RFC3339Nano, f.FirstFailedAt)
			if err != nil {
				return nil, fmt.Errorf("metrics: ingest failure (%s,%s,%d): parse first_failed_at %q: %w",
					f.Instance, f.EntityID, f.Seq, f.FirstFailedAt, err)
			}
			if cur, ok := oldest[f.Instance]; !ok || first.Before(cur) {
				oldest[f.Instance] = first
			}
		}
		if len(by) == 0 {
			by[""] = &IngestFailedBackend{}
		}
		out := make([]IngestFailedBackend, 0, len(by))
		for inst, b := range by {
			if t, ok := oldest[inst]; ok {
				if age := int64(at.Sub(t).Seconds()); age > 0 {
					b.OldestAgeSeconds = age
				}
			}
			out = append(out, *b)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Backend < out[j].Backend })
		return out, nil
	}
}

// SweepBoundFunc evaluates the sizing bound for one observed type and sweep
// tier. It MUST be the same evaluation `pg-desk doctor` uses
// (changes.EvaluateSweepBound), so the alert and the doctor line agree.
type SweepBoundFunc func(tf changes.TypeFlow, tier string) changes.BoundVerdict

// ChangeFlowOption customises RegisterChangeFlow.
type ChangeFlowOption func(*changeFlowOptions)

type changeFlowOptions struct {
	sweepBound   SweepBoundFunc
	ingestFailed IngestFailedFunc
	observedAge  OutcomeFunc
	unflagged    OutcomeFunc
}

// WithWatchedSourceObserved supplies the observation behind
// MetricWatchedSourceObservedAge. Without it the metric emits no series.
func WithWatchedSourceObserved(fn OutcomeFunc) ChangeFlowOption {
	return func(o *changeFlowOptions) { o.observedAge = fn }
}

// WithUnflaggedMembers supplies the observation behind
// MetricIssueUnflaggedMembers. Without it the metric emits no series.
func WithUnflaggedMembers(fn OutcomeFunc) ChangeFlowOption {
	return func(o *changeFlowOptions) { o.unflagged = fn }
}

// WithIngestFailed supplies the failed-ingest observation behind
// MetricIngestFailed and MetricIngestFailedOldestAge. Without it neither
// metric emits a series. A read error fails the scrape, like the change-flow
// read itself.
func WithIngestFailed(fn IngestFailedFunc) ChangeFlowOption {
	return func(o *changeFlowOptions) { o.ingestFailed = fn }
}

// WithSweepBound supplies the sizing-bound evaluation behind
// MetricSweepBoundViolated. Without it the metric emits no series.
func WithSweepBound(fn SweepBoundFunc) ChangeFlowOption {
	return func(o *changeFlowOptions) { o.sweepBound = fn }
}

// ChangeFlowFunc supplies the change-flow observation at collect time. It
// MUST return a zero Flow (Migrated false) for a store that is not on the new
// schema: the catalog then emits no change-flow series rather than failing the
// scrape.
type ChangeFlowFunc func() (changes.Flow, error)

// RegisterChangeFlow registers the change-flow instruments on a meter from mp.
// One callback reads the observation once per scrape and feeds every
// instrument, so the families always agree with each other. It is separate
// from New so the existing dashboard catalog is untouched.
func RegisterChangeFlow(mp metric.MeterProvider, fn ChangeFlowFunc, opts ...ChangeFlowOption) error {
	var o0 changeFlowOptions
	for _, opt := range opts {
		opt(&o0)
	}
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
		metric.WithDescription("hydrations owed, by type: for thread the active entities with hydrated_at older than the sweep max age (or never hydrated); for issue (no age sweep) the watched active entities never hydrated plus the failed-ingest entries; always 0 for pr"))
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

	bound, err := m.Int64ObservableGauge(MetricSweepBoundViolated,
		metric.WithDescription("1 when the sweep sizing bound active_count / max_per_poll x poll_interval <= max_age is violated for a type and tier, 0 when it holds; absent when the poll interval is unknown and for the issue type, which has no age sweep"))
	if err != nil {
		return err
	}

	ingestFailed, err := m.Int64ObservableGauge(MetricIngestFailed,
		metric.WithDescription("failed-ingest entries (change-feed rows that could not be hydrated), by backend instance; read from the ingest_failed table at scrape time"))
	if err != nil {
		return err
	}
	ingestOldest, err := m.Int64ObservableGauge(MetricIngestFailedOldestAge,
		metric.WithDescription("age in seconds of the oldest failed-ingest entry of a backend instance, 0 when it has none"))
	if err != nil {
		return err
	}

	observedAge, err := m.Int64ObservableGauge(MetricWatchedSourceObservedAge,
		metric.WithDescription("age in seconds of the oldest successful observation of a watched query of an owning backend instance, read from the feed_observed meta keys at scrape time; 0 while none is recorded"))
	if err != nil {
		return err
	}
	unflagged, err := m.Int64ObservableGauge(MetricIssueUnflaggedMembers,
		metric.WithDescription("members of an owning backend instance's watched queries whose pg-desk entity is not flagged watched, the count the last poll stored"))
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
			if o0.sweepBound == nil || !isFlowType(t.Type) || !changes.HasSweepBound(t.Type) {
				continue
			}
			for _, tier := range changes.SweepTiers {
				var v int64
				switch o0.sweepBound(t, tier) {
				case changes.BoundUnknown:
					continue // no verdict: emit no series
				case changes.BoundViolated:
					v = 1
				}
				o.ObserveInt64(bound, v, metric.WithAttributes(typ, attribute.String("tier", tier)))
			}
		}
		if o0.ingestFailed != nil {
			rows, err := o0.ingestFailed()
			if err != nil {
				return err
			}
			for _, r := range rows {
				be := attribute.String("backend", r.Backend)
				o.ObserveInt64(ingestFailed, r.Count, metric.WithAttributes(be))
				o.ObserveInt64(ingestOldest, r.OldestAgeSeconds, metric.WithAttributes(be))
			}
		}
		for _, s := range []struct {
			fn OutcomeFunc
			g  metric.Int64Observable
		}{{o0.observedAge, observedAge}, {o0.unflagged, unflagged}} {
			if s.fn == nil {
				continue
			}
			rows, err := s.fn()
			if err != nil {
				return err
			}
			for _, r := range rows {
				o.ObserveInt64(s.g, r.Value, metric.WithAttributes(attribute.String("backend", r.Backend)))
			}
		}
		for _, c := range flow.Consumers {
			o.ObserveInt64(lag, c.Lag, metric.WithAttributes(
				attribute.String("type", c.Type), attribute.String("consumer", c.Name),
			))
		}
		return nil
	}, records, hydrations, failures, retries, due, lag, repeated, bound, ingestFailed, ingestOldest, observedAge, unflagged)
	return err
}

// isFlowType reports whether t is one of the change flow's entity types: the
// sizing bound is defined for those only (consumer-only extra types carry no
// active set).
func isFlowType(t string) bool {
	for _, k := range changes.FlowEntityTypes {
		if k == t {
			return true
		}
	}
	return false
}
