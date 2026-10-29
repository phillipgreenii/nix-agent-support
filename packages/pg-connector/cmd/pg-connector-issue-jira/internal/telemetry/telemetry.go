package telemetry

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/changemetrics"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// Metric names, after the pa-monitor convention (dotted, one service prefix).
// Every one is registered without a _total suffix. The catalog file
// packages/pg-connector/grafana/pg-connector-issue-jira-metrics.txt lists the
// same set; keep them in step (the catalog test fails otherwise).
const (
	MetricDaemonUp        = "pg_connector_issue_jira.daemon.up"
	MetricGovernorPaused  = "pg_connector_issue_jira.governor.paused"
	MetricJiraRequests    = "pg_connector_issue_jira.jira.requests"
	MetricBudgetRemaining = "pg_connector_issue_jira.budget.remaining"

	// The origin's own rate-limit reading (the last pjira-ratelimit line) and the
	// configured floor. They report only while the reading is no older than
	// origin.ReadingMaxAge; origin.floor always reports.
	MetricOriginRemaining      = "pg_connector_issue_jira.origin.remaining"
	MetricOriginLimit          = "pg_connector_issue_jira.origin.limit"
	MetricOriginNearLimit      = "pg_connector_issue_jira.origin.near_limit"
	MetricOriginReset          = "pg_connector_issue_jira.origin.reset"
	MetricOriginFloor          = "pg_connector_issue_jira.origin.floor"
	MetricOriginHeadroom       = "pg_connector_issue_jira.origin.headroom"
	MetricOriginReadingAgeSecs = "pg_connector_issue_jira.origin.reading_age_seconds"

	MetricQueueDepth         = "pg_connector_issue_jira.queue.depth"
	MetricQueueOldestOverdue = "pg_connector_issue_jira.queue.oldest_overdue_seconds"
	MetricGroupStaleness     = "pg_connector_issue_jira.group.staleness_seconds"
	MetricRequestDuration    = "pg_connector_issue_jira.request.duration"
	MetricTrackedEntities    = "pg_connector_issue_jira.tracked.entities"
	MetricStoreBytes         = "pg_connector_issue_jira.store.bytes"
	MetricStoreEvictions     = "pg_connector_issue_jira.store.evictions"
	MetricRowsWithoutKind    = "pg_connector_issue_jira.change.rows_without_kind"

	// The change feed and change engine instruments are built by
	// pkg/connectord/changemetrics under metricPrefix; these constants name them.
	MetricChangeSettle          = "pg_connector_issue_jira.change.settle"
	MetricChangeSettlePending   = "pg_connector_issue_jira.change.settle_pending"
	MetricChangeDetectionDelay  = "pg_connector_issue_jira.change.detection_delay_seconds"
	MetricConsumerLagRows       = "pg_connector_issue_jira.consumer.lag_rows"
	MetricConsumerOldestUnacked = "pg_connector_issue_jira.consumer.oldest_unacked_age_seconds"
	MetricConsumerRedeliveries  = "pg_connector_issue_jira.consumer.redeliveries"
	MetricQueryLastSuccessAge   = "pg_connector_issue_jira.query.last_success_age_seconds"
	MetricWatchedEntities       = "pg_connector_issue_jira.watched.entities"

	meterName                 = "pg-connector-issue-jira"
	metricPrefix              = "pg_connector_issue_jira."
	spanRequest               = "pg_connector_issue_jira.request"
	spanOriginCall            = "pg_connector_issue_jira.origin_call"
	none                      = "none"
	unknown                   = "unknown"
	other                     = "other"
	maxReasonLabelLen         = 40
	pausedReasonWhenNotPaused = none
)

// The tracked-entity classes, the label values of tracked.entities.
const (
	ClassMembers     = "members"
	ClassOtherOpen   = "other_open"
	ClassClosedGrace = "closed_grace"
)

// Label keys. Their values come from closed vocabularies; see the package doc.
const (
	keyReason     = "reason"
	keyClass      = "class"
	keyOp         = "op"
	keyServedFrom = "served_from"
	keyResult     = "result"
	keyGroup      = "group"
	keyPurpose    = "purpose"
)

