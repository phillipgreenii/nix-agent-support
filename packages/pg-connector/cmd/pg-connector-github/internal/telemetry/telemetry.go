package telemetry

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/changemetrics"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Metric names, after the pa-monitor convention (dotted, one service prefix).
const (
	MetricDaemonUp           = "pg_connector_github.daemon.up"
	MetricBudgetSpent        = "pg_connector_github.budget.spent"
	MetricBudgetRemaining    = "pg_connector_github.budget.remaining"
	MetricGovernorPaused     = "pg_connector_github.governor.paused"
	MetricGovernorBacklog    = "pg_connector_github.governor.backlog"
	MetricQueueDepth         = "pg_connector_github.queue.depth"
	MetricQueueOldestOverdue = "pg_connector_github.queue.oldest_overdue_seconds"
	MetricRequestDuration    = "pg_connector_github.request.duration"
	MetricBatchIDs           = "pg_connector_github.batch.ids"
	MetricRevalidation       = "pg_connector_github.revalidation"
	MetricGroupStaleness     = "pg_connector_github.group.staleness_seconds"
	MetricStoreBytes         = "pg_connector_github.store.bytes"
	MetricStoreEvictions     = "pg_connector_github.store.evictions"

	MetricChangeSettle          = "pg_connector_github.change.settle"
	MetricChangeSettlePending   = "pg_connector_github.change.settle_pending"
	MetricChangeDetectionDelay  = "pg_connector_github.change.detection_delay_seconds"
	MetricConsumerLagRows       = "pg_connector_github.consumer.lag_rows"
	MetricConsumerOldestUnacked = "pg_connector_github.consumer.oldest_unacked_age_seconds"
	MetricConsumerRedeliveries  = "pg_connector_github.consumer.redeliveries"
	MetricQueryLastSuccessAge   = "pg_connector_github.query.last_success_age_seconds"
	MetricWatchedEntities       = "pg_connector_github.watched.entities"
	MetricTrackedEntities       = "pg_connector_github.tracked.entities"
	MetricTrackedLimit          = "pg_connector_github.tracked.limit"

	meterName                 = "pg-connector-github"
	metricPrefix              = "pg_connector_github."
	spanRequest               = "pg_connector_github.request"
	spanOriginCall            = "pg_connector_github.origin_call"
	none                      = "none"
	unknown                   = "unknown"
	other                     = "other"
	maxReasonLabelLen         = 40
	pausedReasonWhenNotPaused = none
)

// The tracked-entity classes, the label values of tracked.entities. Members and
// other_open are the keep-alive set (the PRs the daemon refreshes in the
// background); closed_grace are closed or merged PRs the store still holds
// until terminal_ttl and then expiry take them.
const (
	ClassMembers     = "members"
	ClassOtherOpen   = "other_open"
	ClassClosedGrace = "closed_grace"
)

// TrackedCounts are the tracked-entity class counts behind tracked.entities
// (summary.Counts without the watched count, which the hoisted
// watched.entities gauge reports).
type TrackedCounts struct {
	Members, OtherOpen, ClosedGrace int
}

// Label keys. Their values come from closed vocabularies; see the package doc.
const (
	keyBucket     = "bucket"
	keyPurpose    = "purpose"
	keyGroup      = "group"
	keyReason     = "reason"
	keyClass      = "class"
	keyOp         = "op"
	keyServedFrom = "served_from"
	keyRole       = "role"
	keyResult     = "result"
)

