package metrics

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/pg-router/internal/eventqueue"
)

// harness wires a ManualReader-backed meter provider to an Emitter and a real
// queue (the Emitter as the queue's Observer), so metrics are exercised
// end-to-end through the queue.
type harness struct {
	reader  *sdkmetric.ManualReader
	mp      metric.MeterProvider
	emitter *Emitter
	q       *eventqueue.Queue
	clk     *mockClock
}

type mockClock struct{ t time.Time }

func (c *mockClock) now() time.Time          { return c.t }
func (c *mockClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newHarness(t *testing.T) *harness {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	clk := &mockClock{t: time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)}

	var q *eventqueue.Queue
	emitter, err := New(mp, func() map[string]int { return q.DepthByType() }, WithClock(clk.now))
	if err != nil {
		t.Fatalf("New emitter: %v", err)
	}
	q, err = eventqueue.New(eventqueue.NewMemStore(),
		eventqueue.WithClock(clk.now), eventqueue.WithObserver(emitter))
	if err != nil {
		t.Fatalf("New queue: %v", err)
	}
	return &harness{reader: reader, mp: mp, emitter: emitter, q: q, clk: clk}
}

func (h *harness) collect(t *testing.T) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := h.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	return rm
}

// findMetric returns the metric by name, or fails.
func findMetric(t *testing.T, rm metricdata.ResourceMetrics, name string) metricdata.Metrics {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return m
			}
		}
	}
	t.Fatalf("metric %q not found in collected data", name)
	return metricdata.Metrics{}
}

// sumFor returns the counter value for the given attribute key=value, summed
// across every datapoint carrying it (a counter with an additional label such
// as "role" has one datapoint per label combination), or -1 if none match.
func sumFor(m metricdata.Metrics, key, value string) int64 {
	s, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		return -1
	}
	var total int64
	found := false
	for _, dp := range s.DataPoints {
		if v, present := dp.Attributes.Value(attribute.Key(key)); present && v.AsString() == value {
			total += dp.Value
			found = true
		}
	}
	if !found {
		return -1
	}
	return total
}

// sumForBoth is sumFor widened to match on TWO attribute key/value pairs at
// once (bead pg2-j4uwg): once a counter carries more than one label
// dimension (e.g. "class" AND "reason"), a single-key match can return the
// wrong datapoint's value when several datapoints share that one key's
// value but differ on the other.
func sumForBoth(m metricdata.Metrics, key1, value1, key2, value2 string) int64 {
	s, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		return -1
	}
	for _, dp := range s.DataPoints {
		v1, ok1 := dp.Attributes.Value(attribute.Key(key1))
		v2, ok2 := dp.Attributes.Value(attribute.Key(key2))
		if ok1 && v1.AsString() == value1 && ok2 && v2.AsString() == value2 {
			return dp.Value
		}
	}
	return -1
}

// gaugeVal scans the whole collected set for the named gauge's datapoint with
// the given attribute, returning -1 when the metric or datapoint is absent (an
// observable gauge emits NO metric when its callback observes nothing, e.g. an
// empty queue — that absence means "zero").
func gaugeVal(rm metricdata.ResourceMetrics, name, key, value string) int64 {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			g, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				return -1
			}
			for _, dp := range g.DataPoints {
				if v, present := dp.Attributes.Value(attribute.Key(key)); present && v.AsString() == value {
					return dp.Value
				}
			}
		}
	}
	return -1
}

// All three instruments register with the declared kind/unit and emit on the
// right event.
func TestInstrumentsRegistered(t *testing.T) {
	h := newHarness(t)
	// Drive one of each so the instruments appear in the collected set.
	h.emitter.RecordFailure("critical")
	h.enqueue(t, "e1", "review-requested", 10*time.Minute)

	rm := h.collect(t)
	depth := findMetric(t, rm, MetricQueueDepth)
	if _, ok := depth.Data.(metricdata.Gauge[int64]); !ok {
		t.Fatalf("queue_depth is not a gauge: %T", depth.Data)
	}
	if depth.Unit != "{event}" {
		t.Fatalf("queue_depth unit = %q", depth.Unit)
	}
	failures := findMetric(t, rm, MetricFailures)
	if _, ok := failures.Data.(metricdata.Sum[int64]); !ok {
		t.Fatalf("failures is not a counter: %T", failures.Data)
	}
	// unconsumed_expired only appears once it has been incremented; assert its
	// registration indirectly through the dedicated expiry test below.
}

