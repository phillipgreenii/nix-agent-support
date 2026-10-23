// Package changemetrics registers the metrics of the shared change feed and
// change engine: the settle counter and its pending gauge, the detection delay
// histogram, the per-consumer lag, age and redelivery series, the age of each
// configured query's last success, and the count of watched entities.
//
// A connector builds one Metrics with New, hands ChangeObserver to the change
// engine and FeedObserver to the change feed, and supplies the polled sources
// through Config.Sources. Every instrument name is Config.Prefix followed by a
// fixed dotted suffix, so two connectors on one meter provider never collide.
//
// The label sets are closed. The kind label passes a value of Config.Kinds and
// folds anything else to "unknown"; the origin label passes the engine's four
// origin words; the result label passes the engine's three result words; the
// consumer label passes a consumer name the latest Sources.Consumers poll saw
// and folds every other name to "other"; the query label passes a configured
// query name and folds every other key to "other". A request that has not
// stored a key therefore never mints a label value.
package changemetrics

import (
	"context"
	"sort"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// The folded values of the closed label sets.
const (
	unknown = "unknown"
	other   = "other"
)

// Sources are the polled values behind the gauges. A nil source leaves the
// gauges it feeds unreported; a failing source skips only its own gauges.
type Sources struct {
	// Pending returns the stored pending-change row count, polled for
	// change.settle_pending.
	Pending func(ctx context.Context) (int, error)
	// Consumers returns every stored consumer key's position, polled for
	// consumer.lag_rows and consumer.oldest_unacked_age_seconds. The consumer
	// names it returned are also the only values the consumer label of
	// consumer.redeliveries may carry.
	Consumers func(ctx context.Context) ([]feed.Position, error)
	// Queries returns the stored query rows, polled together with
	// ConfiguredQueries for query.last_success_age_seconds.
	Queries func(ctx context.Context) ([]store.QueryRow, error)
	// ConfiguredQueries names the configured queries: the only values the query
	// label may carry (any other folds to "other") and the series
	// query.last_success_age_seconds reports. nil means none.
	ConfiguredQueries func() []string
	// Watched returns the count of watched entities, polled for
	// watched.entities.
	Watched func(ctx context.Context) (int, error)
}

// Config configures a Metrics.
type Config struct {
	// Prefix is put in front of every instrument name, for example
	// "example_connector."; empty means no prefix.
	Prefix string
	// Kinds is the kind label set (the connector's change kinds); any other
	// kind folds to "unknown".
	Kinds []string
	// Started is when the daemon started: the age of a configured query that
	// never succeeded counts from it. Zero means when New ran.
	Started time.Time
	// Now is the clock the polled ages use; nil means time.Now.
	Now func() time.Time
	// Sources are the polled values.
	Sources Sources
}

// Metrics holds the instruments. It is safe for concurrent use.
type Metrics struct {
	cfg     Config
	now     func() time.Time
	started time.Time

	settle         metric.Int64Counter
	settlePending  metric.Int64ObservableGauge
	detectionDelay metric.Float64Histogram
	consumerLag    metric.Int64ObservableGauge
	consumerOldest metric.Float64ObservableGauge
	redeliveries   metric.Int64Counter
	queryAge       metric.Float64ObservableGauge
	watched        metric.Int64ObservableGauge

	consumersMu sync.RWMutex
	consumers   map[string]struct{}

	registration metric.Registration
}

// New creates the instruments on meter and registers the polled-gauge
// callback.
func New(meter metric.Meter, cfg Config) (*Metrics, error) {
	m := &Metrics{cfg: cfg, now: cfg.Now, started: cfg.Started}
	if m.now == nil {
		m.now = time.Now
	}
	if m.started.IsZero() {
		m.started = m.now()
	}
	p := cfg.Prefix

	var err error
	if m.settle, err = meter.Int64Counter(p+"change.settle",
		metric.WithDescription("Ended settle rows by result: settled, failed or timeout"),
		metric.WithUnit("{row}")); err != nil {
		return nil, err
	}
	if m.settlePending, err = meter.Int64ObservableGauge(p+"change.settle_pending",
		metric.WithDescription("Change rows waiting on dependent groups"),
		metric.WithUnit("{row}")); err != nil {
		return nil, err
	}
	if m.detectionDelay, err = meter.Float64Histogram(p+"change.detection_delay_seconds",
		metric.WithDescription("Per kind of every appended change row: its detection time minus the origin's updatedAt"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(0, 1, 5, 15, 30, 60, 120, 300, 900, 1800, 3600, 7200, 21600, 86400)); err != nil {
		return nil, err
	}
	if m.consumerLag, err = meter.Int64ObservableGauge(p+"consumer.lag_rows",
		metric.WithDescription("Rows a poll of a stored consumer key would deliver after its acknowledged position"),
		metric.WithUnit("{row}")); err != nil {
		return nil, err
	}
	if m.consumerOldest, err = meter.Float64ObservableGauge(p+"consumer.oldest_unacked_age_seconds",
		metric.WithDescription("Age of the oldest unacknowledged row of a stored consumer key; zero when none"),
		metric.WithUnit("s")); err != nil {
		return nil, err
	}
	if m.redeliveries, err = meter.Int64Counter(p+"consumer.redeliveries",
		metric.WithDescription("Rows handed out again to a consumer key that had not acknowledged them"),
		metric.WithUnit("{row}")); err != nil {
		return nil, err
	}
	if m.queryAge, err = meter.Float64ObservableGauge(p+"query.last_success_age_seconds",
		metric.WithDescription("Age of a configured query's last success, or of the daemon when it never succeeded"),
		metric.WithUnit("s")); err != nil {
		return nil, err
	}
	if m.watched, err = meter.Int64ObservableGauge(p+"watched.entities",
		metric.WithDescription("Entities the daemon watches"),
		metric.WithUnit("{entity}")); err != nil {
		return nil, err
	}

	m.registration, err = meter.RegisterCallback(m.observe,
		m.settlePending, m.consumerLag, m.consumerOldest, m.queryAge, m.watched)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// Close unregisters the gauge callback.
func (m *Metrics) Close() error { return m.registration.Unregister() }

// ChangeObserver returns the engine.Observer that feeds change.settle and
// change.detection_delay_seconds from the change engine's events.
func (m *Metrics) ChangeObserver() engine.Observer { return changeObserver{m: m} }

// FeedObserver returns the feed.Observer that feeds consumer.redeliveries from
// the change feed's events.
func (m *Metrics) FeedObserver() feed.Observer { return feedObserver{m: m} }

type changeObserver struct{ m *Metrics }

// Detected records one change.detection_delay_seconds point for the kind and
// origin; a negative delay (clock skew against the origin) records zero.
func (o changeObserver) Detected(kind, origin string, delay time.Duration) {
	if delay < 0 {
		delay = 0
	}
	o.m.detectionDelay.Record(context.Background(), delay.Seconds(), metric.WithAttributes(
		attribute.String("kind", KindLabel(o.m.cfg.Kinds, kind)),
		attribute.String("origin", OriginLabel(origin)),
	))
}

// Settled counts one ended settle row under its result word.
func (o changeObserver) Settled(result string) {
	o.m.settle.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("result", ResultLabel(result)),
	))
}

type feedObserver struct{ m *Metrics }

// Redelivered adds rows to consumer.redeliveries under the key's labels.
func (o feedObserver) Redelivered(consumer, query string, rows int) {
	if rows <= 0 {
		return
	}
	o.m.redeliveries.Add(context.Background(), int64(rows), metric.WithAttributes(
		attribute.String("consumer", o.m.consumerLabel(consumer)),
		attribute.String("query", o.m.queryLabel(query)),
	))
}

// KindLabel passes a kind from kinds and folds anything else to "unknown".
func KindLabel(kinds []string, kind string) string {
	for _, k := range kinds {
		if kind == k {
			return kind
		}
	}
	return unknown
}

// OriginLabel passes one of the engine's four origin words and folds anything
// else to "unknown".
func OriginLabel(origin string) string {
	switch origin {
	case engine.OriginQuery, engine.OriginRefresh, engine.OriginWrite, engine.OriginRead:
		return origin
	default:
		return unknown
	}
}

// ResultLabel passes one of the engine's three settle result words and folds
// anything else to "unknown".
func ResultLabel(result string) string {
	switch result {
	case engine.ResultSettled, engine.ResultFailed, engine.ResultTimeout:
		return result
	default:
		return unknown
	}
}

// consumerLabel passes a consumer name that the last Consumers poll saw stored
// and folds every other name to "other".
func (m *Metrics) consumerLabel(consumer string) string {
	m.consumersMu.RLock()
	_, ok := m.consumers[consumer]
	m.consumersMu.RUnlock()
	if ok {
		return consumer
	}
	return other
}

// queryLabel passes a configured query name and folds every other key (an
// ad-hoc query key, an unknown name, none when the source is unset) to "other".
func (m *Metrics) queryLabel(query string) string {
	if m.cfg.Sources.ConfiguredQueries != nil {
		for _, name := range m.cfg.Sources.ConfiguredQueries() {
			if name == query {
				return query
			}
		}
	}
	return other
}

// noteConsumers remembers the consumer names of the latest Consumers poll.
func (m *Metrics) noteConsumers(ps []feed.Position) {
	set := make(map[string]struct{}, len(ps))
	for _, p := range ps {
		set[p.Consumer] = struct{}{}
	}
	m.consumersMu.Lock()
	m.consumers = set
	m.consumersMu.Unlock()
}

// observe is the polled-gauge callback: settle_pending, consumer.lag_rows,
// consumer.oldest_unacked_age_seconds, query.last_success_age_seconds and
// watched.entities. An unset source leaves its gauges unreported; a failing
// source skips only its own gauges.
func (m *Metrics) observe(ctx context.Context, o metric.Observer) error {
	src := m.cfg.Sources
	if src.Pending != nil {
		if n, err := src.Pending(ctx); err == nil {
			o.ObserveInt64(m.settlePending, int64(n))
		}
	}
	if src.Consumers != nil {
		if ps, err := src.Consumers(ctx); err == nil {
			m.noteConsumers(ps)
			m.observeConsumers(o, ps)
		}
	}
	if src.Queries != nil && src.ConfiguredQueries != nil {
		if rows, err := src.Queries(ctx); err == nil {
			m.observeQueries(o, rows)
		}
	}
	if src.Watched != nil {
		if n, err := src.Watched(ctx); err == nil {
			o.ObserveInt64(m.watched, int64(n))
		}
	}
	return nil
}

type consumerSeries struct {
	lag    int64
	oldest time.Time
}

// observeConsumers reports lag and age per (consumer, query) label pair. Cursors
// of one consumer, and every unconfigured query, share a pair, so their lag adds
// up and their age is the oldest one.
func (m *Metrics) observeConsumers(o metric.Observer, ps []feed.Position) {
	type pair struct{ consumer, query string }
	series := map[pair]*consumerSeries{}
	for _, p := range ps {
		k := pair{consumer: p.Consumer, query: m.queryLabel(p.Scope)}
		s := series[k]
		if s == nil {
			s = &consumerSeries{}
			series[k] = s
		}
		s.lag += int64(p.LagRows)
		if !p.OldestUnacked.IsZero() && (s.oldest.IsZero() || p.OldestUnacked.Before(s.oldest)) {
			s.oldest = p.OldestUnacked
		}
	}
	keys := make([]pair, 0, len(series))
	for k := range series {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].consumer != keys[j].consumer {
			return keys[i].consumer < keys[j].consumer
		}
		return keys[i].query < keys[j].query
	})
	now := m.now()
	for _, k := range keys {
		s := series[k]
		attrs := metric.WithAttributes(
			attribute.String("consumer", k.consumer),
			attribute.String("query", k.query),
		)
		o.ObserveInt64(m.consumerLag, s.lag, attrs)
		age := 0.0
		if !s.oldest.IsZero() {
			age = max(now.Sub(s.oldest).Seconds(), 0)
		}
		o.ObserveFloat64(m.consumerOldest, age, attrs)
	}
}

// observeQueries reports one series per configured query: the age of its
// last_success_at, or the age since the daemon started when it never succeeded
// (so a "configured queries fresh" alert has a series to fire on).
func (m *Metrics) observeQueries(o metric.Observer, rows []store.QueryRow) {
	byKey := make(map[string]store.QueryRow, len(rows))
	for _, r := range rows {
		byKey[r.Key] = r
	}
	now := m.now()
	seen := map[string]bool{}
	for _, name := range m.cfg.Sources.ConfiguredQueries() {
		if seen[name] {
			continue
		}
		seen[name] = true
		since := m.started
		if r, ok := byKey[name]; ok && !r.LastSuccessAt.IsZero() {
			since = r.LastSuccessAt
		}
		o.ObserveFloat64(m.queryAge, max(now.Sub(since).Seconds(), 0),
			metric.WithAttributes(attribute.String("query", name)))
	}
}
