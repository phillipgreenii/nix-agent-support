package telemetry

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

var changeT0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

type changeFixture struct {
	t      *testing.T
	reader *sdkmetric.ManualReader
	mp     *sdkmetric.MeterProvider
	now    time.Time
}

func newChangeFixture(t *testing.T) *changeFixture {
	t.Helper()
	f := &changeFixture{t: t, reader: sdkmetric.NewManualReader(), now: changeT0}
	f.mp = sdkmetric.NewMeterProvider(sdkmetric.WithReader(f.reader))
	t.Cleanup(func() { _ = f.mp.Shutdown(context.Background()) })
	return f
}

// build constructs a Telemetry on the fixture's provider; mutate sets the new
// sources before it is built.
func (f *changeFixture) build(mutate func(*Options)) *Telemetry {
	f.t.Helper()
	o := Options{
		MeterProvider:  f.mp,
		TracerProvider: tracenoop.NewTracerProvider(),
		Now:            func() time.Time { return f.now },
		Started:        changeT0,
	}
	if mutate != nil {
		mutate(&o)
	}
	tel, err := New(o)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = tel.Close() })
	return tel
}

func (f *changeFixture) collect() map[string]metricdata.Metrics {
	f.t.Helper()
	var rm metricdata.ResourceMetrics
	if err := f.reader.Collect(context.Background(), &rm); err != nil {
		f.t.Fatal(err)
	}
	out := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

func (f *changeFixture) metric(name string) metricdata.Metrics {
	f.t.Helper()
	m, ok := f.collect()[name]
	if !ok {
		f.t.Fatalf("metric %s absent", name)
	}
	return m
}

func histogramCounts(t *testing.T, m metricdata.Metrics) map[string]uint64 {
	t.Helper()
	h, ok := m.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("%s: %T is not a float histogram", m.Name, m.Data)
	}
	out := map[string]uint64{}
	for _, p := range h.DataPoints {
		out[setString(p.Attributes)] = p.Count
	}
	return out
}