// TestCatalogHasTenMembers is Task 3.3's red-first test (register gaps
// R6/pg2-zqpxj, R21/pg2-00jpn, and pg2-cz31d): the catalog grows from 4 to 10
// members. WithLiveness is supplied here to prove MetricLiveness CAN
// register (the daemon-mode half of its binding decision) — see
// TestLivenessNotRegisteredWithoutOption below for the drain-mode half.
func TestCatalogHasTenMembers(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	var q *eventqueue.Queue
	emitter, err := New(mp, func() map[string]int { return q.DepthByType() }, WithLiveness(func() bool { return true }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	q, err = eventqueue.New(eventqueue.NewMemStore(), eventqueue.WithObserver(emitter))
	if err != nil {
		t.Fatalf("eventqueue.New: %v", err)
	}

	// Drive one of each so every member has a recorded/observed data point —
	// an unincremented counter or an ObservableGauge whose callback never
	// calls Observe is ABSENT from Collect, not zero (see gaugeVal's doc).
	emitter.RecordFailure(FailureClassDeclined)
	emitter.RecordFailure(FailureClassDispatchFail)
	emitter.OnUnconsumedExpired("t")
	emitter.OnUnknownTypeRejected("t")
	emitter.RecordThroughput("t", "r")
	emitter.OnSourceFailure("src", errors.New("boom"))
	emitter.OnDeduped("t")
	emitter.RecordDispatchLatency(12.5, "accepted", "r", "t")
	if _, err := q.Enqueue(eventqueue.Event{ID: "e1", Type: "t", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}

	want := []string{
		MetricQueueDepth, MetricFailures, MetricUnconsumedExpired, MetricUnknownTypeRejected,
		MetricThroughput, MetricBacklog, MetricLiveness, MetricDispatchLatency,
		MetricSourceFailures, MetricDeduped,
	}
	if len(want) != 10 {
		t.Fatalf("test bug: want has %d entries, not 10", len(want))
	}
	for _, name := range want {
		found := false
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == name {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("catalog member %q did not register on the test MeterProvider", name)
		}
	}

	// RecordFailure accepts both classes, distinguished by the "class" label.
	m := findMetric(t, rm, MetricFailures)
	if got := sumFor(m, "class", FailureClassDeclined); got != 1 {
		t.Errorf("failures[%s] = %d, want 1", FailureClassDeclined, got)
	}
	if got := sumFor(m, "class", FailureClassDispatchFail); got != 1 {
		t.Errorf("failures[%s] = %d, want 1", FailureClassDispatchFail, got)
	}
}

// MetricLiveness is registered ONLY when WithLiveness is supplied — the Task
// 3.3 binding decision's drain-mode half: drain-and-exit never registers this
// observable AT ALL, not merely never observes it live.
func TestLivenessNotRegisteredWithoutOption(t *testing.T) {
	h := newHarness(t) // no WithLiveness
	h.emitter.OnDeclined("t", "h", "busy")
	rm := h.collect(t)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == MetricLiveness {
				t.Fatalf("MetricLiveness registered without WithLiveness; want it absent entirely")
			}
		}
	}
}

// MetricLiveness reports 1 while isLive() is true, else 0 — a single scalar
// datapoint per collect, re-evaluated live on each callback invocation.
func TestLivenessReflectsIsLive(t *testing.T) {
	live := true
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	if _, err := New(mp, func() map[string]int { return nil }, WithLiveness(func() bool { return live })); err != nil {
		t.Fatalf("New: %v", err)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	m := findMetric(t, rm, MetricLiveness)
	g, ok := m.Data.(metricdata.Gauge[int64])
	if !ok || len(g.DataPoints) != 1 || g.DataPoints[0].Value != 1 {
		t.Fatalf("liveness = %+v, want a single datapoint = 1 while isLive() is true", m.Data)
	}

	live = false
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	m = findMetric(t, rm, MetricLiveness)
	g, ok = m.Data.(metricdata.Gauge[int64])
	if !ok || len(g.DataPoints) != 1 || g.DataPoints[0].Value != 0 {
		t.Fatalf("liveness = %+v, want a single datapoint = 0 once isLive() is false", m.Data)
	}
}

// MetricBacklog is a scalar sum(DepthByType()) — distinct from the existing
// per-type MetricQueueDepth gauge.
func TestBacklogIsScalarSum(t *testing.T) {
	h := newHarness(t)
	h.enqueue(t, "e1", "review-requested", 10*time.Minute)
	h.enqueue(t, "e2", "push-requested", 10*time.Minute)
	rm := h.collect(t)
	m := findMetric(t, rm, MetricBacklog)
	g, ok := m.Data.(metricdata.Gauge[int64])
	if !ok || len(g.DataPoints) != 1 || g.DataPoints[0].Value != 2 {
		t.Fatalf("backlog = %+v, want a single scalar datapoint = 2 (sum across types)", m.Data)
	}
	if got := gaugeVal(rm, MetricQueueDepth, "type", "review-requested"); got != 1 {
		t.Fatalf("queue_depth[review-requested] = %d, want 1 (per-type, unaffected by backlog's scalar)", got)
	}
}

// OnSourceFailure (Task 3.3, register gap R21 / bead pg2-00jpn, INV-FAIL-3)
// increments the source-failures counter, per source.
func TestOnSourceFailurePerSource(t *testing.T) {
	h := newHarness(t)
	h.emitter.OnSourceFailure("github-pulls", errors.New("boom"))
	h.emitter.OnSourceFailure("github-pulls", errors.New("boom"))
	h.emitter.OnSourceFailure("jira-issues", errors.New("boom"))
	m := findMetric(t, h.collect(t), MetricSourceFailures)
	if got := sumFor(m, "source", "github-pulls"); got != 2 {
		t.Fatalf("source_failures[github-pulls] = %d, want 2", got)
	}
	if got := sumFor(m, "source", "jira-issues"); got != 1 {
		t.Fatalf("source_failures[jira-issues] = %d, want 1", got)
	}
}

// rateLimitReserveErr is the verbatim error text the command query surfaced
// for the 2026-10-01/02 episode (bead pg2-hsla6): the pg-connector-pr-github
// reserve breach, wrapped by scriptout's "unavailable" envelope twice and by
// the command query's exit-status prefix.
const rateLimitReserveErr = "produce pr-mine: command query [/nix/store/x-pg-router-source-pg-connector/bin/pg-router-source-pg-connector changes pr mine --consumer pg-router]: exit status 1: pg-connector-pr-github: scriptout: unavailable: pg-connector-pr-github: scriptout: unavailable: pg-connector-pr-github: GraphQL rate limit remaining (221) is below the configured reserve (1000)"

// TestClassifySourceFailure pins the closed reason taxonomy (bead pg2-hsla6).
// The rate-limit case MUST win over the generic "scriptout: unavailable"
// wrapper it arrives in.
func TestClassifySourceFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"rate limit reserve breach (the 2026-10-01 episode)", errors.New(rateLimitReserveErr), SourceFailureRateLimited},
		{"plain rate limit phrasing", errors.New("HTTP 403: API rate limit exceeded"), SourceFailureRateLimited},
		{"scriptout unavailable", errors.New("command query [x]: exit status 1: scriptout: unavailable: backend down"), SourceFailureUnavailable},
		{"scriptout unauthenticated", errors.New("exit status 1: scriptout: unauthenticated: token expired"), SourceFailureUnauthenticated},
		{"context cancelled sentinel", fmt.Errorf("command query [x]: %w", context.Canceled), SourceFailureInterrupted},
		{"context canceled text", errors.New("command query [x]: context canceled"), SourceFailureInterrupted},
		{"child killed by a timeout (the 30s scriptout kill)", errors.New("command query [x]: exit status 1: scriptout: pg-connector-pr-github: signal: killed"), SourceFailureTimeout},
		{"bare child kill", errors.New("command query [x]: signal: killed"), SourceFailureTimeout},
		{"deadline exceeded sentinel", fmt.Errorf("command query [x]: %w", context.DeadlineExceeded), SourceFailureTimeout},
		{"deadline exceeded text", errors.New("command query [x]: context deadline exceeded"), SourceFailureTimeout},
		{"timeout wins over the generic unavailable wrapper", errors.New("exit status 1: scriptout: unavailable: backend: signal: killed"), SourceFailureTimeout},
		{"unrecognized exit", errors.New("command query [x]: exit status 1"), SourceFailureError},
		{"nil error", nil, SourceFailureError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifySourceFailure(tc.err); got != tc.want {
				t.Fatalf("ClassifySourceFailure = %q, want %q", got, tc.want)
			}
		})
	}
}

// A rate-limit-reserve breach increments the source-failures counter with
// reason="rate-limited"; an ordinary failure for the same source lands on its
// own reason series, and both are visible to the alert's `sum by (source)`.
func TestOnSourceFailureCarriesReason(t *testing.T) {
	h := newHarness(t)
	h.emitter.OnSourceFailure("pr-mine", errors.New(rateLimitReserveErr))
	h.emitter.OnSourceFailure("pr-mine", errors.New(rateLimitReserveErr))
	h.emitter.OnSourceFailure("pr-mine", errors.New("command query [x]: exit status 1"))
	m := findMetric(t, h.collect(t), MetricSourceFailures)
	if got := sumForBoth(m, "source", "pr-mine", "reason", SourceFailureRateLimited); got != 2 {
		t.Fatalf("source_failures{pr-mine,rate-limited} = %d, want 2", got)
	}
	if got := sumForBoth(m, "source", "pr-mine", "reason", SourceFailureError); got != 1 {
		t.Fatalf("source_failures{pr-mine,error} = %d, want 1", got)
	}
}

// The source-failures counter is created lazily: until a source's first
// OnSourceFailure there is NO datapoint for it, so on a live scrape an absent
// series is the normal healthy state (pg2-vicjc). This is why
// pg-router-source-failure-rate keeps noDataState: OK — see alerts.yaml.
func TestSourceFailureSeriesAbsentUntilFirstFailure(t *testing.T) {
	h := newHarness(t)
	for _, sm := range h.collect(t).ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != MetricSourceFailures {
				continue
			}
			if s, ok := m.Data.(metricdata.Sum[int64]); ok && len(s.DataPoints) > 0 {
				t.Fatalf("source_failures has %d datapoints before any failure, want none", len(s.DataPoints))
			}
		}
	}
	h.emitter.OnSourceFailure("github-pulls", errors.New("boom"))
	m := findMetric(t, h.collect(t), MetricSourceFailures)
	if got := sumFor(m, "source", "github-pulls"); got != 1 {
		t.Fatalf("source_failures[github-pulls] = %d after first failure, want 1", got)
	}
}

// OnDeduped (INV-EVT-3, bead pg2-cz31d) increments the deduped counter, per
// type.
func TestOnDedupedPerType(t *testing.T) {
	h := newHarness(t)
	h.emitter.OnDeduped("review-requested")
	h.emitter.OnDeduped("review-requested")
	h.emitter.OnDeduped("push-requested")
	m := findMetric(t, h.collect(t), MetricDeduped)
	if got := sumFor(m, "type", "review-requested"); got != 2 {
		t.Fatalf("deduped[review-requested] = %d, want 2", got)
	}
	if got := sumFor(m, "type", "push-requested"); got != 1 {
		t.Fatalf("deduped[push-requested] = %d, want 1", got)
	}
}

