package metrics

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/phillipgreenii/pg-router/internal/eventqueue"
)

// The level metrics and the level log lines. Level 0 of a scale is its best.
//
// Exported names: the Metric* constants below, LevelSample, LevelReading,
// WithLevels, WithPausedRoles, WithLogger, OverflowLabel, and these Emitter
// methods: OnLevelChanged, OnLevelGraceExpired, OnLevelOverrideSet,
// OnLevelOverrideExpired, OnLevelEventRejected (the projection's events),
// OnRequirementBlocked, OnRequirementDrop, OnRolePaused, OnRoleResumed,
// OnLegacyGateDropped (eventqueue.LevelObserver), OnLevelPull and
// OnLevelPullFailure (the discover package's level pull observer interfaces,
// asserted where the router is wired, because discover imports this package's
// importers).
//
// Constants omit the unit and _total suffixes the exporter adds. Two forced
// exceptions are registered with NO unit so the exposed name is exactly the
// catalog name and does not clash with a sibling instrument:
// MetricResourceLevelSeconds (the counter; the gauge MetricResourceLevel
// already owns the bare name) and MetricSensorPullSeconds (the gauge; the
// counter MetricSensorPull owns the bare name).
//
// Every log line carries the key resource_level (never level): the level the
// line is about, and for a legacy gate the gate's type.
const (
	MetricResourceLevel               = "pg_router_resource_level"
	MetricResourceScaleSize           = "pg_router_resource_scale_size"
	MetricResourceLevelAge            = "pg_router_resource_level_age"
	MetricResourceLevelLeaseRemaining = "pg_router_resource_level_lease_remaining"
	MetricResourceLevelStale          = "pg_router_resource_level_stale"
	MetricResourceLevelOverrideActive = "pg_router_resource_level_override_active"
	MetricResourceLevelSource         = "pg_router_resource_level_source"
	MetricResourceLevelSeconds        = "pg_router_resource_level_seconds"
	MetricResourceChanges             = "pg_router_resource_changes"
	MetricResourceReading             = "pg_router_resource_reading"
	MetricSensorPull                  = "pg_router_sensor_pull"
	MetricSensorPullSeconds           = "pg_router_sensor_pull_seconds"
	MetricSensorPullFailures          = "pg_router_sensor_pull_failures"
	MetricSensorLastObservedAge       = "pg_router_sensor_last_observed_age"
	MetricLevelEventsRejected         = "pg_router_level_events_rejected"
	MetricRolePaused                  = "pg_router_role_paused"
	MetricRequirementBlocked          = "pg_router_requirement_blocked"
	MetricRequirementDrops            = "pg_router_requirement_drops"
)

// OverflowLabel is the value a label takes once its 64 distinct values are used.
const OverflowLabel = "OTHER"

// maxFoldLabels is the number of distinct values kept per folded label.
const maxFoldLabels = 64

// LevelReading is the numeric reading behind a level.
type LevelReading struct {
	Value float64
	Unit  string
}

// LevelSample is one resource's state at collection time, supplied by the level
// projection through WithLevels. When HasLevel is false no level is in force:
// Level and Index describe the scale's unknown value.
type LevelSample struct {
	Resource       string
	Level          string
	Index          int // 0 = best
	ScaleSize      int
	HasLevel       bool
	Source         string // sensor, operator or grace_fallback; empty without a level
	Owner          string
	ObservedAt     time.Time
	Age            time.Duration
	LeaseRemaining time.Duration
	HasLease       bool // the level in force has an expiry
	Stale          bool
	OverrideActive bool
	Reading        *LevelReading
}

// WithLevels registers the level gauges over fn, read on each collect.
func WithLevels(fn func() []LevelSample) Option { return func(o *options) { o.levels = fn } }

// WithPausedRoles seeds MetricRolePaused at 0 for each named role so its series
// exists from start.
func WithPausedRoles(roles ...string) Option {
	return func(o *options) { o.pausedRoles = append(o.pausedRoles, roles...) }
}

// WithLogger sets the logger of the level log lines (default slog.Default,
// resolved at each call).
func WithLogger(l *slog.Logger) Option { return func(o *options) { o.logger = l } }

// levelState is the Emitter's level bookkeeping.
type levelState struct {
	changes, rejected, pulls, pullFails, blocked, drops metric.Int64Counter
	log                                                 *slog.Logger
	now                                                 func() time.Time

	mu       sync.Mutex
	paused   map[string]bool
	pullDur  map[string]float64
	fold     map[string]map[string]struct{}
	accum    map[[2]string]float64
	lastTick time.Time
}

