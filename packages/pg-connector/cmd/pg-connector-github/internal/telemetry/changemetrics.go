package telemetry

import (
	"context"
	"sort"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// Label keys of the change engine's metrics.
const (
	keyKind     = "kind"
	keyOrigin   = "origin"
	keyConsumer = "consumer"
	keyQuery    = "query"
)

// NewChangeObserver returns the changes.Observer that feeds change.settle and
// change.detection_delay_seconds from the change engine's events. A nil
// Telemetry (the daemon runs on when telemetry fails to start) yields an
// observer that does nothing.
func NewChangeObserver(t *Telemetry) changes.Observer {
	if t == nil {
		return noopChangeObserver{}
	}
	return changeObserver{t: t}
}

// NewFeedObserver returns the feed.Observer that feeds consumer.redeliveries
// from the change feed's events. A nil Telemetry yields an observer that does
// nothing.
func NewFeedObserver(t *Telemetry) feed.Observer {
	if t == nil {
		return noopFeedObserver{}
	}
	return feedObserver{t: t}
}

type noopChangeObserver struct{}

func (noopChangeObserver) Detected(string, string, time.Duration) {}
func (noopChangeObserver) Settled(string)                         {}

type noopFeedObserver struct{}

func (noopFeedObserver) Redelivered(string, string, int) {}

type changeObserver struct{ t *Telemetry }

// Detected records one change.detection_delay_seconds point for the kind and
// origin; a negative delay (clock skew against the origin) records zero.
func (o changeObserver) Detected(kind, origin string, delay time.Duration) {
	if delay < 0 {
		delay = 0
	}
	o.t.detectionDelay.Record(context.Background(), delay.Seconds(), metric.WithAttributes(
		attribute.String(keyKind, changeKindLabel(kind)),
		attribute.String(keyOrigin, changeOriginLabel(origin)),
	))
}

// Settled counts one ended settle row under its result word.
func (o changeObserver) Settled(result string) {
	o.t.settle.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String(keyResult, settleResultLabel(result)),
	))
}

type feedObserver struct{ t *Telemetry }

// Redelivered adds rows to consumer.redeliveries under the key's labels.
func (o feedObserver) Redelivered(consumer, query string, rows int) {
	if rows <= 0 {
		return
	}
	o.t.redeliveries.Add(context.Background(), int64(rows), metric.WithAttributes(
		attribute.String(keyConsumer, o.t.consumerLabel(consumer)),
		attribute.String(keyQuery, o.t.queryLabel(query)),
	))
}

// changeKindLabel passes a kind from classify.Kinds() and folds anything else
// to "unknown".
func changeKindLabel(kind string) string {
	for _, k := range classify.Kinds() {
		if kind == k {
			return kind
		}
	}
	return unknown
}

// changeOriginLabel passes one of the four origin words and folds anything else
// to "unknown".
func changeOriginLabel(origin string) string {
	switch origin {
	case groupsvc.OriginQuery, groupsvc.OriginRefresh, groupsvc.OriginWrite, groupsvc.OriginRead:
		return origin
	default:
		return unknown
	}
}

// settleResultLabel passes one of the three settle result words and folds
// anything else to "unknown".
func settleResultLabel(result string) string {
	switch result {
	case changes.ResultSettled, changes.ResultFailed, changes.ResultTimeout:
		return result
	default:
		return unknown
	}
}

// consumerLabel passes a consumer name that the last Positions poll saw stored
// and folds every other name to "other", so a request that has not stored a key
// never mints a label value.
func (t *Telemetry) consumerLabel(consumer string) string {
	t.consumersMu.RLock()
	_, ok := t.consumers[consumer]
	t.consumersMu.RUnlock()
	if ok {
		return consumer
	}
	return other
}

// queryLabel passes a configured query name and folds every other key (an
// ad-hoc query key, an unknown name, none when the source is unset) to "other".
func (t *Telemetry) queryLabel(query string) string {
	if t.opts.ConfiguredQueries != nil {
		for _, name := range t.opts.ConfiguredQueries() {
			if name == query {
				return query
			}
		}
	}
	return other
}

// noteConsumers remembers the consumer names of the latest Positions poll.
func (t *Telemetry) noteConsumers(ps []feed.Position) {
	set := make(map[string]struct{}, len(ps))
	for _, p := range ps {
		set[p.Consumer] = struct{}{}
	}
	t.consumersMu.Lock()
	t.consumers = set
	t.consumersMu.Unlock()
}

// observeChanges reads the change engine's polled gauges: settle_pending,
// consumer.lag_rows, consumer.oldest_unacked_age_seconds and
// query.last_success_age_seconds. An unset source leaves its gauges
// unreported; a failing source skips only its own gauges.
func (t *Telemetry) observeChanges(ctx context.Context, o metric.Observer) {
	if t.opts.Pending != nil {
		if n, err := t.opts.Pending(ctx); err == nil {
			o.ObserveInt64(t.settlePending, int64(n))
		}
	}
	if t.opts.Consumers != nil {
		if ps, err := t.opts.Consumers(ctx); err == nil {
			t.noteConsumers(ps)
			t.observeConsumers(o, ps)
		}
	}
	if t.opts.Queries != nil && t.opts.ConfiguredQueries != nil {
		if rows, err := t.opts.Queries(ctx); err == nil {
			t.observeQueries(o, rows)
		}
	}
}

type consumerSeries struct {
	lag    int64
	oldest time.Time
}

// observeConsumers reports lag and age per (consumer, query) label pair. Keys
// that differ only in kind, and every unconfigured query, share a pair, so
// their lag adds up and their age is the oldest one.
func (t *Telemetry) observeConsumers(o metric.Observer, ps []feed.Position) {
	type pair struct{ consumer, query string }
	series := map[pair]*consumerSeries{}
	for _, p := range ps {
		k := pair{consumer: p.Consumer, query: t.queryLabel(p.Query)}
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
	now := t.now()
	for _, k := range keys {
		s := series[k]
		attrs := metric.WithAttributes(
			attribute.String(keyConsumer, k.consumer),
			attribute.String(keyQuery, k.query),
		)
		o.ObserveInt64(t.consumerLag, s.lag, attrs)
		age := 0.0
		if !s.oldest.IsZero() {
			age = max(now.Sub(s.oldest).Seconds(), 0)
		}
		o.ObserveFloat64(t.consumerOldest, age, attrs)
	}
}

// observeQueries reports one series per configured query: the age of its
// last_success_at, or the age since the daemon started when it never succeeded
// (so the "configured queries fresh" alert has a series to fire on).
func (t *Telemetry) observeQueries(o metric.Observer, rows []store.QueryRow) {
	byKey := make(map[string]store.QueryRow, len(rows))
	for _, r := range rows {
		byKey[r.Key] = r
	}
	now := t.now()
	seen := map[string]bool{}
	for _, name := range t.opts.ConfiguredQueries() {
		if seen[name] {
			continue
		}
		seen[name] = true
		since := t.started
		if r, ok := byKey[name]; ok && !r.LastSuccessAt.IsZero() {
			since = r.LastSuccessAt
		}
		o.ObserveFloat64(t.queryAge, max(now.Sub(since).Seconds(), 0),
			metric.WithAttributes(attribute.String(keyQuery, name)))
	}
}

// consumerState is the stored-key set the Redelivered observer checks.
type consumerState struct {
	consumersMu sync.RWMutex
	consumers   map[string]struct{}
}