// RecordThroughput increments the throughput counter, per type. Exported for
// direct/test use — see its doc for why no production call site feeds it yet.
func TestRecordThroughputPerType(t *testing.T) {
	h := newHarness(t)
	h.emitter.RecordThroughput("review-requested", "worker-a")
	h.emitter.RecordThroughput("review-requested", "worker-a")
	h.emitter.RecordThroughput("review-requested", "worker-b")
	m := findMetric(t, h.collect(t), MetricThroughput)
	if got := sumFor(m, "type", "review-requested"); got != 3 {
		t.Fatalf("throughput[review-requested] = %d, want 3", got)
	}
	// Per-role split (bead pg2-nimab): the role label separates the lanes.
	if got := sumForBoth(m, "type", "review-requested", "role", "worker-a"); got != 2 {
		t.Fatalf("throughput[review-requested,role=worker-a] = %d, want 2", got)
	}
	if got := sumForBoth(m, "type", "review-requested", "role", "worker-b"); got != 1 {
		t.Fatalf("throughput[review-requested,role=worker-b] = %d, want 1", got)
	}
}

// MetricDispatchLatency is the catalog's one Histogram, registered with
// WithUnit("ms") and the exact explicit bucket boundaries the Task 3.3
// binding decision specifies.
func TestRecordDispatchLatency_HistogramWithBuckets(t *testing.T) {
	h := newHarness(t)
	h.emitter.RecordDispatchLatency(42, "accepted", "worker-a", "pr.changed")
	m := findMetric(t, h.collect(t), MetricDispatchLatency)
	if m.Unit != "ms" {
		t.Fatalf("dispatch_latency unit = %q, want ms", m.Unit)
	}
	hist, ok := m.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("dispatch_latency is not a histogram: %T", m.Data)
	}
	if len(hist.DataPoints) != 1 || hist.DataPoints[0].Count != 1 {
		t.Fatalf("dispatch_latency datapoints = %+v, want exactly one recorded value", hist.DataPoints)
	}
	wantBounds := []float64{100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000, 120000, 300000, 600000, 1200000, 1800000, 3600000}
	if !reflect.DeepEqual(hist.DataPoints[0].Bounds, wantBounds) {
		t.Fatalf("dispatch_latency bucket bounds = %v, want %v", hist.DataPoints[0].Bounds, wantBounds)
	}
	if v, ok := hist.DataPoints[0].Attributes.Value(attribute.Key("role")); !ok || v.AsString() != "worker-a" {
		t.Fatalf("dispatch_latency role label = %+v, want worker-a", hist.DataPoints[0].Attributes)
	}
	if v, ok := hist.DataPoints[0].Attributes.Value(attribute.Key("outcome")); !ok || v.AsString() != "accepted" {
		t.Fatalf("dispatch_latency outcome label = %+v, want accepted", hist.DataPoints[0].Attributes)
	}
	if v, ok := hist.DataPoints[0].Attributes.Value(attribute.Key("type")); !ok || v.AsString() != "pr" {
		t.Fatalf("dispatch_latency type label = %+v, want pr (entity type of pr.changed)", hist.DataPoints[0].Attributes)
	}
	if got := hist.DataPoints[0].Attributes.Len(); got != 3 {
		t.Fatalf("dispatch_latency has %d labels (%+v), want exactly outcome, role, type", got, hist.DataPoints[0].Attributes)
	}
}