func (e *Emitter) logger() *slog.Logger {
	if e.lvl.log != nil {
		return e.lvl.log
	}
	return slog.Default()
}

// foldLabel keeps the first maxFoldLabels distinct values of the label kind and
// records any later one as OverflowLabel.
func (e *Emitter) foldLabel(kind, v string) string {
	s := &e.lvl
	s.mu.Lock()
	defer s.mu.Unlock()
	set := s.fold[kind]
	if set == nil {
		set = map[string]struct{}{}
		s.fold[kind] = set
	}
	if _, ok := set[v]; ok {
		return v
	}
	if len(set) >= maxFoldLabels {
		return OverflowLabel
	}
	set[v] = struct{}{}
	return v
}

func (e *Emitter) registerLevels(m metric.Meter, cfg options) error {
	s := &e.lvl
	s.log, s.now = cfg.logger, cfg.now
	s.paused, s.pullDur = map[string]bool{}, map[string]float64{}
	s.fold, s.accum = map[string]map[string]struct{}{}, map[[2]string]float64{}
	s.lastTick = cfg.now()
	for _, r := range cfg.pausedRoles {
		s.paused[r] = false
	}
	var err error
	counters := []struct {
		dst  *metric.Int64Counter
		name string
		desc string
	}{
		{&s.changes, MetricResourceChanges, "changes of the level in force, per resource and the level changed to"},
		{&s.rejected, MetricLevelEventsRejected, "level answers the router refused, per reason"},
		{&s.pulls, MetricSensorPull, "finished pulls of a sensor, whatever the outcome"},
		{&s.pullFails, MetricSensorPullFailures, "failed sensor pulls, per sensor and reason (deadline, exit, malformed, rejected)"},
		{&s.blocked, MetricRequirementBlocked, "times a requirement stopped a role or emitter from doing work it had, per role, resource and why (level, unknown, stale)"},
		{&s.drops, MetricRequirementDrops, "events that went away for a blocked role at their final attempt"},
	}
	for _, c := range counters {
		if *c.dst, err = m.Int64Counter(c.name, metric.WithDescription(c.desc)); err != nil {
			return err
		}
	}
	if _, err = m.Int64ObservableGauge(MetricRolePaused,
		metric.WithDescription("1 while a requirement holds the role back, else 0"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			for role, p := range s.paused {
				v := int64(0)
				if p {
					v = 1
				}
				o.Observe(v, metric.WithAttributes(attribute.String("role", role)))
			}
			return nil
		})); err != nil {
		return err
	}
	if _, err = m.Float64ObservableGauge(MetricSensorPullSeconds,
		metric.WithDescription("duration in seconds of the last pull of the sensor"),
		metric.WithFloat64Callback(func(_ context.Context, o metric.Float64Observer) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			for sensor, d := range s.pullDur {
				o.Observe(d, metric.WithAttributes(attribute.String("sensor", sensor)))
			}
			return nil
		})); err != nil {
		return err
	}
	if cfg.levels == nil {
		return nil
	}
	return e.registerLevelGauges(m, cfg.levels)
}