// Options configure a Telemetry.
type Options struct {
	// MeterProvider and TracerProvider are where the instruments and spans
	// go; nil means the global providers Setup installed (no-op when Setup
	// found no endpoint).
	MeterProvider  metric.MeterProvider
	TracerProvider trace.TracerProvider

	// Governor returns the governor's state, polled for budget.remaining and
	// governor.paused (governor.Governor.State); nil leaves them unreported.
	Governor func() governor.State
	// Scheduler returns the scheduler's state, polled for queue.depth and
	// queue.oldest_overdue_seconds (scheduler.Scheduler.State); nil leaves
	// them unreported.
	Scheduler func() scheduler.State

	// KnownOp says whether op is registered under instance. The request
	// middleware labels a request with its op only when this says yes, and
	// "unknown" otherwise, so a client cannot mint label values by sending
	// made-up ops. nil trusts every op name (the router answers unknown_op for
	// the ones nobody registered, but the label would still carry the name).
	KnownOp func(instance, op string) bool

	// StorePath is the SQLite store file (<state dir>/config.StoreFileName).
	// When set, store.bytes is polled as the size of that file plus its -wal and
	// -shm siblings; empty leaves the gauge unreported.
	StorePath string

	// Now is the clock the request duration, the spans and the polled ages
	// use; nil means time.Now.
	Now func() time.Time

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
	// watched.entities (store.Store.CountWatched for the entity kind); nil
	// leaves it unreported.
	Watched func(context.Context) (int, error)
	// Tracked returns the tracked-entity class counts, polled for
	// tracked.entities (summary.Service.Classes); nil leaves it unreported.
	Tracked func(context.Context) (TrackedCounts, error)
	// TrackedLimit is the configured tracked-set limit, exported as tracked.limit
	// so an alert can compare tracked.entities with it; zero or negative means
	// none is configured and the gauge is not reported.
	TrackedLimit int
}