// EntityType reduces an event type to its entity prefix and maps anything that
// is not a short lowercase identifier to the one fixed fallback, bounding the
// dispatch-latency "type" label (bead pg2-sve9v).
func TestEntityType(t *testing.T) {
	cases := []struct{ in, want string }{
		{"pr.changed", "pr"},
		{"pr.reconcile", "pr"},
		{"issue.changed", "issue"},
		{"thread.changed", "thread"},
		{"bead", "bead"},
		{"review-requested", "review-requested"},
		{"", UnknownEntityType},
		{".changed", UnknownEntityType},
		{"PR.changed", UnknownEntityType},
		{"1pr.changed", UnknownEntityType},
		{"pr id 42.changed", UnknownEntityType},
		{strings.Repeat("a", 33) + ".changed", UnknownEntityType},
		{strings.Repeat("a", 32) + ".changed", strings.Repeat("a", 32)},
	}
	for _, c := range cases {
		if got := EntityType(c.in); got != c.want {
			t.Errorf("EntityType(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Distinct verbs of one entity share one latency series, and an unparseable
// event type lands in the single fallback series: the label's cardinality is
// the number of entity types, not event types or ids.
func TestRecordDispatchLatency_TypeLabelBounded(t *testing.T) {
	h := newHarness(t)
	h.emitter.RecordDispatchLatency(1, "accepted", "w", "pr.changed")
	h.emitter.RecordDispatchLatency(1, "accepted", "w", "pr.new")
	h.emitter.RecordDispatchLatency(1, "accepted", "w", "issue.changed")
	h.emitter.RecordDispatchLatency(1, "accepted", "w", "")
	h.emitter.RecordDispatchLatency(1, "accepted", "w", "weird type!.x")
	m := findMetric(t, h.collect(t), MetricDispatchLatency)
	hist := m.Data.(metricdata.Histogram[float64])
	got := map[string]uint64{}
	for _, dp := range hist.DataPoints {
		v, _ := dp.Attributes.Value(attribute.Key("type"))
		got[v.AsString()] += dp.Count
	}
	want := map[string]uint64{"pr": 2, "issue": 1, UnknownEntityType: 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dispatch_latency counts by type = %v, want %v", got, want)
	}
}

// RecordFailure accepts all three delivery-side classes — FailureClassDeclined,
// FailureClassDispatchFail, and FailureClassHandlerError (bead pg2-97539) —
// see FailureClassDispatchFail/FailureClassHandlerError's own docs for their
// respective production call sites.
func TestRecordFailure_AcceptsThreeClasses(t *testing.T) {
	h := newHarness(t)
	h.emitter.RecordFailure(FailureClassDeclined)
	h.emitter.RecordFailure(FailureClassDispatchFail)
	h.emitter.RecordFailure(FailureClassDispatchFail)
	h.emitter.RecordFailure(FailureClassHandlerError)
	h.emitter.RecordFailure(FailureClassHandlerError)
	h.emitter.RecordFailure(FailureClassHandlerError)
	m := findMetric(t, h.collect(t), MetricFailures)
	if got := sumFor(m, "class", FailureClassDeclined); got != 1 {
		t.Fatalf("failures[%s] = %d, want 1", FailureClassDeclined, got)
	}
	if got := sumFor(m, "class", FailureClassDispatchFail); got != 2 {
		t.Fatalf("failures[%s] = %d, want 2", FailureClassDispatchFail, got)
	}
	if got := sumFor(m, "class", FailureClassHandlerError); got != 3 {
		t.Fatalf("failures[%s] = %d, want 3", FailureClassHandlerError, got)
	}
}

// OnHandlerFailure (bead pg2-97539) feeds the SAME pg_router_failures
// counter OnDeclined/OnDispatchFailure do, labeled with the THIRD
// delivery-side class — the production call site is
// orchestrator.HandlerFailureObserver.OnHandlerFailure, fed from
// roleListener.Offer's own non-panic handler error return (see
// FailureClassHandlerError's own doc). eventID/evtType are accepted for
// interface symmetry only and are not part of the failure-rate label set.
func TestOnHandlerFailureFeedsFailuresCounter(t *testing.T) {
	h := newHarness(t)
	h.emitter.OnHandlerFailure("dsp-1", "review-requested", "role-a", errors.New("boom"))
	h.emitter.OnHandlerFailure("dsp-2", "push-requested", "role-a", errors.New("boom"))

	m := findMetric(t, h.collect(t), MetricFailures)
	if got := sumFor(m, "class", FailureClassHandlerError); got != 2 {
		t.Fatalf("failures[%s] = %d, want 2 (eventID/evtType are not part of the label set)", FailureClassHandlerError, got)
	}
	// Must not also bleed into either of the other two classes.
	if got := sumFor(m, "class", FailureClassDeclined); got > 0 {
		t.Fatalf("failures[%s] = %d, want 0 (OnHandlerFailure must not touch FailureClassDeclined)", FailureClassDeclined, got)
	}
	if got := sumFor(m, "class", FailureClassDispatchFail); got > 0 {
		t.Fatalf("failures[%s] = %d, want 0 (OnHandlerFailure must not touch FailureClassDispatchFail)", FailureClassDispatchFail, got)
	}
}

// queue depth tracks enqueue/accept: it reflects the live retained set per type.
func TestQueueDepthTracksEnqueue(t *testing.T) {
	h := newHarness(t)
	h.enqueue(t, "e1", "review-requested", 10*time.Minute)
	h.enqueue(t, "e2", "review-requested", 10*time.Minute)
	rm := h.collect(t)
	if got := gaugeVal(rm, MetricQueueDepth, "type", "review-requested"); got != 2 {
		t.Fatalf("queue_depth[review-requested] = %d, want 2", got)
	}
	// Once expired AND unowed the depth returns to zero (gauge emits no datapoint -> -1).
	h.clk.advance(11 * time.Minute)
	h.q.Expire()
	rm = h.collect(t)
	if got := gaugeVal(rm, MetricQueueDepth, "type", "review-requested"); got > 0 {
		t.Fatalf("queue_depth after expiry = %d, want 0 (or absent)", got)
	}
}

// Integration through the queue: unconsumed-expired fires when an event expires
// with no accepting consumer (per-type label).
func TestUnconsumedExpiredThroughQueue(t *testing.T) {
	h := newHarness(t)
	h.enqueue(t, "orphan1", "review-requested", 5*time.Minute)
	h.clk.advance(6 * time.Minute)
	h.q.Expire()
	rm := h.collect(t)
	if got := sumFor(findMetric(t, rm, MetricUnconsumedExpired), "type", "review-requested"); got != 1 {
		t.Fatalf("unconsumed_expired[review-requested] = %d, want 1", got)
	}
}

// failure rate increments per failure class.
func TestFailureRatePerClass(t *testing.T) {
	h := newHarness(t)
	h.emitter.RecordFailure("resource-limit")
	h.emitter.RecordFailure("resource-limit")
	h.emitter.RecordFailure("critical")
	rm := h.collect(t)
	m := findMetric(t, rm, MetricFailures)
	if got := sumFor(m, "class", "resource-limit"); got != 2 {
		t.Fatalf("failures[resource-limit] = %d, want 2", got)
	}
	if got := sumFor(m, "class", "critical"); got != 1 {
		t.Fatalf("failures[critical] = %d, want 1", got)
	}
}

// The Emitter is the core's ingest observer too, so the ingest-time condition
// INV-DISP-3 requires in metrics can actually be wired to it
// (core.Options.Observer). As of Task 4.1, internal/core's own production
// code (core.go) now imports internal/metrics (for the counter NAME
// constants statusCounters folds — Binding Decision 7), so the assertion
// can no longer live in THIS (internal) test file without creating an
// import cycle: core -> metrics (production) vs. this file -> core
// (test-only) would cycle through the single package "metrics" the test
// binary builds. It now lives in metrics_core_external_test.go instead, an
// external (package metrics_test) test file — the cycle only existed
// because an INTERNAL test file and core.go would otherwise both need to
// resolve inside the same "metrics" package compilation; an external test
// package is a separate compilation unit that may import both metrics and
// core without looping back.

// The Emitter is discover's pull-source-failure observer too (Task 3.3,
// register gap R21 / bead pg2-00jpn) — moved to metrics_core_external_test.go
// alongside the core.IngestObserver assertion above: discover.go itself
// imports internal/core, so this internal test file importing discover
// would ALSO now cycle back through core -> metrics (Task 4.1) the same
// way the core.IngestObserver assertion did.

// unknown-type-rejected increments per event type: the metric half of INV-DISP-3's
// "the condition is recorded to logs and metrics".
func TestUnknownTypeRejectedPerType(t *testing.T) {
	h := newHarness(t)
	h.emitter.OnUnknownTypeRejected("review-abandoned")
	h.emitter.OnUnknownTypeRejected("review-abandoned")
	h.emitter.OnUnknownTypeRejected("never-declared")

	m := findMetric(t, h.collect(t), MetricUnknownTypeRejected)
	if _, ok := m.Data.(metricdata.Sum[int64]); !ok {
		t.Fatalf("unknown_type_rejected is not a counter: %T", m.Data)
	}
	if m.Unit != "{event}" {
		t.Fatalf("unknown_type_rejected unit = %q, want {event}", m.Unit)
	}
	if got := sumFor(m, "type", "review-abandoned"); got != 2 {
		t.Fatalf("unknown_type_rejected[review-abandoned] = %d, want 2", got)
	}
	if got := sumFor(m, "type", "never-declared"); got != 1 {
		t.Fatalf("unknown_type_rejected[never-declared] = %d, want 1", got)
	}
}

// OnEnqueue/OnAccept are safe to call even with no matching prior state (a
// zero-value Event, or an eventID OnEnqueue never saw).
func TestNoopHooks(t *testing.T) {
	h := newHarness(t)
	h.emitter.OnEnqueue(eventqueue.Event{})
	h.emitter.OnAccept("e", "l")
}

// Integration through the queue: a real accept in Dispatch reaches
// RecordThroughput via OnAccept, fed with the type OnEnqueue recorded for
// this eventID — proving the production call site (activityObserver's own
// pattern, mirrored here), not just RecordThroughput in isolation.
func TestThroughputThroughQueue_RecordsOnAccept(t *testing.T) {
	h := newHarness(t)
	h.q.Register(acceptingListener{typ: "review-requested"})
	h.enqueue(t, "e1", "review-requested", 10*time.Minute)

	h.q.Dispatch()

	m := findMetric(t, h.collect(t), MetricThroughput)
	if got := sumFor(m, "type", "review-requested"); got != 1 {
		t.Fatalf("throughput[review-requested] = %d, want 1 after one Dispatch-path accept", got)
	}
	if got := sumForBoth(m, "type", "review-requested", "role", "accepting"); got != 1 {
		t.Fatalf("throughput[review-requested,role=accepting] = %d, want 1 (OnAccept's listener id)", got)
	}
}

// Fan-out (Register's own doc: "fan-out delivers a matching event to each
// bound listener") means OnAccept can fire more than once for the SAME
// eventID in one Dispatch pass — once per accepting listener. Each accept
// must be counted: throughput mirrors the queue's own per-listener
// q.delivered tally, not a per-event one.
func TestThroughputThroughQueue_FanOutRecordsEachListenerAccept(t *testing.T) {
	h := newHarness(t)
	h.q.Register(namedAcceptingListener{id: "l1", typ: "review-requested"})
	h.q.Register(namedAcceptingListener{id: "l2", typ: "review-requested"})
	h.enqueue(t, "e1", "review-requested", 10*time.Minute)

	h.q.Dispatch()

	m := findMetric(t, h.collect(t), MetricThroughput)
	if got := sumFor(m, "type", "review-requested"); got != 2 {
		t.Fatalf("throughput[review-requested] = %d, want 2 (one per accepting listener)", got)
	}
}

// A re-emitted event id after its predecessor's retention has ended
// (eventqueue.Queue.Enqueue's documented stale-retire path, INV-EVT-3) is a
// FRESH event, not a continuation — OnEnqueue must refresh the correlation
// entry, not leave the original's stale type/time in place.
func TestOnEnqueue_StaleIDReuse_RefreshesTypeAndTime(t *testing.T) {
	h := newHarness(t)
	h.enqueue(t, "dup1", "type-A", 5*time.Minute)
	h.clk.advance(6 * time.Minute)
	h.q.Expire() // type-A's "dup1" retention ends; never accepted

	h.clk.advance(1 * time.Hour)
	h.q.Register(acceptingListener{typ: "type-B"})
	h.enqueue(t, "dup1", "type-B", 10*time.Minute) // legitimate same-id re-emit
	h.clk.advance(50 * time.Millisecond)

	h.q.Dispatch()

	m := findMetric(t, h.collect(t), MetricThroughput)
	if got := sumFor(m, "type", "type-B"); got != 1 {
		t.Fatalf("throughput[type-B] = %d, want 1 (the fresh re-emit's own type)", got)
	}
	if got := sumFor(m, "type", "type-A"); got != -1 {
		t.Fatalf("throughput[type-A] = %d, want none recorded (the stale type must not leak through)", got)
	}

	lat := findMetric(t, h.collect(t), MetricDispatchLatency)
	hist, ok := lat.Data.(metricdata.Histogram[float64])
	if !ok || len(hist.DataPoints) != 1 {
		t.Fatalf("dispatch_latency = %+v, want exactly one recorded datapoint", lat.Data)
	}
	if hist.DataPoints[0].Sum > 1000 {
		t.Fatalf("dispatch_latency sum = %v ms, want ~50 (fresh), not the stale hour-old enqueue time", hist.DataPoints[0].Sum)
	}
}

// Integration through the queue: a real accept in Dispatch reaches
// RecordDispatchLatency via OnAccept, measuring elapsed time since OnEnqueue
// against the injected clock, labeled outcome="accepted" — the one outcome
// OnAccept can observe (see RecordDispatchLatency's own doc for why other
// outcomes are deferred).
func TestDispatchLatencyThroughQueue_RecordsElapsedSinceEnqueue(t *testing.T) {
	h := newHarness(t)
	h.q.Register(acceptingListener{typ: "review-requested"})
	h.enqueue(t, "e1", "review-requested", 10*time.Minute)
	h.clk.advance(250 * time.Millisecond)

	h.q.Dispatch()

	m := findMetric(t, h.collect(t), MetricDispatchLatency)
	hist, ok := m.Data.(metricdata.Histogram[float64])
	if !ok || len(hist.DataPoints) != 1 {
		t.Fatalf("dispatch_latency = %+v, want exactly one recorded datapoint", m.Data)
	}
	dp := hist.DataPoints[0]
	if v, present := dp.Attributes.Value(attribute.Key("outcome")); !present || v.AsString() != "accepted" {
		t.Fatalf("dispatch_latency outcome label = %+v, want \"accepted\"", dp.Attributes)
	}
	if v, present := dp.Attributes.Value(attribute.Key("role")); !present || v.AsString() != "accepting" {
		t.Fatalf("dispatch_latency role label = %+v, want \"accepting\" (OnAccept's listener id)", dp.Attributes)
	}
	if v, present := dp.Attributes.Value(attribute.Key("type")); !present || v.AsString() != "review-requested" {
		t.Fatalf("dispatch_latency type label = %+v, want \"review-requested\" (OnEnqueue's event type, reduced to its entity type)", dp.Attributes)
	}
	if dp.Sum != 250 {
		t.Fatalf("dispatch_latency sum = %v ms, want 250 (elapsed since OnEnqueue)", dp.Sum)
	}
}

// OnAccept for an eventID OnEnqueue never recorded (e.g. one the FIFO cap
// already evicted) MUST NOT panic and MUST NOT record a throughput/latency
// sample — the same tolerated imperfection activityObserver's own doc accepts
// for its identical map (an evicted correlation is simply lost, not guessed).
func TestOnAccept_UnknownEventID_NoPanicNoRecord(t *testing.T) {
	h := newHarness(t)
	h.emitter.OnAccept("never-enqueued", "some-listener")

	rm := h.collect(t)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == MetricThroughput || m.Name == MetricDispatchLatency {
				t.Fatalf("%s recorded from an unknown eventID's accept, want nothing recorded", m.Name)
			}
		}
	}
}

// OnEnqueue's correlation map evicts the OLDEST entry once at
// dispatchPendingCap, FIFO — proving the real cap (not merely the tolerance
// for a missing entry TestOnAccept_UnknownEventID_NoPanicNoRecord covers).
func TestOnEnqueue_FIFOCapEviction_OldestDropped(t *testing.T) {
	h := newHarness(t)
	for i := range dispatchPendingCap + 1 {
		h.emitter.OnEnqueue(eventqueue.Event{ID: fmt.Sprintf("e%d", i), Type: "review-requested", At: h.clk.now()})
	}

	// The very first entry (e0) was evicted to make room for the (cap+1)th;
	// its accept must be silently dropped.
	h.emitter.OnAccept("e0", "listener")
	rm := h.collect(t)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == MetricThroughput {
				t.Fatalf("throughput recorded for e0, want it evicted by the FIFO cap")
			}
		}
	}

	// The most recently enqueued entry must have survived.
	h.emitter.OnAccept(fmt.Sprintf("e%d", dispatchPendingCap), "listener")
	m := findMetric(t, h.collect(t), MetricThroughput)
	if got := sumFor(m, "type", "review-requested"); got != 1 {
		t.Fatalf("throughput[review-requested] = %d, want 1 for the surviving most-recent entry", got)
	}
}