func (e *Emitter) registerLevelGauges(m metric.Meter, fn func() []LevelSample) error {
	res := func(r string) metric.MeasurementOption {
		return metric.WithAttributes(attribute.String("resource", e.foldLabel("resource", r)))
	}
	b2i := func(b bool) int64 {
		if b {
			return 1
		}
		return 0
	}
	intGauge := func(name, desc string, unit string, obs func(o metric.Int64Observer, x LevelSample, a metric.MeasurementOption)) error {
		_, err := m.Int64ObservableGauge(name, metric.WithUnit(unit), metric.WithDescription(desc),
			metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
				for _, x := range fn() {
					obs(o, x, res(x.Resource))
				}
				return nil
			}))
		return err
	}
	fltGauge := func(name, desc, unit string, obs func(o metric.Float64Observer, x LevelSample, a metric.MeasurementOption)) error {
		_, err := m.Float64ObservableGauge(name, metric.WithUnit(unit), metric.WithDescription(desc),
			metric.WithFloat64Callback(func(_ context.Context, o metric.Float64Observer) error {
				for _, x := range fn() {
					obs(o, x, res(x.Resource))
				}
				return nil
			}))
		return err
	}
	if err := intGauge(MetricResourceLevel, "index of the level in force on the resource's scale, 0 = best (the scale's unknown value while no level is held)", "{level}",
		func(o metric.Int64Observer, x LevelSample, a metric.MeasurementOption) { o.Observe(int64(x.Index), a) }); err != nil {
		return err
	}
	if err := intGauge(MetricResourceScaleSize, "number of levels on the resource's scale", "{level}",
		func(o metric.Int64Observer, x LevelSample, a metric.MeasurementOption) {
			o.Observe(int64(x.ScaleSize), a)
		}); err != nil {
		return err
	}
	if err := intGauge(MetricResourceLevelStale, "1 while the level in force is held past its lease", "{state}",
		func(o metric.Int64Observer, x LevelSample, a metric.MeasurementOption) { o.Observe(b2i(x.Stale), a) }); err != nil {
		return err
	}
	if err := intGauge(MetricResourceLevelOverrideActive, "1 while an operator override is in force", "{state}",
		func(o metric.Int64Observer, x LevelSample, a metric.MeasurementOption) {
			o.Observe(b2i(x.OverrideActive), a)
		}); err != nil {
		return err
	}
	if err := intGauge(MetricResourceLevelSource, "1 for the source of the level in force (sensor, operator or grace_fallback); absent when no level is held", "{state}",
		func(o metric.Int64Observer, x LevelSample, a metric.MeasurementOption) {
			if x.HasLevel {
				o.Observe(1, a, metric.WithAttributes(attribute.String("source", x.Source)))
			}
		}); err != nil {
		return err
	}
	if err := fltGauge(MetricResourceLevelAge, "seconds since the level in force was observed; absent when no level is held", "s",
		func(o metric.Float64Observer, x LevelSample, a metric.MeasurementOption) {
			if x.HasLevel {
				o.Observe(x.Age.Seconds(), a)
			}
		}); err != nil {
		return err
	}
	if err := fltGauge(MetricResourceLevelLeaseRemaining, "seconds until the level in force expires; present only while it has an expiry", "s",
		func(o metric.Float64Observer, x LevelSample, a metric.MeasurementOption) {
			if x.HasLevel && x.HasLease {
				o.Observe(max(x.LeaseRemaining, 0).Seconds(), a)
			}
		}); err != nil {
		return err
	}
	if err := fltGauge(MetricResourceReading, "the numeric reading behind the level in force", "",
		func(o metric.Float64Observer, x LevelSample, a metric.MeasurementOption) {
			if x.Reading != nil {
				o.Observe(x.Reading.Value, a, metric.WithAttributes(attribute.String("unit", x.Reading.Unit)))
			}
		}); err != nil {
		return err
	}
	if _, err := m.Float64ObservableGauge(MetricSensorLastObservedAge,
		metric.WithUnit("s"),
		metric.WithDescription("seconds since the newest answer a sensor's level in force was observed"),
		metric.WithFloat64Callback(func(_ context.Context, o metric.Float64Observer) error {
			newest := map[string]time.Time{}
			for _, x := range fn() {
				if !x.HasLevel || x.Source == "operator" || x.Owner == "" {
					continue
				}
				if x.ObservedAt.After(newest[x.Owner]) {
					newest[x.Owner] = x.ObservedAt
				}
			}
			now := e.lvl.now()
			for sensor, t := range newest {
				o.Observe(max(now.Sub(t), 0).Seconds(), metric.WithAttributes(attribute.String("sensor", sensor)))
			}
			return nil
		})); err != nil {
		return err
	}
	_, err := m.Float64ObservableCounter(MetricResourceLevelSeconds,
		metric.WithDescription("seconds each resource has spent at each level"),
		metric.WithFloat64Callback(func(_ context.Context, o metric.Float64Observer) error {
			s := &e.lvl
			now := s.now()
			s.mu.Lock()
			defer s.mu.Unlock()
			delta := max(now.Sub(s.lastTick), 0).Seconds()
			s.lastTick = now
			for _, x := range fn() {
				s.accum[[2]string{e.foldLabelLocked("resource", x.Resource), x.Level}] += delta
			}
			for k, v := range s.accum {
				o.Observe(v, metric.WithAttributes(attribute.String("resource", k[0]), attribute.String("level", k[1])))
			}
			return nil
		}))
	return err
}

// foldLabelLocked is foldLabel for a caller already holding lvl.mu.
func (e *Emitter) foldLabelLocked(kind, v string) string {
	s := &e.lvl
	set := s.fold[kind]
	if set == nil {
		set = map[string]struct{}{}
		s.fold[kind] = set
	}
	if _, ok := set[v]; ok {
		return v
	}
	if len(set) >= maxFoldLabels {
		return OverflowLabel
	}
	set[v] = struct{}{}
	return v
}