// Telemetry holds the instruments of the daemon's metric table and records the
// request and origin-call spans. It is safe for concurrent use.
type Telemetry struct {
	opts   Options
	tracer trace.Tracer
	now    func() time.Time

	up        metric.Int64ObservableGauge
	remaining metric.Int64ObservableGauge
	paused    metric.Int64ObservableGauge
	backlog   metric.Int64ObservableGauge
	depth     metric.Int64ObservableGauge
	overdue   metric.Float64ObservableGauge
	spent     metric.Int64Counter
	duration  metric.Float64Histogram
	batchIDs  metric.Int64Histogram

	revalidation metric.Int64Counter
	staleness    metric.Float64Histogram
	storeBytes   metric.Int64ObservableGauge
	evictions    metric.Int64Counter

	tracked      metric.Int64ObservableGauge
	trackedLimit metric.Int64ObservableGauge

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
	t := &Telemetry{opts: opts, tracer: tp.Tracer(meterName), now: opts.Now}
	if t.now == nil {
		t.now = time.Now
	}
	m := mp.Meter(meterName)

	var err error
	if t.up, err = m.Int64ObservableGauge(MetricDaemonUp,
		metric.WithDescription("1 while the daemon is running"), metric.WithUnit("1")); err != nil {
		return nil, err
	}
	if t.spent, err = m.Int64Counter(MetricBudgetSpent,
		metric.WithDescription("Origin spend by bucket, purpose and group, in the bucket's unit (GraphQL points, REST requests)"),
		metric.WithUnit("{cost}")); err != nil {
		return nil, err
	}
	if t.remaining, err = m.Int64ObservableGauge(MetricBudgetRemaining,
		metric.WithDescription("What the origin reports left in a bucket; absent while no reading is in force"),
		metric.WithUnit("{cost}")); err != nil {
		return nil, err
	}
	if t.paused, err = m.Int64ObservableGauge(MetricGovernorPaused,
		metric.WithDescription("1 while a bucket's background work is held back (reason names why), else 0 with reason none"),
		metric.WithUnit("1")); err != nil {
		return nil, err
	}
	if t.backlog, err = m.Int64ObservableGauge(MetricGovernorBacklog,
		metric.WithDescription("Queued refresh tasks per priority class that are due now and have not gone out: the work the workers and the governor have not got to"),
		metric.WithUnit("{task}")); err != nil {
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
	if t.duration, err = m.Float64Histogram(MetricRequestDuration,
		metric.WithDescription("Caller-visible request latency by op and where the answer came from"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30)); err != nil {
		return nil, err
	}
	if t.batchIDs, err = m.Int64Histogram(MetricBatchIDs,
		metric.WithDescription("Entity ids per origin batch, by role (due or fill)"),
		metric.WithUnit("{id}"),
		metric.WithExplicitBucketBoundaries(1, 2, 5, 10, 20, 30, 50, 75, 100)); err != nil {
		return nil, err
	}

	if t.revalidation, err = m.Int64Counter(MetricRevalidation,
		metric.WithDescription("Group revalidations by group and outcome: extended, repulled or forced_by_max_age"),
		metric.WithUnit("{revalidation}")); err != nil {
		return nil, err
	}
	if t.staleness, err = m.Float64Histogram(MetricGroupStaleness,
		metric.WithDescription("How far past fresh_until the served group data is, per answered request; zero when fresh"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(0, 1, 5, 15, 30, 60, 120, 300, 900, 1800, 3600, 7200, 21600, 86400)); err != nil {
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

	if t.tracked, err = m.Int64ObservableGauge(MetricTrackedEntities,
		metric.WithDescription("Tracked pull requests by class: members of a configured query, other open (kept by a read or a watch), and closed within the grace period"),
		metric.WithUnit("{entity}")); err != nil {
		return nil, err
	}
	if t.trackedLimit, err = m.Int64ObservableGauge(MetricTrackedLimit,
		metric.WithDescription("The configured tracked-set limit (tracked_set_limit); absent while none is configured"),
		metric.WithUnit("{entity}")); err != nil {
		return nil, err
	}

	if t.changes, err = changemetrics.New(m, changemetrics.Config{
		Prefix:  metricPrefix,
		Kinds:   classify.Kinds(),
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

	t.registration, err = m.RegisterCallback(t.observe, t.up, t.remaining, t.paused, t.backlog, t.depth, t.overdue, t.storeBytes, t.tracked, t.trackedLimit)
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

// observe is the polled-gauge callback: it reads the governor's and the
// scheduler's state at collection time.
func (t *Telemetry) observe(ctx context.Context, o metric.Observer) error {
	o.ObserveInt64(t.up, 1)
	if t.opts.StorePath != "" {
		o.ObserveInt64(t.storeBytes, storeSize(t.opts.StorePath))
	}

	if t.opts.Governor != nil {
		for _, b := range t.opts.Governor().Buckets {
			bucket := attribute.String(keyBucket, bucketLabel(b.Bucket))
			if b.Remaining >= 0 {
				o.ObserveInt64(t.remaining, b.Remaining, metric.WithAttributes(bucket))
			}
			if b.Paused {
				o.ObserveInt64(t.paused, 1, metric.WithAttributes(bucket, attribute.String(keyReason, reasonLabel(b.PauseReason))))
			} else {
				o.ObserveInt64(t.paused, 0, metric.WithAttributes(bucket, attribute.String(keyReason, pausedReasonWhenNotPaused)))
			}
		}
	}
	if t.opts.Scheduler != nil {
		for _, c := range t.opts.Scheduler().Classes {
			class := metric.WithAttributes(attribute.String(keyClass, classLabel(c.Class)))
			o.ObserveInt64(t.depth, int64(c.Depth), class)
			o.ObserveInt64(t.backlog, int64(c.Backlog), class)
			o.ObserveFloat64(t.overdue, c.OldestOverdue.Seconds(), class)
		}
	}
	if t.opts.Tracked != nil {
		if c, err := t.opts.Tracked(ctx); err == nil {
			for _, kv := range []struct {
				class string
				n     int
			}{{ClassMembers, c.Members}, {ClassOtherOpen, c.OtherOpen}, {ClassClosedGrace, c.ClosedGrace}} {
				o.ObserveInt64(t.tracked, int64(kv.n), metric.WithAttributes(attribute.String(keyClass, kv.class)))
			}
		}
	}
	if t.opts.TrackedLimit > 0 {
		o.ObserveInt64(t.trackedLimit, int64(t.opts.TrackedLimit))
	}
	return nil
}

// OnSpend is the governor's spend observer (governor.Options.OnSpend). It
// records budget.spent and an origin-call span. The governor's Spend carries no
// context, so the span here is a root span; a call site that holds the request
// context and wants the span parented under the request calls RecordSpend
// instead, and installs one of the two, never both (the counter would count
// twice).
func (t *Telemetry) OnSpend(ev governor.SpendEvent) { t.RecordSpend(context.Background(), ev) }

// RecordSpend adds the call's cost to budget.spent (labels bucket, purpose,
// group) and records one origin-call span, a child of the span in ctx, carrying
// the call's cost, bucket, class, purpose, group, batch size and outcome. The
// call's end time is the event's.
func (t *Telemetry) RecordSpend(ctx context.Context, ev governor.SpendEvent) {
	if ev.Cost > 0 {
		t.spent.Add(ctx, ev.Cost, metric.WithAttributes(
			attribute.String(keyBucket, bucketLabel(ev.Bucket)),
			attribute.String(keyPurpose, purposeLabel(ev.Purpose)),
			attribute.String(keyGroup, groupLabel(ev.Group)),
		))
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
			attribute.String(keyBucket, bucketLabel(ev.Bucket)),
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

// OnBatchSent is the summary service's batch observer
// (summary.ServiceConfig.OnBatchSent): one batch.ids observation per call, the
// number of ids the batch carried in that role.
func (t *Telemetry) OnBatchSent(ids []string, role scheduler.Role) {
	t.batchIDs.Record(context.Background(), int64(len(ids)),
		metric.WithAttributes(attribute.String(keyRole, roleLabel(role))))
}

// Middleware is the router's request middleware (router.Router.Use): it opens
// the request span, runs the request, then records request.duration and
// completes the span with what the handler annotated (served_from, the wait).
// The span's context reaches the handler, so RecordSpend called with it
// parents each origin call under the request.
func (t *Telemetry) Middleware() router.Middleware {
	return func(next router.Func) router.Func {
		return func(ctx context.Context, c router.Call) scriptout.Response {
			op := t.opLabel(c)
			start := t.now()
			ctx, span := t.tracer.Start(ctx, spanRequest,
				trace.WithTimestamp(start),
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(attribute.String(keyOp, op), attribute.String("instance", instanceLabel(c.Instance))))

			resp := next(ctx, c)

			end := t.now()
			ann := router.AnnotationsFrom(ctx)
			served := servedFromLabel(ann.ServedFrom)
			span.SetAttributes(
				attribute.String(keyServedFrom, served),
				attribute.Float64("wait_seconds", ann.Wait.Seconds()),
			)
			if resp.Error != nil {
				span.SetStatus(codes.Error, resp.Error.Code)
				span.SetAttributes(attribute.String("error.code", resp.Error.Code))
			}
			span.End(trace.WithTimestamp(end))

			t.duration.Record(ctx, end.Sub(start).Seconds(), metric.WithAttributes(
				attribute.String(keyOp, op),
				attribute.String(keyServedFrom, served),
			))
			return resp
		}
	}
}

func (t *Telemetry) opLabel(c router.Call) string {
	op := c.Request.Op
	if op == "" || (t.opts.KnownOp != nil && !t.opts.KnownOp(c.Instance, op)) {
		return unknown
	}
	return op
}

// instanceLabel bounds the instance span attribute: the daemon serves "pr" and
// "ci"; anything else is a client's invention.
func instanceLabel(instance string) string {
	switch instance {
	case "pr", "ci":
		return instance
	default:
		return unknown
	}
}

func bucketLabel(b governor.Bucket) string {
	for _, k := range governor.Buckets() {
		if b == k {
			return string(b)
		}
	}
	return unknown
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

// groupLabel passes the field group the call fetched. Groups are the
// connector's own fixed names; an unset one (a call not tied to a group) is
// "none".
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

func roleLabel(r scheduler.Role) string {
	switch r {
	case scheduler.RoleDue, scheduler.RoleFill:
		return string(r)
	default:
		return other
	}
}

func servedFromLabel(s string) string {
	if s == "" {
		return none
	}
	return s
}

// reasonLabel reduces a pause reason to its leading phrase. The governor's
// reasons are sentences with the particulars appended ("secondary rate limit
// (HTTP 403, retry after 1m0s)"); the particulars vary per pause and MUST NOT
// become a label, the phrase before them names the kind.
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