// Restart (bead pg2-0efop): an event restored from the durable queue by a
// fresh process must still count toward throughput and dispatch latency when
// it is accepted. Before the fix replay() restored the entry without telling
// the Emitter, so OnAccept silently skipped it and pg_router_throughput_total
// read zero for the whole restored backlog (the 2026-09-30 pr.changed stall
// false-positive). Latency is measured from the ORIGINAL enqueue instant the
// durable record carries, across the restart.
func TestRestartRestoredEventsCountTowardThroughputAndLatency(t *testing.T) {
	mem := eventqueue.NewMemStore()
	t0 := time.Date(2026, 9, 30, 14, 0, 0, 0, time.UTC)
	clk1 := &mockClock{t: t0}

	// Process 1: enqueue two events, accept nothing, then "crash".
	q1, err := eventqueue.New(mem, eventqueue.WithClock(clk1.now))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"e1", "e2"} {
		evt := eventqueue.Event{ID: id, Type: "pr.changed", ExpiresAt: t0.Add(time.Hour)}
		if _, err := q1.Enqueue(evt); err != nil {
			t.Fatal(err)
		}
	}

	// Process 2: brand-new Emitter (empty pending map) over the same store.
	clk2 := &mockClock{t: t0.Add(90 * time.Second)}
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	var q2 *eventqueue.Queue
	emitter, err := New(mp, func() map[string]int { return q2.DepthByType() }, WithClock(clk2.now))
	if err != nil {
		t.Fatal(err)
	}
	q2, err = eventqueue.New(mem, eventqueue.WithClock(clk2.now), eventqueue.WithObserver(emitter))
	if err != nil {
		t.Fatal(err)
	}
	q2.Register(acceptingListener{typ: "pr.changed"})
	for q2.Dispatch() > 0 { // one head event per listener per pass
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	if got := sumFor(findMetric(t, rm, MetricThroughput), "type", "pr.changed"); got != 2 {
		t.Fatalf("throughput[pr.changed] = %d, want 2 (both restored events accepted)", got)
	}
	hist, ok := findMetric(t, rm, MetricDispatchLatency).Data.(metricdata.Histogram[float64])
	if !ok || len(hist.DataPoints) != 1 {
		t.Fatalf("dispatch_latency = %+v, want one datapoint series", hist)
	}
	dp := hist.DataPoints[0]
	if dp.Count != 2 || dp.Sum != 2*90_000 {
		t.Fatalf("dispatch_latency count=%d sum=%vms, want 2 samples of 90000ms each (from the original enqueue instant)", dp.Count, dp.Sum)
	}
}