// Options configure a Telemetry. Every polled source is optional: a nil source
// leaves the series it feeds unreported.
type Options struct {
	// MeterProvider and TracerProvider are where the instruments and spans
	// go; nil means the global providers Setup installed (no-op when Setup
	// found no endpoint).
	MeterProvider  metric.MeterProvider
	TracerProvider trace.TracerProvider

	// Governor returns the governor's state, polled for budget.remaining and
	// governor.paused (governor.Governor.State); nil leaves them unreported.
	Governor func() governor.State
	// Bucket is the governor bucket those two read; empty means origin.Bucket.
	Bucket governor.Bucket
	// Readings is the holder of the origin's latest rate-limit reading, polled
	// for the origin.* gauges; nil leaves them unreported (origin.floor too).
	Readings *origin.Readings
	// Floor is rate_reserve_requests, reported as origin.floor and subtracted
	// from the reading's remaining count for origin.headroom.
	Floor int
	// Scheduler returns the scheduler's state, polled for queue.depth and
	// queue.oldest_overdue_seconds (scheduler.Scheduler.State); nil leaves
	// them unreported.
	Scheduler func() scheduler.State

	// KnownOp says whether op is registered under instance. The request
	// middleware labels a request with its op only when this says yes, and
	// "unknown" otherwise, so a client cannot mint label values by sending
	// made-up ops. nil trusts every op name.
	KnownOp func(instance, op string) bool

	// StorePath is the SQLite store file (<state dir>/config.StoreFileName).
	// When set, store.bytes is polled as the size of that file plus its -wal and
	// -shm siblings; empty leaves the gauge unreported.
	StorePath string

	// Now is the clock the request duration, the spans and the polled ages
	// use; nil means time.Now.
	Now func() time.Time

	// Tracked returns the tracked-entity class counts, polled for
	// tracked.entities (summary.Classes); nil leaves it unreported.
	Tracked func(context.Context) (summary.Counts, error)
	// RowsWithoutKind returns the count of stored change rows that carry no
	// kind, polled for change.rows_without_kind (CountRowsWithoutKind); nil
	// leaves it unreported.
	RowsWithoutKind func(context.Context) (int, error)

	// Pending returns the stored pending-change row count, polled for
	// change.settle_pending (store.Store.CountPending); nil leaves it
	// unreported.
	Pending func(context.Context) (int, error)
	// Consumers returns every stored consumer key's position, polled for
	// consumer.lag_rows and consumer.oldest_unacked_age_seconds
	// (feed.Feed.Positions); nil leaves them unreported. The consumer names it
	// returned are also the only values the consumer label of
	// consumer.redeliveries may carry.
	Consumers func(context.Context) ([]feed.Position, error)
	// Queries returns the stored query rows, polled with ConfiguredQueries for
	// query.last_success_age_seconds (store.Store.ListQueries); nil leaves it
	// unreported.
	Queries func(context.Context) ([]store.QueryRow, error)
	// ConfiguredQueries names the configured queries: the only values the query
	// label may carry (any other folds to "other") and the series
	// query.last_success_age_seconds reports. nil means none.
	ConfiguredQueries func() []string
	// Started is when the daemon started: the age of a configured query that
	// never succeeded counts from it. Zero means when New ran.
	Started time.Time
	// Watched returns the count of watched entities, polled for
	// watched.entities (store.Store.CountWatched for the issue kind); nil
	// leaves it unreported.
	Watched func(context.Context) (int, error)
}

// Telemetry holds the instruments of the daemon's metric table and records the
// request and origin-call spans. It is safe for concurrent use.
type Telemetry struct {
	opts   Options
	tracer trace.Tracer
	now    func() time.Time
	bucket governor.Bucket

	up               metric.Int64ObservableGauge
	remaining        metric.Int64ObservableGauge
	paused           metric.Int64ObservableGauge
	originRemaining  metric.Int64ObservableGauge
	originLimit      metric.Int64ObservableGauge
	originNearLimit  metric.Int64ObservableGauge
	originReset      metric.Int64ObservableGauge
	originFloor      metric.Int64ObservableGauge
	originHeadroom   metric.Int64ObservableGauge
	originReadingAge metric.Float64ObservableGauge
	depth            metric.Int64ObservableGauge
	overdue          metric.Float64ObservableGauge
	tracked          metric.Int64ObservableGauge
	noKind           metric.Int64ObservableGauge
	requests         metric.Int64Counter
	duration         metric.Float64Histogram

	staleness  metric.Float64Histogram
	storeBytes metric.Int64ObservableGauge
	evictions  metric.Int64Counter

	// changes holds the change feed's and change engine's instruments
	// (pkg/connectord/changemetrics), registered under metricPrefix.
	changes *changemetrics.Metrics

	registration metric.Registration
}