func TestSettledCountsEachResultWordUnderItsOwnSeries(t *testing.T) {
	f := newChangeFixture(t)
	o := NewChangeObserver(f.build(nil))
	o.Settled(changes.ResultTimeout)
	o.Settled(changes.ResultSettled)
	o.Settled(changes.ResultSettled)
	o.Settled(changes.ResultFailed)
	o.Settled("exploded")

	got := sumByLabels(t, f.metric(MetricChangeSettle))
	want := map[string]int64{"result=timeout": 1, "result=settled": 2, "result=failed": 1, "result=unknown": 1}
	if len(got) != len(want) {
		t.Fatalf("settle series = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("change.settle{%s} = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
}

func TestSettlePendingReportsTheSourceCountAndSkipsOnError(t *testing.T) {
	f := newChangeFixture(t)
	n, fail := 3, false
	f.build(func(o *Options) {
		o.Pending = func(context.Context) (int, error) {
			if fail {
				return 0, errors.New("store closed")
			}
			return n, nil
		}
	})
	if v := gaugeInt(t, f.metric(MetricChangeSettlePending), ""); v != 3 {
		t.Errorf("settle_pending = %d, want 3", v)
	}
	n = 0
	if v := gaugeInt(t, f.metric(MetricChangeSettlePending), ""); v != 0 {
		t.Errorf("settle_pending = %d, want 0", v)
	}
	fail = true
	if _, ok := f.collect()[MetricChangeSettlePending]; ok {
		t.Error("settle_pending must be unreported while its source fails")
	}
}

func TestDetectedRecordsOnePointPerCallWithKindAndOrigin(t *testing.T) {
	f := newChangeFixture(t)
	o := NewChangeObserver(f.build(nil))
	o.Detected(classify.KindMerged, groupsvc.OriginQuery, 20*time.Second)
	o.Detected(classify.KindMerged, groupsvc.OriginQuery, 40*time.Second)
	o.Detected(classify.KindCIChanged, groupsvc.OriginRefresh, time.Minute)
	o.Detected("not-a-kind", groupsvc.OriginWrite, time.Second)
	o.Detected(classify.KindClosed, "carrier-pigeon", time.Second)
	o.Detected(classify.KindClosed, groupsvc.OriginRead, -time.Second)

	m := f.metric(MetricChangeDetectionDelay)
	got := histogramCounts(t, m)
	want := map[string]uint64{
		"kind=merged,origin=query":       2,
		"kind=ci_changed,origin=refresh": 1,
		"kind=unknown,origin=write":      1,
		"kind=closed,origin=unknown":     1,
		"kind=closed,origin=read":        1,
	}
	if len(got) != len(want) {
		t.Fatalf("detection delay series = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("detection_delay{%s} count = %d, want %d", k, got[k], v)
		}
	}
	for _, p := range m.Data.(metricdata.Histogram[float64]).DataPoints {
		if setString(p.Attributes) == "kind=merged,origin=query" && p.Sum != 60 {
			t.Errorf("merged/query sum = %v seconds, want 60", p.Sum)
		}
		if setString(p.Attributes) == "kind=closed,origin=read" && p.Sum != 0 {
			t.Errorf("a negative delay must record zero, got %v", p.Sum)
		}
	}
}

func TestEveryClassifierKindAndOriginWordIsAcceptedUnfolded(t *testing.T) {
	for _, k := range classify.Kinds() {
		if got := changeKindLabel(k); got != k {
			t.Errorf("kind %q folded to %q", k, got)
		}
	}
	for _, w := range []string{groupsvc.OriginQuery, groupsvc.OriginRefresh, groupsvc.OriginWrite, groupsvc.OriginRead} {
		if got := changeOriginLabel(w); got != w {
			t.Errorf("origin %q folded to %q", w, got)
		}
	}
}

func fixedPositions(ps ...feed.Position) func(context.Context) ([]feed.Position, error) {
	return func(context.Context) ([]feed.Position, error) { return ps, nil }
}

func TestConsumerGaugesReportEachStoredKeyAndFoldUnconfiguredQueries(t *testing.T) {
	f := newChangeFixture(t)
	f.build(func(o *Options) {
		o.ConfiguredQueries = func() []string { return []string{"mine"} }
		o.Consumers = fixedPositions(
			feed.Position{Consumer: "desk", Kind: feed.KindPR, Query: "mine", LagRows: 4, OldestUnacked: changeT0.Add(-90 * time.Second)},
			feed.Position{Consumer: "desk", Kind: feed.KindCI, Query: "mine", LagRows: 1, OldestUnacked: changeT0.Add(-30 * time.Second)},
			feed.Position{Consumer: "router", Kind: feed.KindPR, Query: "example-owner/example-repo#41"},
			feed.Position{Consumer: "router", Kind: feed.KindCI, Query: "adhoc?assignee=someone", LagRows: 2, OldestUnacked: changeT0.Add(-10 * time.Second)},
		)
	})
	lag := f.metric(MetricConsumerLagRows)
	oldest := f.metric(MetricConsumerOldestUnacked)

	if v := gaugeInt(t, lag, "consumer=desk,query=mine"); v != 5 {
		t.Errorf("lag desk/mine = %d, want 5 (4+1)", v)
	}
	if v := gaugeInt(t, lag, "consumer=router,query=other"); v != 2 {
		t.Errorf("lag router/other = %d, want 2", v)
	}
	if v := gaugeFloat(t, oldest, "consumer=desk,query=mine"); v != 90 {
		t.Errorf("oldest desk/mine = %v, want 90", v)
	}
	if v := gaugeFloat(t, oldest, "consumer=router,query=other"); v != 10 {
		t.Errorf("oldest router/other = %v, want 10", v)
	}
	for _, m := range []metricdata.Metrics{lag, oldest} {
		sets := labelSets(t, m)
		if len(sets) != 2 {
			t.Errorf("%s series = %v, want 2", m.Name, sets)
		}
		for _, s := range sets {
			if strings.ContainsAny(s, "#/?") || strings.Contains(s, "example-") || strings.Contains(s, "adhoc") {
				t.Errorf("%s label set %q carries an entity id or an ad-hoc query key", m.Name, s)
			}
		}
	}
}

func TestOldestUnackedIsZeroWithNothingUnacknowledgedAndGrowsWithTheClock(t *testing.T) {
	f := newChangeFixture(t)
	f.build(func(o *Options) {
		o.ConfiguredQueries = func() []string { return []string{"mine"} }
		o.Consumers = fixedPositions(
			feed.Position{Consumer: "idle", Kind: feed.KindPR, Query: "mine"},
			feed.Position{Consumer: "behind", Kind: feed.KindPR, Query: "mine", LagRows: 1, OldestUnacked: changeT0},
		)
	})
	m := f.metric(MetricConsumerOldestUnacked)
	if v := gaugeFloat(t, m, "consumer=idle,query=mine"); v != 0 {
		t.Errorf("idle age = %v, want a series with value 0", v)
	}
	f.now = changeT0.Add(5 * time.Minute)
	if v := gaugeFloat(t, f.metric(MetricConsumerOldestUnacked), "consumer=behind,query=mine"); v != 300 {
		t.Errorf("behind age = %v, want 300", v)
	}
}

func TestRedeliveredAddsRowsOnlyUnderStoredConsumersAndConfiguredQueries(t *testing.T) {
	f := newChangeFixture(t)
	tel := f.build(func(o *Options) {
		o.ConfiguredQueries = func() []string { return []string{"mine"} }
		o.Consumers = fixedPositions(feed.Position{Consumer: "desk", Kind: feed.KindPR, Query: "mine"})
	})
	o := NewFeedObserver(tel)

	// Before any Positions poll no consumer is known to be stored.
	o.Redelivered("desk", "mine", 1)
	f.collect() // polls Positions, which records desk as stored
	o.Redelivered("desk", "mine", 3)
	o.Redelivered("desk", "mine", 2)
	o.Redelivered("desk", "example-owner/example-repo#41", 1)
	o.Redelivered("minted-by-a-request", "mine", 4)
	o.Redelivered("desk", "mine", 0)

	got := sumByLabels(t, f.metric(MetricConsumerRedeliveries))
	want := map[string]int64{
		"consumer=other,query=mine": 1 + 4, // before the first poll, and a name no key stored
		"consumer=desk,query=mine":  3 + 2,
		"consumer=desk,query=other": 1,
	}
	if len(got) != len(want) {
		t.Fatalf("redelivery series = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("redeliveries{%s} = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
}

func TestQueryLastSuccessAgeIsOneSeriesPerConfiguredQuery(t *testing.T) {
	f := newChangeFixture(t)
	f.build(func(o *Options) {
		o.ConfiguredQueries = func() []string { return []string{"fresh", "never", "fresh"} }
		o.Queries = func(context.Context) ([]store.QueryRow, error) {
			return []store.QueryRow{
				{Key: "fresh", Configured: true, LastSuccessAt: changeT0.Add(-2 * time.Minute)},
				{Key: "fresh?assignee=someone", LastSuccessAt: changeT0},
				{Key: "dropped", Configured: true, LastSuccessAt: changeT0},
			}, nil
		}
	})
	m := f.metric(MetricQueryLastSuccessAge)
	if sets := labelSets(t, m); len(sets) != 2 {
		t.Fatalf("series = %v, want one per configured query (fresh, never)", sets)
	}
	if v := gaugeFloat(t, m, "query=fresh"); v != 120 {
		t.Errorf("fresh age = %v, want 120", v)
	}
	if v := gaugeFloat(t, m, "query=never"); v != 0 {
		t.Errorf("never-succeeded age at start = %v, want 0", v)
	}

	f.now = changeT0.Add(10 * time.Minute)
	m = f.metric(MetricQueryLastSuccessAge)
	if v := gaugeFloat(t, m, "query=fresh"); v != 720 {
		t.Errorf("fresh age after 10m = %v, want 720", v)
	}
	if v := gaugeFloat(t, m, "query=never"); v != 600 {
		t.Errorf("never-succeeded age = %v, want 600 (counted from Started)", v)
	}
}

func TestQueryAgeCountsFromNewWhenStartedIsUnset(t *testing.T) {
	f := newChangeFixture(t)
	f.build(func(o *Options) {
		o.Started = time.Time{}
		o.ConfiguredQueries = func() []string { return []string{"never"} }
		o.Queries = func(context.Context) ([]store.QueryRow, error) { return nil, nil }
	})
	f.now = changeT0.Add(time.Minute)
	if v := gaugeFloat(t, f.metric(MetricQueryLastSuccessAge), "query=never"); v != 60 {
		t.Errorf("age = %v, want 60", v)
	}
}

func TestChangeGaugesAreUnreportedWhenTheirSourcesAreUnset(t *testing.T) {
	f := newChangeFixture(t)
	f.build(nil)
	got := f.collect()
	for _, name := range []string{MetricChangeSettlePending, MetricConsumerLagRows, MetricConsumerOldestUnacked, MetricQueryLastSuccessAge} {
		if _, ok := got[name]; ok {
			t.Errorf("%s reported with its source unset", name)
		}
	}
	if _, ok := got[MetricDaemonUp]; !ok {
		t.Error("daemon.up must still be reported")
	}

	// A query source without the configured list (or the reverse) reports nothing.
	f2 := newChangeFixture(t)
	f2.build(func(o *Options) {
		o.Queries = func(context.Context) ([]store.QueryRow, error) { return nil, nil }
	})
	if _, ok := f2.collect()[MetricQueryLastSuccessAge]; ok {
		t.Error("query.last_success_age_seconds reported without ConfiguredQueries")
	}
}

func TestNilTelemetryYieldsInertObservers(t *testing.T) {
	c := NewChangeObserver(nil)
	c.Detected(classify.KindMerged, groupsvc.OriginQuery, time.Second)
	c.Settled(changes.ResultSettled)
	NewFeedObserver(nil).Redelivered("desk", "mine", 3)
}

func TestChangeMetricInstrumentsHaveTheCatalogueNames(t *testing.T) {
	for got, want := range map[string]string{
		MetricChangeSettle:          "pg_connector_github.change.settle",
		MetricChangeSettlePending:   "pg_connector_github.change.settle_pending",
		MetricChangeDetectionDelay:  "pg_connector_github.change.detection_delay_seconds",
		MetricConsumerLagRows:       "pg_connector_github.consumer.lag_rows",
		MetricConsumerOldestUnacked: "pg_connector_github.consumer.oldest_unacked_age_seconds",
		MetricConsumerRedeliveries:  "pg_connector_github.consumer.redeliveries",
		MetricQueryLastSuccessAge:   "pg_connector_github.query.last_success_age_seconds",
	} {
		if got != want {
			t.Errorf("metric name %q, want %q", got, want)
		}
	}
}

func TestObserversAreSafeUnderConcurrentUse(t *testing.T) {
	f := newChangeFixture(t)
	tel := f.build(func(o *Options) {
		o.ConfiguredQueries = func() []string { return []string{"mine"} }
		o.Consumers = fixedPositions(feed.Position{Consumer: "desk", Kind: feed.KindPR, Query: "mine", LagRows: 1})
		o.Pending = func(context.Context) (int, error) { return 1, nil }
	})
	co, fo := NewChangeObserver(tel), NewFeedObserver(tel)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				co.Settled(changes.ResultSettled)
				co.Detected(classify.KindMerged, groupsvc.OriginQuery, time.Second)
				fo.Redelivered("desk", "mine", 1)
				f.collect()
			}
		}()
	}
	wg.Wait()
}