// OnRestore only seeds the OnAccept correlation: it must not emit any metric
// by itself (the restored event was already reported by the process that
// enqueued it).
func TestOnRestore_SeedsCorrelationWithoutEmitting(t *testing.T) {
	h := newHarness(t)
	h.emitter.OnRestore(eventqueue.Event{ID: "r1", Type: "pr.changed", At: h.clk.now()})

	for _, sm := range h.collect(t).ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == MetricThroughput || m.Name == MetricDispatchLatency {
				t.Fatalf("%s emitted by OnRestore alone, want nothing until an accept", m.Name)
			}
		}
	}
	h.emitter.OnAccept("r1", "l")
	if got := sumFor(findMetric(t, h.collect(t), MetricThroughput), "type", "pr.changed"); got != 1 {
		t.Fatalf("throughput[pr.changed] = %d, want 1 after the restored event's accept", got)
	}
}

// OnDeclined — the queue's pre-accept-decline / dispatch-failure signal
// (eventqueue.Observer, INV-FAIL-1) — feeds the SAME pg_router_failures
// counter RecordFailure does, labeled with the one class knowable at that
// call site (FailureClassDeclined) PLUS, as of bead pg2-j4uwg, a second
// "reason" label carrying reason verbatim. evtType/listenerID are still not
// part of the label set; reason itself widened from "wired through, no
// consumer read it" (Task 2.3's own framing of this as deferred, not
// forbidden, growth) to a real, additive metrics-catalog dimension — a
// decline recorded with a DIFFERENT reason now lands on its OWN datapoint
// rather than collapsing into one undifferentiated "declined" bucket.
func TestOnDeclinedFeedsFailuresCounter(t *testing.T) {
	h := newHarness(t)
	h.emitter.OnDeclined("review-requested", "h1", "busy")
	h.emitter.OnDeclined("review-requested", "h1", "busy")
	h.emitter.OnDeclined("push-requested", "h2", "unavailable")

	m := findMetric(t, h.collect(t), MetricFailures)
	if got := sumForBoth(m, "class", FailureClassDeclined, "reason", "busy"); got != 2 {
		t.Fatalf("failures[declined,reason=busy] = %d, want 2", got)
	}
	if got := sumForBoth(m, "class", FailureClassDeclined, "reason", "unavailable"); got != 1 {
		t.Fatalf("failures[declined,reason=unavailable] = %d, want 1", got)
	}
	// Role (bead pg2-nimab): OnDeclined's listenerID is the role name.
	if got := sumForBoth(m, "class", FailureClassDeclined, "role", "h1"); got != 2 {
		t.Fatalf("failures[declined,role=h1] = %d, want 2", got)
	}
	if got := sumForBoth(m, "class", FailureClassDeclined, "role", "h2"); got != 1 {
		t.Fatalf("failures[declined,role=h2] = %d, want 1", got)
	}
}

// OnDeduped is Task 2.3's new eventqueue.Observer method. Task 3.3 (landed
// independently, ahead of this task on main) already promoted MetricDeduped
// to a real OTel catalog member fed from core.IngestObserver's OnDeduped —
// the SAME Emitter method eventqueue.Observer's OnDeduped now also satisfies
// (both interfaces name an identical `OnDeduped(evtType string)`), so this
// proves it increments that real counter rather than a second, redundant
// in-process one.
func TestEmitter_OnDedupedIncrementsCounter(t *testing.T) {
	h := newHarness(t)
	h.emitter.OnDeduped("review-requested")
	h.emitter.OnDeduped("review-requested")

	m := findMetric(t, h.collect(t), MetricDeduped)
	if got := sumFor(m, "type", "review-requested"); got != 2 {
		t.Fatalf("deduped[%s] = %d, want 2", "review-requested", got)
	}
}

// Integration through the queue: a real pre-accept decline in Dispatch reaches
// the failures counter via OnDeclined — proving the production call site, not
// just the method in isolation.
func TestDeclineThroughQueueFeedsFailuresCounter(t *testing.T) {
	h := newHarness(t)
	l := &decliningListener{typ: "review-requested"}
	h.q.Register(l)
	h.enqueue(t, "e1", "review-requested", 10*time.Minute)

	h.q.Dispatch() // pre-accept decline: l always declines

	m := findMetric(t, h.collect(t), MetricFailures)
	if got := sumFor(m, "class", FailureClassDeclined); got != 1 {
		t.Fatalf("failures[%s] = %d, want 1 after one Dispatch-path decline", FailureClassDeclined, got)
	}
	if got := sumForBoth(m, "class", FailureClassDeclined, "role", "declining"); got != 1 {
		t.Fatalf("failures[%s,role=declining] = %d, want 1 (the listener id rides through the queue)", FailureClassDeclined, got)
	}
}

// OnDispatchFailure — the queue's OTHER delivery-side failure signal
// (eventqueue.Observer, INV-OBS-1) — feeds the SAME pg_router_failures counter
// RecordFailure does, labeled with FailureClassDispatchFail.
func TestOnDispatchFailureFeedsFailuresCounter(t *testing.T) {
	h := newHarness(t)
	h.emitter.OnDispatchFailure("review-requested", "worker-a")
	h.emitter.OnDispatchFailure("review-requested", "worker-a")
	h.emitter.OnDispatchFailure("push-requested", "worker-b")

	m := findMetric(t, h.collect(t), MetricFailures)
	if got := sumFor(m, "class", FailureClassDispatchFail); got != 3 {
		t.Fatalf("failures[%s] = %d, want 3 (evtType is not part of the label set)", FailureClassDispatchFail, got)
	}
	// Role (bead pg2-nimab): OnDispatchFailure's listenerID is the role name.
	if got := sumForBoth(m, "class", FailureClassDispatchFail, "role", "worker-a"); got != 2 {
		t.Fatalf("failures[%s,role=worker-a] = %d, want 2", FailureClassDispatchFail, got)
	}
	if got := sumForBoth(m, "class", FailureClassDispatchFail, "role", "worker-b"); got != 1 {
		t.Fatalf("failures[%s,role=worker-b] = %d, want 1", FailureClassDispatchFail, got)
	}
}

// Integration through the queue: a real dispatch failure (a listener's Offer
// panicking, recovered by eventqueue.Queue's offerSafely) reaches the
// failures counter via OnDispatchFailure — proving the production call site
// bead pg2-icm3u adds, not just the method in isolation, and proving it lands
// under the OTHER class from a graceful decline (TestDeclineThroughQueue...
// above).
func TestDispatchFailureThroughQueueFeedsFailuresCounter(t *testing.T) {
	h := newHarness(t)
	l := &panickingListener{typ: "review-requested"}
	h.q.Register(l)
	h.enqueue(t, "e1", "review-requested", 10*time.Minute)

	h.q.Dispatch() // Offer panics; offerSafely recovers it as a dispatch failure

	m := findMetric(t, h.collect(t), MetricFailures)
	if got := sumFor(m, "class", FailureClassDispatchFail); got != 1 {
		t.Fatalf("failures[%s] = %d, want 1 after one Dispatch-path panic", FailureClassDispatchFail, got)
	}
	if got := sumForBoth(m, "class", FailureClassDispatchFail, "role", "panicking"); got != 1 {
		t.Fatalf("failures[%s,role=panicking] = %d, want 1 (the listener id rides through fanOut)", FailureClassDispatchFail, got)
	}
	if got := sumFor(m, "class", FailureClassDeclined); got != -1 {
		t.Fatalf("failures[%s] = %d, want none recorded — a panic is not a graceful decline", FailureClassDeclined, got)
	}
}

// decliningListener is a minimal eventqueue.Listener that always declines
// (pre-accept, busy) events of its bound type — enough to drive Dispatch's
// Offer()==false branch without pulling in eventqueue's own test doubles
// (unexported to that package).
type decliningListener struct{ typ string }

func (decliningListener) ID() string { return "declining" }
func (l decliningListener) Matches(e eventqueue.Event) bool {
	return e.Type == l.typ
}