// New creates the instruments and registers the polled-gauge callback.
func New(opts Options) (*Telemetry, error) {
	mp := opts.MeterProvider
	if mp == nil {
		mp = otel.GetMeterProvider()
	}
	tp := opts.TracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	t := &Telemetry{opts: opts, tracer: tp.Tracer(meterName), now: opts.Now, bucket: opts.Bucket}
	if t.now == nil {
		t.now = time.Now
	}
	if t.bucket == "" {
		t.bucket = origin.Bucket
	}
	m := mp.Meter(meterName)

	var err error
	if t.up, err = m.Int64ObservableGauge(MetricDaemonUp,
		metric.WithDescription("1 while the daemon is running"), metric.WithUnit("1")); err != nil {
		return nil, err
	}
	if t.paused, err = m.Int64ObservableGauge(MetricGovernorPaused,
		metric.WithDescription("1 while background work is held back (reason names why), else 0 with reason none"),
		metric.WithUnit("1")); err != nil {
		return nil, err
	}
	if t.requests, err = m.Int64Counter(MetricJiraRequests,
		metric.WithDescription("Requests made to Jira by result: ok, error, rate_limited, unauthenticated or refused"),
		metric.WithUnit("{request}")); err != nil {
		return nil, err
	}
	if t.remaining, err = m.Int64ObservableGauge(MetricBudgetRemaining,
		metric.WithDescription("Headroom under the configured request rate: the cap minus the spend in the current window"),
		metric.WithUnit("{request}")); err != nil {
		return nil, err
	}
	if t.originRemaining, err = m.Int64ObservableGauge(MetricOriginRemaining,
		metric.WithDescription("Requests left in the origin burst window, as the last response reported it (X-RateLimit-Remaining); absent when no reading is current"), metric.WithUnit("{request}")); err != nil {
		return nil, err
	}
	if t.originLimit, err = m.Int64ObservableGauge(MetricOriginLimit,
		metric.WithDescription("Requests per burst window the origin allows (X-RateLimit-Limit); absent when no reading is current"), metric.WithUnit("{request}")); err != nil {
		return nil, err
	}
	if t.originNearLimit, err = m.Int64ObservableGauge(MetricOriginNearLimit,
		metric.WithDescription("1 when the origin says the tenant is below 20% of its burst capacity (X-RateLimit-NearLimit), else 0"), metric.WithUnit("1")); err != nil {
		return nil, err
	}
	if t.originReset, err = m.Int64ObservableGauge(MetricOriginReset,
		metric.WithDescription("Unix time the origin said the burst window resets (X-RateLimit-Reset, sent with a 429); absent when none was stated"), metric.WithUnit("s")); err != nil {
		return nil, err
	}
	if t.originFloor, err = m.Int64ObservableGauge(MetricOriginFloor,
		metric.WithDescription("The configured floor, rate_reserve_requests: nothing spends below it"), metric.WithUnit("{request}")); err != nil {
		return nil, err
	}
	if t.originHeadroom, err = m.Int64ObservableGauge(MetricOriginHeadroom,
		metric.WithDescription("origin.remaining minus origin.floor; negative means the floor is breached"), metric.WithUnit("{request}")); err != nil {
		return nil, err
	}
	if t.originReadingAge, err = m.Float64ObservableGauge(MetricOriginReadingAgeSecs,
		metric.WithDescription("Age of the origin's last rate-limit reading; absent before the first"),
		metric.WithUnit("s")); err != nil {
		return nil, err
	}
	if t.depth, err = m.Int64ObservableGauge(MetricQueueDepth,
		metric.WithDescription("Queued refresh tasks per priority class"), metric.WithUnit("{task}")); err != nil {
		return nil, err
	}
	if t.overdue, err = m.Float64ObservableGauge(MetricQueueOldestOverdue,
		metric.WithDescription("How far past its due time the most overdue queued task of a class is"),
		metric.WithUnit("s")); err != nil {
		return nil, err
	}
	if t.staleness, err = m.Float64Histogram(MetricGroupStaleness,
		metric.WithDescription("How far past fresh_until the served group data is, per answered request; zero when fresh"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(0, 1, 5, 15, 30, 60, 120, 300, 900, 1800, 3600, 7200, 21600, 86400)); err != nil {
		return nil, err
	}
	if t.duration, err = m.Float64Histogram(MetricRequestDuration,
		metric.WithDescription("Caller-visible request latency by op and where the answer came from"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30)); err != nil {
		return nil, err
	}
	if t.tracked, err = m.Int64ObservableGauge(MetricTrackedEntities,
		metric.WithDescription("Tracked issues by class: members of a configured query, other open, and closed within the grace period"),
		metric.WithUnit("{entity}")); err != nil {
		return nil, err
	}
	if t.storeBytes, err = m.Int64ObservableGauge(MetricStoreBytes,
		metric.WithDescription("Size of the store file plus its WAL and shared-memory files"),
		metric.WithUnit("By")); err != nil {
		return nil, err
	}
	if t.evictions, err = m.Int64Counter(MetricStoreEvictions,
		metric.WithDescription("Rows the expiry sweep evicted from the store"),
		metric.WithUnit("{row}")); err != nil {
		return nil, err
	}
	if t.noKind, err = m.Int64ObservableGauge(MetricRowsWithoutKind,
		metric.WithDescription("Stored change rows that carry no kind; it MUST be zero"),
		metric.WithUnit("{row}")); err != nil {
		return nil, err
	}

	if t.changes, err = changemetrics.New(m, changemetrics.Config{
		Prefix:  metricPrefix,
		Kinds:   changes.Spec().Changes,
		Started: opts.Started,
		Now:     t.now,
		Sources: changemetrics.Sources{
			Pending:           opts.Pending,
			Consumers:         opts.Consumers,
			Queries:           opts.Queries,
			ConfiguredQueries: opts.ConfiguredQueries,
			Watched:           opts.Watched,
		},
	}); err != nil {
		return nil, err
	}

	t.registration, err = m.RegisterCallback(t.observe,
		t.up, t.remaining, t.paused, t.depth, t.overdue, t.tracked, t.storeBytes, t.noKind,
		t.originRemaining, t.originLimit, t.originNearLimit, t.originReset, t.originFloor, t.originHeadroom, t.originReadingAge)
	if err != nil {
		_ = t.changes.Close()
		return nil, err
	}
	return t, nil
}

// Close unregisters the gauge callbacks.
func (t *Telemetry) Close() error {
	return errors.Join(t.registration.Unregister(), t.changes.Close())
}

// observe is the polled-gauge callback: it reads the governor's, the
// scheduler's and the store's state at collection time. A failing source skips
// only its own series.
func (t *Telemetry) observe(ctx context.Context, o metric.Observer) error {
	o.ObserveInt64(t.up, 1)
	if t.opts.StorePath != "" {
		o.ObserveInt64(t.storeBytes, storeSize(t.opts.StorePath))
	}

	if t.opts.Governor != nil {
		if b, ok := t.opts.Governor().Bucket(t.bucket); ok {
			if b.Cap > 0 {
				o.ObserveInt64(t.remaining, max(b.Cap-b.Spent, 0))
			}
			if b.Paused {
				o.ObserveInt64(t.paused, 1, metric.WithAttributes(attribute.String(keyReason, reasonLabel(b.PauseReason))))
			} else {
				o.ObserveInt64(t.paused, 0, metric.WithAttributes(attribute.String(keyReason, pausedReasonWhenNotPaused)))
			}
		}
	}
	t.observeOrigin(o)
	if t.opts.Scheduler != nil {
		for _, c := range t.opts.Scheduler().Classes {
			class := metric.WithAttributes(attribute.String(keyClass, classLabel(c.Class)))
			o.ObserveInt64(t.depth, int64(c.Depth), class)
			o.ObserveFloat64(t.overdue, c.OldestOverdue.Seconds(), class)
		}
	}
	if t.opts.Tracked != nil {
		if c, err := t.opts.Tracked(ctx); err == nil {
			for class, n := range map[string]int{
				ClassMembers: c.Members, ClassOtherOpen: c.OtherOpen, ClassClosedGrace: c.ClosedGrace,
			} {
				o.ObserveInt64(t.tracked, int64(n), metric.WithAttributes(attribute.String(keyClass, class)))
			}
		}
	}
	if t.opts.RowsWithoutKind != nil {
		if n, err := t.opts.RowsWithoutKind(ctx); err == nil {
			o.ObserveInt64(t.noKind, int64(n))
		}
	}
	return nil
}

// observeOrigin reports the origin.* series: the floor always, the rest from
// the latest rate-limit reading while it is current. A key the reading did not
// carry leaves its series unreported rather than zero.
func (t *Telemetry) observeOrigin(o metric.Observer) {
	if t.opts.Readings == nil {
		return
	}
	o.ObserveInt64(t.originFloor, int64(t.opts.Floor))
	now := t.now()
	l, ok := t.opts.Readings.Current(now)
	if !ok {
		return
	}
	o.ObserveFloat64(t.originReadingAge, l.Age(now).Seconds())
	if l.Remaining != nil {
		o.ObserveInt64(t.originRemaining, *l.Remaining)
		o.ObserveInt64(t.originHeadroom, *l.Remaining-int64(t.opts.Floor))
	}
	if l.Limit != nil {
		o.ObserveInt64(t.originLimit, *l.Limit)
	}
	if l.NearLimit != nil {
		v := int64(0)
		if *l.NearLimit {
			v = 1
		}
		o.ObserveInt64(t.originNearLimit, v)
	}
	if at, ok := l.ResetAt(); ok {
		o.ObserveInt64(t.originReset, at.Unix())
	}
}

// Request is the origin.Observer: it counts one Jira request under its result
// word (origin.Options.Observer takes the *Telemetry as is). The call's
// duration rides on the origin-call span (OnSpend), not on a metric; a result
// that is not one of the five words folds to "error".
func (t *Telemetry) Request(result string, _ time.Duration) {
	if t == nil {
		return
	}
	t.requests.Add(context.Background(), 1, metric.WithAttributes(attribute.String(keyResult, requestResultLabel(result))))
}

// OnSpend is the governor's spend observer (governor.Options.OnSpend). It
// records an origin-call span; the governor's Spend carries no context, so the
// span is a root span. A call site that holds the request context and wants the
// span parented under the request calls RecordSpend instead, and installs one of
// the two, never both.
func (t *Telemetry) OnSpend(ev governor.SpendEvent) {
	if t == nil {
		return
	}
	t.RecordSpend(context.Background(), ev)
}

// RecordSpend records one origin-call span, a child of the span in ctx,
// carrying the call's cost, class, purpose, group, batch size and outcome. The
// call's end time is the event's.
func (t *Telemetry) RecordSpend(ctx context.Context, ev governor.SpendEvent) {
	if t == nil {
		return
	}
	at := ev.At
	if at.IsZero() {
		at = t.now()
	}
	_, span := t.tracer.Start(ctx, spanOriginCall,
		trace.WithTimestamp(at),
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.Int64("cost", ev.Cost),
			attribute.String(keyClass, classLabel(ev.Class)),
			attribute.String(keyPurpose, purposeLabel(ev.Purpose)),
			attribute.String(keyGroup, groupLabel(ev.Group)),
			attribute.Int("batch_size", ev.BatchSize),
			attribute.String("outcome", ev.Outcome),
		))
	if ev.Outcome != "" && ev.Outcome != "ok" {
		span.SetStatus(codes.Error, ev.Outcome)
	}
	span.End(trace.WithTimestamp(at))
}