// OnLevelChanged counts a change of the level in force and logs level_changed.
func (e *Emitter) OnLevelChanged(resource, from, to string) {
	e.lvl.changes.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("resource", e.foldLabel("resource", resource)), attribute.String("to", to),
	))
	e.logger().Info("level_changed", "resource", resource, "resource_level", to, "from", from)
}

// OnLevelGraceExpired logs level_grace_expired: the sensor's last level ran out of grace.
func (e *Emitter) OnLevelGraceExpired(resource, lastLevel string) {
	e.logger().Warn("level_grace_expired", "resource", resource, "resource_level", lastLevel)
}

// OnLevelOverrideSet logs level_override_set (a zero expiresAt is until cleared).
func (e *Emitter) OnLevelOverrideSet(resource, level, owner string, expiresAt time.Time) {
	if expiresAt.IsZero() {
		e.logger().Info("level_override_set", "resource", resource, "resource_level", level, "owner", owner, "until_cleared", true)
		return
	}
	e.logger().Info("level_override_set", "resource", resource, "resource_level", level, "owner", owner, "expires_at", expiresAt.UTC().Format(time.RFC3339))
}

// OnLevelOverrideExpired logs level_override_expired.
func (e *Emitter) OnLevelOverrideExpired(resource, level, owner string) {
	e.logger().Info("level_override_expired", "resource", resource, "resource_level", level, "owner", owner)
}

// OnLevelEventRejected counts a refused level answer and logs level_event_rejected.
func (e *Emitter) OnLevelEventRejected(resource, level, reason string) {
	e.lvl.rejected.Add(context.Background(), 1, metric.WithAttributes(attribute.String("reason", reason)))
	e.logger().Warn("level_event_rejected", "resource", resource, "resource_level", level, "reason", reason)
}

// OnRequirementBlocked implements eventqueue.LevelObserver: it counts a block by
// role, resource and why the clause failed.
func (e *Emitter) OnRequirementBlocked(participant, _, resource, kind string) {
	e.lvl.blocked.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("role", e.foldLabel("role", participant)),
		attribute.String("resource", e.foldLabel("resource", resource)),
		attribute.String("kind", kind),
	))
}

// OnRequirementDrop implements eventqueue.LevelObserver.
func (e *Emitter) OnRequirementDrop(evtType, role, resource string) {
	e.lvl.drops.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("type", e.foldLabel("type", evtType)),
		attribute.String("role", e.foldLabel("role", role)),
		attribute.String("resource", e.foldLabel("resource", resource)),
	))
}

// OnRolePaused implements eventqueue.LevelObserver: it sets the gauge and logs
// role_paused, once per transition.
func (e *Emitter) OnRolePaused(role, resource, level string) {
	e.lvl.mu.Lock()
	e.lvl.paused[role] = true
	e.lvl.mu.Unlock()
	e.logger().Warn("role_paused", "role", role, "resource", resource, "resource_level", level)
}

// OnRoleResumed implements eventqueue.LevelObserver.
func (e *Emitter) OnRoleResumed(role, resource, level string) {
	e.lvl.mu.Lock()
	e.lvl.paused[role] = false
	e.lvl.mu.Unlock()
	e.logger().Info("role_resumed", "role", role, "resource", resource, "resource_level", level)
}

// OnLegacyGateDropped implements eventqueue.LevelObserver: one legacy_gate_dropped
// line per dropped active legacy gate. A legacy gate has no resource; its type
// stands in as the resource_level.
func (e *Emitter) OnLegacyGateDropped(gateType, owner string, expiresAt time.Time) {
	exp := "none"
	if !expiresAt.IsZero() {
		exp = expiresAt.UTC().Format(time.RFC3339)
	}
	e.logger().Warn("legacy_gate_dropped", "resource_level", gateType, "type", gateType, "owner", owner, "expires_at", exp)
}

// OnLevelPull implements discover.LevelPullTimingObserver.
func (e *Emitter) OnLevelPull(sensor string, elapsed time.Duration) {
	e.lvl.pulls.Add(context.Background(), 1, metric.WithAttributes(attribute.String("sensor", sensor)))
	e.lvl.mu.Lock()
	e.lvl.pullDur[sensor] = elapsed.Seconds()
	e.lvl.mu.Unlock()
}

// OnLevelPullFailure implements discover.LevelPullObserver.
func (e *Emitter) OnLevelPullFailure(sensor, reason string) {
	e.lvl.pullFails.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("sensor", sensor), attribute.String("reason", reason),
	))
}

var _ eventqueue.LevelObserver = (*Emitter)(nil)