func (decliningListener) Offer(eventqueue.Offering) eventqueue.OfferResult {
	return eventqueue.OfferResult{Accepted: false, Decline: eventqueue.DeclineBusy}
}

// acceptingListener is a minimal eventqueue.Listener that always ACCEPTS
// events of its bound type — a real accept path (as opposed to
// decliningListener's decline) for exercising DepthByType/Flush together.
type acceptingListener struct{ typ string }

func (acceptingListener) ID() string { return "accepting" }
func (l acceptingListener) Matches(e eventqueue.Event) bool {
	return e.Type == l.typ
}

func (acceptingListener) Offer(eventqueue.Offering) eventqueue.OfferResult {
	return eventqueue.OfferResult{Accepted: true, Decline: eventqueue.DeclineNone}
}

// namedAcceptingListener is acceptingListener with a settable ID, so a test
// can register more than one accepting listener bound to the same type
// without their queue-internal per-listener state (q.inFlight, keyed by ID)
// colliding — acceptingListener's own ID() is a fixed constant, unsuitable
// for a genuine fan-out test.
type namedAcceptingListener struct{ id, typ string }

func (l namedAcceptingListener) ID() string { return l.id }
func (l namedAcceptingListener) Matches(e eventqueue.Event) bool {
	return e.Type == l.typ
}

func (namedAcceptingListener) Offer(eventqueue.Offering) eventqueue.OfferResult {
	return eventqueue.OfferResult{Accepted: true, Decline: eventqueue.DeclineNone}
}

// panickingListener is a minimal eventqueue.Listener whose Offer always
// PANICS for events of its bound type — bead pg2-icm3u's dispatch-failure
// path: eventqueue.Queue's offerSafely recovers the panic and Dispatch
// reports it via OnDispatchFailure rather than OnDeclined (decliningListener
// above stays the graceful-decline test double).
type panickingListener struct{ typ string }

func (panickingListener) ID() string { return "panicking" }
func (l panickingListener) Matches(e eventqueue.Event) bool {
	return e.Type == l.typ
}

func (panickingListener) Offer(eventqueue.Offering) eventqueue.OfferResult {
	panic("panickingListener: simulated Offer panic (dispatch failure)")
}

// New surfaces an instrument-registration error rather than panicking.
func TestNewInstrumentError(t *testing.T) {
	if _, err := New(failMP{}, func() map[string]int { return nil }); err == nil {
		t.Fatal("expected instrument-registration error")
	}
}

// failMP is a MeterProvider whose meter fails to create a counter — exercises
// New's error path. It embeds the noop bases to satisfy OTel's sealed
// interfaces, overriding only what must fail.
type failMP struct{ noop.MeterProvider }

func (failMP) Meter(string, ...metric.MeterOption) metric.Meter { return failMeter{} }

type failMeter struct{ noop.Meter }

func (failMeter) Int64Counter(string, ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	return nil, errors.New("instrument boom")
}

// Flush against the no-op MeterProvider default (INV-OBS-1: core stays
// unaware of any concrete backend) must be a safe no-op, never an error — the
// no-op provider implements no ForceFlush at all.
func TestFlushNoopProviderIsSafe(t *testing.T) {
	if err := Flush(context.Background(), noop.NewMeterProvider()); err != nil {
		t.Fatalf("Flush(noop provider) = %v, want nil", err)
	}
}

// Flush against a REAL MeterProvider forces its reader to collect immediately
// rather than waiting for a periodic tick — the acceptance criterion this
// helper exists for: "scrape after a short run reports non-empty depth."
// sdkmetric.NewManualReader has no periodic tick of its own, so this proves
// ForceFlush is actually invoked (a stub Flush that silently did nothing would
// still pass a bare "Collect works" test, but this drives it through the SAME
// harness/queue path run-until-idle uses, immediately before the collect that
// stands in for the scrape).
func TestFlushRealProviderReportsNonEmptyDepth(t *testing.T) {
	h := newHarness(t)
	h.q.Register(acceptingListener{typ: "review-requested"})
	h.enqueue(t, "e1", "review-requested", 10*time.Minute)
	h.q.Dispatch() // accepted; still RETAINED (and so still counted) until it expires+sweeps

	if err := Flush(context.Background(), h.mp); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	rm := h.collect(t)
	if got := gaugeVal(rm, MetricQueueDepth, "type", "review-requested"); got != 1 {
		t.Fatalf("queue_depth[review-requested] after Flush = %d, want 1 (non-empty depth reported)", got)
	}
}

// NewReadableProvider's MeterProvider must actually back live instruments —
// an Emitter constructed on it and driven records a value the paired
// Reader's Snapshot can read back (Task 3.6-prereq's value-read-back
// acceptance criterion). Unlike newHarness's own reader (which the TEST
// constructs and owns directly), this proves the PRODUCTION constructor
// bootCore is expected to call wires the two together correctly on its own.
func TestNewReadableProvider_SnapshotReadsBackRecordedValue(t *testing.T) {
	mp, reader := NewReadableProvider()
	emitter, err := New(mp, func() map[string]int { return nil })
	if err != nil {
		t.Fatalf("New emitter: %v", err)
	}

	emitter.OnUnconsumedExpired("review-requested")

	rm, err := reader.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	m := findMetric(t, rm, MetricUnconsumedExpired)
	if got := sumFor(m, "type", "review-requested"); got != 1 {
		t.Fatalf("%s{type=review-requested} = %d, want 1", MetricUnconsumedExpired, got)
	}
}

// A Reader with nothing recorded yet must still Snapshot cleanly (no
// instruments driven at all is not an error condition — the same "absence
// means zero" posture gaugeVal's own doc already states for an observable
// gauge).
func TestNewReadableProvider_SnapshotBeforeAnyRecordingIsNotAnError(t *testing.T) {
	mp, reader := NewReadableProvider()
	if _, err := New(mp, func() map[string]int { return nil }); err != nil {
		t.Fatalf("New emitter: %v", err)
	}

	if _, err := reader.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot before any recording = %v, want nil", err)
	}
}

// --- helpers --------------------------------------------------------------

// enqueue appends an event with an explicit absolute `expiresAt`, computed off the
// harness's mock clock. Expiry is an INSTANT and never a duration (DEC-EVENT-1),
// so `expiresIn` is a test-side convenience for picking that instant, not a field
// the event carries.
func (h *harness) enqueue(t *testing.T, id, typ string, expiresIn time.Duration) {
	t.Helper()
	evt := eventqueue.Event{ID: id, Type: typ, ExpiresAt: h.clk.now().Add(expiresIn)}
	if _, err := h.q.Enqueue(evt); err != nil {
		t.Fatalf("enqueue %s: %v", id, err)
	}
}