// RecordEvictions adds n to store.evictions, the count the expiry sweep reports
// (summary.Sweep). A non-positive n records nothing.
func (t *Telemetry) RecordEvictions(n int) {
	if t != nil && n > 0 {
		t.evictions.Add(context.Background(), int64(n))
	}
}

// requestResultLabel passes one of the six result words origin reports and
// folds anything else to "error".
func requestResultLabel(result string) string {
	switch result {
	case origin.ResultOK, origin.ResultError, origin.ResultRateLimited,
		origin.ResultUnauthenticated, origin.ResultRefused, origin.ResultNotFound:
		return result
	default:
		return origin.ResultError
	}
}

func purposeLabel(p governor.Purpose) string {
	switch p {
	case governor.PurposeInteractive, governor.PurposeWrite, governor.PurposeQuery, governor.PurposeRefresh,
		governor.PurposeSettle, governor.PurposeFill, governor.PurposePassthrough:
		return string(p)
	default:
		return unknown
	}
}

// groupLabel passes the field group a call fetched. Groups are the connector's
// own fixed names; an unset one (a call not tied to a group) is "none".
func groupLabel(g string) string {
	if g == "" {
		return none
	}
	return g
}

func classLabel(c governor.Class) string {
	switch c {
	case governor.ClassInteractive:
		return "interactive"
	case governor.ClassInvalidated:
		return "invalidated"
	case governor.ClassQuery:
		return "query"
	case governor.ClassKeepAlive:
		return "keepalive"
	case governor.ClassFill:
		return "fill"
	default:
		return unknown
	}
}

// reasonLabel reduces a pause reason to its leading phrase. The governor's
// reasons are sentences with the particulars appended ("rate limited (retry
// after 1m0s)"); the particulars vary per pause and MUST NOT become a label,
// the phrase before them names the kind.
func reasonLabel(reason string) string {
	r := strings.ToLower(strings.TrimSpace(reason))
	if i := strings.IndexAny(r, "(:,;"); i >= 0 {
		r = strings.TrimSpace(r[:i])
	}
	r = strings.Join(strings.Fields(r), "_")
	if r == "" {
		return other
	}
	if len(r) > maxReasonLabelLen {
		r = r[:maxReasonLabelLen]
	}
	return r
}

// storeSize is the summed size of the store file and its -wal and -shm
// siblings; a file that does not exist counts as zero.
func storeSize(path string) int64 {
	var total int64
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if fi, err := os.Stat(p); err == nil {
			total += fi.Size()
		}
	}
	return total
}