// pg2-u2yub: a triage role's handler failures are bulkheaded under
// reason=triager-failure (even a budget stop), and a triager failure does not
// move the worker/review series: the residual failure-rate selector
// (reason!~"at-capacity|budget-exceeded|origin-unavailable|triager-failure")
// matches only the non-triager series.
func TestOnHandlerFailure_TriagerFailuresAreBulkheaded(t *testing.T) {
	h := newHarness(t)
	notDone := errors.New(`wireclient: role "alpha-escalation-triager" exited 1: session exited before completing`)
	budgetErr := errors.New(`wireclient: role "beta-escalation-triager" exited 1: session budget exceeded: role=beta-escalation-triager limit=time`)
	h.emitter.OnHandlerFailure("d1", "escalated.alpha", "alpha-escalation-triager", notDone)
	h.emitter.OnHandlerFailure("d2", "escalated.alpha", "alpha-escalation-triager", notDone)
	h.emitter.OnHandlerFailure("d3", "escalated.beta", "beta-escalation-triager", budgetErr)
	h.emitter.OnHandlerFailure("d4", "work-ready", "worker", errors.New("boom"))

	m := findMetric(t, h.collect(t), MetricFailures)
	s := m.Data.(metricdata.Sum[int64])
	got := map[string]int64{}
	residual := int64(0)
	for _, dp := range s.DataPoints {
		role, _ := dp.Attributes.Value("role")
		reason, _ := dp.Attributes.Value("reason")
		got[role.AsString()+"|"+reason.AsString()] += dp.Value
		switch reason.AsString() {
		case "at-capacity", ReasonBudgetExceeded, "origin-unavailable", ReasonTriagerFailure, ReasonUpstreamKilled:
		default:
			residual += dp.Value
		}
	}
	want := map[string]int64{
		"alpha-escalation-triager|triager-failure": 2,
		"beta-escalation-triager|triager-failure":  1,
		"worker|": 1,
	}
	if len(got) != len(want) {
		t.Fatalf("series = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("series %q = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
	if residual != 1 {
		t.Errorf("residual (failure-rate) series total = %d, want 1 (only the worker failure; triager failures must not move it)", residual)
	}
}

func TestIsTriagerRole(t *testing.T) {
	for role, want := range map[string]bool{
		"alpha-escalation-triager": true,
		"beta-escalation-triager":  true,
		"split-triage":             true,
		"worker":                   false,
		"review":                   false,
		"":                         false,
	} {
		if got := IsTriagerRole(role); got != want {
			t.Errorf("IsTriagerRole(%q) = %v, want %v", role, got, want)
		}
	}
}

// pg2-irowq: a handler error containing the documented sentinel "session
// budget exceeded" maps to reason=budget-exceeded, and every handler-error
// carries the (config-bounded) role label. Other handler errors carry no
// reason, so alert residual matchers keep covering them.
func TestOnHandlerFailure_BudgetSentinelMapsToReasonAndRoleLabel(t *testing.T) {
	h := newHarness(t)
	budgetErr := fmt.Errorf(`wireclient: role "worker" exited 1: session budget exceeded: role=worker pool=p bead=zr-1 session=s limit=time used=1500 cap=1500 elapsed=1500s`)
	h.emitter.OnHandlerFailure("dsp-1", "work-ready", "worker", budgetErr)
	h.emitter.OnHandlerFailure("dsp-2", "work-ready", "worker", budgetErr)
	h.emitter.OnHandlerFailure("dsp-3", "review-ready", "review", errors.New(`wireclient: role "review" exited 1: session exited before completing`))

	m := findMetric(t, h.collect(t), MetricFailures)
	s := m.Data.(metricdata.Sum[int64])
	got := map[string]int64{}
	for _, dp := range s.DataPoints {
		cls, _ := dp.Attributes.Value("class")
		role, _ := dp.Attributes.Value("role")
		reason, _ := dp.Attributes.Value("reason")
		got[cls.AsString()+"|"+role.AsString()+"|"+reason.AsString()] += dp.Value
	}
	want := map[string]int64{
		"handler-error|worker|budget-exceeded": 2,
		"handler-error|review|":                1,
	}
	if len(got) != len(want) {
		t.Fatalf("series = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("series %q = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
}

// handlerFailureSeries records one OnHandlerFailure per (role, err) pair and
// returns the resulting handler-error counts keyed "role|reason".
func handlerFailureSeries(t *testing.T, h *harness, failures []struct {
	role string
	err  error
},
) map[string]int64 {
	t.Helper()
	for i, f := range failures {
		h.emitter.OnHandlerFailure(fmt.Sprintf("dsp-%d", i), "pr.reconcile", f.role, f.err)
	}
	s := findMetric(t, h.collect(t), MetricFailures).Data.(metricdata.Sum[int64])
	got := map[string]int64{}
	for _, dp := range s.DataPoints {
		if cls, _ := dp.Attributes.Value("class"); cls.AsString() != FailureClassHandlerError {
			t.Fatalf("unexpected class %q", cls.AsString())
		}
		role, _ := dp.Attributes.Value("role")
		reason, _ := dp.Attributes.Value("reason")
		got[role.AsString()+"|"+reason.AsString()] += dp.Value
	}
	return got
}

// pg2-fy2pm: a handler error whose text carries BOTH "scriptout:" and
// "signal: killed" (the pg-connector 30s exec-timeout kill of a slow gh call)
// maps to reason=upstream-killed with the config-bounded role label.
func TestOnHandlerFailure_UpstreamKilledSentinel(t *testing.T) {
	h := newHarness(t)
	got := handlerFailureSeries(t, h, []struct {
		role string
		err  error
	}{
		{"desk-pr", errors.New(`exit 1: unavailable: scriptout: pg-connector-pr-github: signal: killed`)},
		{"desk-pr", fmt.Errorf(`wireclient: role "desk-pr" exited 1: %w`, errors.New(`unavailable: scriptout: pg-connector-pr-github: signal: killed`))},
	})
	want := map[string]int64{"desk-pr|upstream-killed": 2}
	if len(got) != len(want) || got["desk-pr|upstream-killed"] != 2 {
		t.Fatalf("series = %v, want %v", got, want)
	}
}

// pg2-fy2pm: bare "signal: killed" (an OOM/SIGKILL of any worker or ccpool
// session) and a bare "scriptout:" error MUST NOT be tagged upstream-killed:
// they keep paging pg-router-failure-rate individually.
func TestOnHandlerFailure_UpstreamKilledRequiresBothSubstrings(t *testing.T) {
	h := newHarness(t)
	got := handlerFailureSeries(t, h, []struct {
		role string
		err  error
	}{
		{"worker", errors.New(`wireclient: role "worker" exited -1: signal: killed`)},
		{"desk-pr", errors.New(`exit 1: unavailable: scriptout: pg-connector-pr-github: error connecting to api.github.com`)},
		{"desk-pr", nil},
	})
	want := map[string]int64{"worker|": 1, "desk-pr|": 2}
	if len(got) != len(want) {
		t.Fatalf("series = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("series %q = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
}

// pg2-fy2pm precedence: triager-failure wins over upstream-killed, and
// budget-exceeded wins over upstream-killed (triager > budget > upstream-killed).
func TestOnHandlerFailure_UpstreamKilledPrecedence(t *testing.T) {
	h := newHarness(t)
	killed := `scriptout: pg-connector-pr-github: signal: killed`
	got := handlerFailureSeries(t, h, []struct {
		role string
		err  error
	}{
		{"alpha-escalation-triager", errors.New(`exit 1: unavailable: ` + killed)},
		{"worker", errors.New(`exit 1: session budget exceeded: role=worker limit=time; ` + killed)},
		{"desk-pr", errors.New(`exit 1: unavailable: ` + killed)},
	})
	want := map[string]int64{
		"alpha-escalation-triager|triager-failure": 1,
		"worker|budget-exceeded":                   1,
		"desk-pr|upstream-killed":                  1,
	}
	if len(got) != len(want) {
		t.Fatalf("series = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("series %q = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
}

// TestSilentCountersExportZeroSeriesFromStartup (pg2-9q3pq): a counter never
// Add()ed exports no series, so deduped/unknown_type_rejected/enqueue_rejected
// are pre-recorded at 0 and exist from the first collect, before any event.
func TestSilentCountersExportZeroSeriesFromStartup(t *testing.T) {
	h := newHarness(t)
	rm := h.collect(t)
	for _, name := range []string{MetricDeduped, MetricUnknownTypeRejected, MetricEnqueueRejected} {
		m := findMetric(t, rm, name)
		s, ok := m.Data.(metricdata.Sum[int64])
		if !ok || len(s.DataPoints) == 0 {
			t.Fatalf("%s: no series at startup (data=%T)", name, m.Data)
		}
		for _, dp := range s.DataPoints {
			if dp.Value != 0 {
				t.Errorf("%s: startup datapoint = %d, want 0", name, dp.Value)
			}
		}
	}
	// Pre-recording must not disturb real counts.
	h.emitter.OnDeduped("t")
	if got := sumFor(findMetric(t, h.collect(t), MetricDeduped), "type", "t"); got != 1 {
		t.Errorf("deduped[t] = %d after one event, want 1", got)
	}
}
