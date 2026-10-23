package changemetrics

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

type fixture struct {
	t      *testing.T
	reader *sdkmetric.ManualReader
	mp     *sdkmetric.MeterProvider
	now    time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, reader: sdkmetric.NewManualReader(), now: t0}
	f.mp = sdkmetric.NewMeterProvider(sdkmetric.WithReader(f.reader))
	t.Cleanup(func() { _ = f.mp.Shutdown(context.Background()) })
	return f
}

func (f *fixture) build(prefix string, mutate func(*Config)) *Metrics {
	f.t.Helper()
	cfg := Config{
		Prefix:  prefix,
		Kinds:   []string{"alpha", "beta"},
		Started: t0,
		Now:     func() time.Time { return f.now },
	}
	if mutate != nil {
		mutate(&cfg)
	}
	m, err := New(f.mp.Meter("example-meter"), cfg)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = m.Close() })
	return m
}

func (f *fixture) collect() map[string]metricdata.Metrics {
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

func (f *fixture) metric(name string) metricdata.Metrics {
	f.t.Helper()
	m, ok := f.collect()[name]
	if !ok {
		f.t.Fatalf("metric %s absent", name)
	}
	return m
}

func setString(s attribute.Set) string {
	var parts []string
	for _, kv := range s.ToSlice() {
		parts = append(parts, string(kv.Key)+"="+kv.Value.AsString())
	}
	return strings.Join(parts, ",")
}

func labelKeys(s attribute.Set) string {
	var parts []string
	for _, kv := range s.ToSlice() {
		parts = append(parts, string(kv.Key))
	}
	return strings.Join(parts, ",")
}

func gaugeInts(t *testing.T, m metricdata.Metrics) map[string]int64 {
	t.Helper()
	g, ok := m.Data.(metricdata.Gauge[int64])
	if !ok {
		t.Fatalf("%s: %T is not an int gauge", m.Name, m.Data)
	}
	out := map[string]int64{}
	for _, p := range g.DataPoints {
		out[setString(p.Attributes)] = p.Value
	}
	return out
}

func gaugeFloats(t *testing.T, m metricdata.Metrics) map[string]float64 {
	t.Helper()
	g, ok := m.Data.(metricdata.Gauge[float64])
	if !ok {
		t.Fatalf("%s: %T is not a float gauge", m.Name, m.Data)
	}
	out := map[string]float64{}
	for _, p := range g.DataPoints {
		out[setString(p.Attributes)] = p.Value
	}
	return out
}

func counterSums(t *testing.T, m metricdata.Metrics) map[string]int64 {
	t.Helper()
	s, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("%s: %T is not an int sum", m.Name, m.Data)
	}
	out := map[string]int64{}
	for _, p := range s.DataPoints {
		out[setString(p.Attributes)] = p.Value
	}
	return out
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

func equalInts(got, want map[string]int64) bool {
	if len(got) != len(want) {
		return false
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

func positions(ps ...feed.Position) func(context.Context) ([]feed.Position, error) {
	return func(context.Context) ([]feed.Position, error) { return ps, nil }
}

// exerciseEveryInstrument drives all eight instruments once.
func exerciseEveryInstrument(f *fixture, prefix string) {
	m := f.build(prefix, func(c *Config) {
		c.Sources = Sources{
			Pending:           func(context.Context) (int, error) { return 2, nil },
			Consumers:         positions(feed.Position{Consumer: "example-consumer", Scope: "example-query", LagRows: 3, OldestUnacked: t0.Add(-time.Minute)}),
			Queries:           func(context.Context) ([]store.QueryRow, error) { return nil, nil },
			ConfiguredQueries: func() []string { return []string{"example-query"} },
			Watched:           func(context.Context) (int, error) { return 7, nil },
		}
	})
	m.ChangeObserver().Detected("alpha", engine.OriginQuery, time.Second)
	m.ChangeObserver().Settled(engine.ResultSettled)
	f.collect() // a poll records the consumer as stored
	m.FeedObserver().Redelivered("example-consumer", "example-query", 1)
}

// TestNamesAndLabelSetsOfTheEightInstruments is the telemetry golden: every
// name is the prefix and the fixed suffix, with the label keys the dashboards
// and alert rules read.
func TestNamesAndLabelSetsOfTheEightInstruments(t *testing.T) {
	f := newFixture(t)
	exerciseEveryInstrument(f, "example_prefix.")
	got := f.collect()

	want := map[string]string{
		"example_prefix.change.settle":                       "result",
		"example_prefix.change.settle_pending":               "",
		"example_prefix.change.detection_delay_seconds":      "kind,origin",
		"example_prefix.consumer.lag_rows":                   "consumer,query",
		"example_prefix.consumer.oldest_unacked_age_seconds": "consumer,query",
		"example_prefix.consumer.redeliveries":               "consumer,query",
		"example_prefix.query.last_success_age_seconds":      "query",
		"example_prefix.watched.entities":                    "",
	}
	if len(got) != len(want) {
		names := make([]string, 0, len(got))
		for n := range got {
			names = append(names, n)
		}
		sort.Strings(names)
		t.Fatalf("instruments = %v, want exactly %d", names, len(want))
	}
	for name, keys := range want {
		m, ok := got[name]
		if !ok {
			t.Errorf("instrument %s absent", name)
			continue
		}
		var sets []string
		switch d := m.Data.(type) {
		case metricdata.Gauge[int64]:
			for _, p := range d.DataPoints {
				sets = append(sets, labelKeys(p.Attributes))
			}
		case metricdata.Gauge[float64]:
			for _, p := range d.DataPoints {
				sets = append(sets, labelKeys(p.Attributes))
			}
		case metricdata.Sum[int64]:
			for _, p := range d.DataPoints {
				sets = append(sets, labelKeys(p.Attributes))
			}
		case metricdata.Histogram[float64]:
			for _, p := range d.DataPoints {
				sets = append(sets, labelKeys(p.Attributes))
			}
		default:
			t.Errorf("%s: unexpected data type %T", name, m.Data)
		}
		if len(sets) == 0 {
			t.Errorf("%s has no data point", name)
		}
		for _, s := range sets {
			if s != keys {
				t.Errorf("%s label keys = %q, want %q", name, s, keys)
			}
		}
	}
}

func TestEmptyPrefixMeansNoPrefix(t *testing.T) {
	f := newFixture(t)
	f.build("", func(c *Config) {
		c.Sources.Watched = func(context.Context) (int, error) { return 1, nil }
	})
	if _, ok := f.collect()["watched.entities"]; !ok {
		t.Error("watched.entities absent under an empty prefix")
	}
}

func TestTwoPrefixesOnOneMeterProviderStaySeparate(t *testing.T) {
	f := newFixture(t)
	one := f.build("example_one.", func(c *Config) {
		c.Sources.Watched = func(context.Context) (int, error) { return 1, nil }
	})
	two := f.build("example_two.", func(c *Config) {
		c.Sources.Watched = func(context.Context) (int, error) { return 2, nil }
	})
	one.ChangeObserver().Settled(engine.ResultSettled)
	two.ChangeObserver().Settled(engine.ResultFailed)
	two.ChangeObserver().Settled(engine.ResultFailed)

	if got := gaugeInts(t, f.metric("example_one.watched.entities"))[""]; got != 1 {
		t.Errorf("first prefix watched = %d, want 1", got)
	}
	if got := gaugeInts(t, f.metric("example_two.watched.entities"))[""]; got != 2 {
		t.Errorf("second prefix watched = %d, want 2", got)
	}
	if got := counterSums(t, f.metric("example_one.change.settle")); !equalInts(got, map[string]int64{"result=settled": 1}) {
		t.Errorf("first prefix settle = %v", got)
	}
	if got := counterSums(t, f.metric("example_two.change.settle")); !equalInts(got, map[string]int64{"result=failed": 2}) {
		t.Errorf("second prefix settle = %v", got)
	}
}

func TestSettledCountsEachResultWordUnderItsOwnSeries(t *testing.T) {
	f := newFixture(t)
	o := f.build("p.", nil).ChangeObserver()
	o.Settled(engine.ResultTimeout)
	o.Settled(engine.ResultSettled)
	o.Settled(engine.ResultSettled)
	o.Settled(engine.ResultFailed)
	o.Settled("exploded")

	want := map[string]int64{"result=timeout": 1, "result=settled": 2, "result=failed": 1, "result=unknown": 1}
	if got := counterSums(t, f.metric("p.change.settle")); !equalInts(got, want) {
		t.Errorf("settle series = %v, want %v", got, want)
	}
}

func TestDetectedFoldsKindsFromConfigAndTheFourOriginWords(t *testing.T) {
	f := newFixture(t)
	o := f.build("p.", nil).ChangeObserver()
	o.Detected("alpha", engine.OriginQuery, 20*time.Second)
	o.Detected("alpha", engine.OriginQuery, 40*time.Second)
	o.Detected("beta", engine.OriginRefresh, time.Minute)
	o.Detected("not-a-kind", engine.OriginWrite, time.Second)
	o.Detected("beta", "carrier-pigeon", time.Second)
	o.Detected("beta", engine.OriginRead, -time.Second)

	m := f.metric("p.change.detection_delay_seconds")
	want := map[string]uint64{
		"kind=alpha,origin=query":   2,
		"kind=beta,origin=refresh":  1,
		"kind=unknown,origin=write": 1,
		"kind=beta,origin=unknown":  1,
		"kind=beta,origin=read":     1,
	}
	got := histogramCounts(t, m)
	if len(got) != len(want) {
		t.Fatalf("detection delay series = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("detection_delay{%s} count = %d, want %d", k, got[k], v)
		}
	}
	for _, p := range m.Data.(metricdata.Histogram[float64]).DataPoints {
		if setString(p.Attributes) == "kind=alpha,origin=query" && p.Sum != 60 {
			t.Errorf("alpha/query sum = %v seconds, want 60", p.Sum)
		}
		if setString(p.Attributes) == "kind=beta,origin=read" && p.Sum != 0 {
			t.Errorf("a negative delay must record zero, got %v", p.Sum)
		}
	}
}

func TestEveryConfiguredKindAndOriginWordIsAcceptedUnfolded(t *testing.T) {
	kinds := []string{"alpha", "beta", "gamma"}
	for _, k := range kinds {
		if got := KindLabel(kinds, k); got != k {
			t.Errorf("kind %q folded to %q", k, got)
		}
	}
	if got := KindLabel(nil, "alpha"); got != "unknown" {
		t.Errorf("a kind with no configured set folded to %q, want unknown", got)
	}
	for _, w := range []string{engine.OriginQuery, engine.OriginRefresh, engine.OriginWrite, engine.OriginRead} {
		if got := OriginLabel(w); got != w {
			t.Errorf("origin %q folded to %q", w, got)
		}
	}
	for _, w := range []string{engine.ResultSettled, engine.ResultFailed, engine.ResultTimeout} {
		if got := ResultLabel(w); got != w {
			t.Errorf("result %q folded to %q", w, got)
		}
	}
}

func TestSettlePendingAndWatchedReportTheSourceCountAndSkipOnError(t *testing.T) {
	f := newFixture(t)
	n, fail := 3, false
	f.build("p.", func(c *Config) {
		c.Sources.Pending = func(context.Context) (int, error) {
			if fail {
				return 0, errors.New("store closed")
			}
			return n, nil
		}
		c.Sources.Watched = func(context.Context) (int, error) {
			if fail {
				return 0, errors.New("store closed")
			}
			return n * 10, nil
		}
	})
	if v := gaugeInts(t, f.metric("p.change.settle_pending"))[""]; v != 3 {
		t.Errorf("settle_pending = %d, want 3", v)
	}
	if v := gaugeInts(t, f.metric("p.watched.entities"))[""]; v != 30 {
		t.Errorf("watched.entities = %d, want 30", v)
	}
	n = 0
	if v := gaugeInts(t, f.metric("p.watched.entities"))[""]; v != 0 {
		t.Errorf("watched.entities = %d, want 0", v)
	}
	fail = true
	got := f.collect()
	for _, name := range []string{"p.change.settle_pending", "p.watched.entities"} {
		if _, ok := got[name]; ok {
			t.Errorf("%s must be unreported while its source fails", name)
		}
	}
}

func TestConsumerGaugesReportEachStoredKeyAndFoldUnconfiguredQueries(t *testing.T) {
	f := newFixture(t)
	f.build("p.", func(c *Config) {
		c.Sources.ConfiguredQueries = func() []string { return []string{"example-query"} }
		c.Sources.Consumers = positions(
			feed.Position{Consumer: "example-a", View: "v1", Scope: "example-query", LagRows: 4, OldestUnacked: t0.Add(-90 * time.Second)},
			feed.Position{Consumer: "example-a", View: "v2", Scope: "example-query", LagRows: 1, OldestUnacked: t0.Add(-30 * time.Second)},
			feed.Position{Consumer: "example-b", View: "v1", Scope: "ad-hoc/key#41"},
			feed.Position{Consumer: "example-b", View: "v2", Scope: "ad-hoc?assignee=x", LagRows: 2, OldestUnacked: t0.Add(-10 * time.Second)},
		)
	})
	lag := gaugeInts(t, f.metric("p.consumer.lag_rows"))
	if want := map[string]int64{"consumer=example-a,query=example-query": 5, "consumer=example-b,query=other": 2}; !equalInts(lag, want) {
		t.Errorf("lag = %v, want %v", lag, want)
	}
	oldest := gaugeFloats(t, f.metric("p.consumer.oldest_unacked_age_seconds"))
	if oldest["consumer=example-a,query=example-query"] != 90 || oldest["consumer=example-b,query=other"] != 10 || len(oldest) != 2 {
		t.Errorf("oldest = %v", oldest)
	}
}

func TestOldestUnackedIsZeroWithNothingUnacknowledgedAndGrowsWithTheClock(t *testing.T) {
	f := newFixture(t)
	f.build("p.", func(c *Config) {
		c.Sources.ConfiguredQueries = func() []string { return []string{"example-query"} }
		c.Sources.Consumers = positions(
			feed.Position{Consumer: "idle", Scope: "example-query"},
			feed.Position{Consumer: "behind", Scope: "example-query", LagRows: 1, OldestUnacked: t0},
		)
	})
	m := gaugeFloats(t, f.metric("p.consumer.oldest_unacked_age_seconds"))
	if v, ok := m["consumer=idle,query=example-query"]; !ok || v != 0 {
		t.Errorf("idle age = %v (present %v), want a series with value 0", v, ok)
	}
	f.now = t0.Add(5 * time.Minute)
	m = gaugeFloats(t, f.metric("p.consumer.oldest_unacked_age_seconds"))
	if v := m["consumer=behind,query=example-query"]; v != 300 {
		t.Errorf("behind age = %v, want 300", v)
	}
}

func TestRedeliveredAddsRowsOnlyUnderStoredConsumersAndConfiguredQueries(t *testing.T) {
	f := newFixture(t)
	m := f.build("p.", func(c *Config) {
		c.Sources.ConfiguredQueries = func() []string { return []string{"example-query"} }
		c.Sources.Consumers = positions(feed.Position{Consumer: "example-a", Scope: "example-query"})
	})
	o := m.FeedObserver()

	// Before any Consumers poll no consumer is known to be stored.
	o.Redelivered("example-a", "example-query", 1)
	f.collect() // polls Consumers, which records example-a as stored
	o.Redelivered("example-a", "example-query", 3)
	o.Redelivered("example-a", "example-query", 2)
	o.Redelivered("example-a", "ad-hoc/key#41", 1)
	o.Redelivered("minted-by-a-request", "example-query", 4)
	o.Redelivered("example-a", "example-query", 0)

	want := map[string]int64{
		"consumer=other,query=example-query":     1 + 4,
		"consumer=example-a,query=example-query": 3 + 2,
		"consumer=example-a,query=other":         1,
	}
	if got := counterSums(t, f.metric("p.consumer.redeliveries")); !equalInts(got, want) {
		t.Errorf("redelivery series = %v, want %v", got, want)
	}
}

func TestQueryLastSuccessAgeIsOneSeriesPerConfiguredQuery(t *testing.T) {
	f := newFixture(t)
	f.build("p.", func(c *Config) {
		c.Sources.ConfiguredQueries = func() []string { return []string{"fresh", "never", "fresh"} }
		c.Sources.Queries = func(context.Context) ([]store.QueryRow, error) {
			return []store.QueryRow{
				{Key: "fresh", Configured: true, LastSuccessAt: t0.Add(-2 * time.Minute)},
				{Key: "fresh?assignee=x", LastSuccessAt: t0},
				{Key: "dropped", Configured: true, LastSuccessAt: t0},
			}, nil
		}
	})
	got := gaugeFloats(t, f.metric("p.query.last_success_age_seconds"))
	if len(got) != 2 || got["query=fresh"] != 120 || got["query=never"] != 0 {
		t.Fatalf("ages = %v, want fresh 120 and never 0", got)
	}
	f.now = t0.Add(10 * time.Minute)
	got = gaugeFloats(t, f.metric("p.query.last_success_age_seconds"))
	if got["query=fresh"] != 720 || got["query=never"] != 600 {
		t.Errorf("ages after 10m = %v, want fresh 720 and never 600 (counted from Started)", got)
	}
}

func TestQueryAgeCountsFromNewWhenStartedIsUnset(t *testing.T) {
	f := newFixture(t)
	f.build("p.", func(c *Config) {
		c.Started = time.Time{}
		c.Sources.ConfiguredQueries = func() []string { return []string{"never"} }
		c.Sources.Queries = func(context.Context) ([]store.QueryRow, error) { return nil, nil }
	})
	f.now = t0.Add(time.Minute)
	if v := gaugeFloats(t, f.metric("p.query.last_success_age_seconds"))["query=never"]; v != 60 {
		t.Errorf("age = %v, want 60", v)
	}
}

func TestGaugesAreUnreportedWhenTheirSourcesAreUnset(t *testing.T) {
	f := newFixture(t)
	f.build("p.", nil)
	got := f.collect()
	for _, name := range []string{
		"p.change.settle_pending", "p.consumer.lag_rows", "p.consumer.oldest_unacked_age_seconds",
		"p.query.last_success_age_seconds", "p.watched.entities",
	} {
		if _, ok := got[name]; ok {
			t.Errorf("%s reported with its source unset", name)
		}
	}

	// A query source without the configured list reports nothing.
	f2 := newFixture(t)
	f2.build("p.", func(c *Config) {
		c.Sources.Queries = func(context.Context) ([]store.QueryRow, error) { return nil, nil }
	})
	if _, ok := f2.collect()["p.query.last_success_age_seconds"]; ok {
		t.Error("query.last_success_age_seconds reported without ConfiguredQueries")
	}
}

func TestCloseStopsThePolledGauges(t *testing.T) {
	f := newFixture(t)
	m := f.build("p.", func(c *Config) {
		c.Sources.Watched = func(context.Context) (int, error) { return 1, nil }
	})
	if _, ok := f.collect()["p.watched.entities"]; !ok {
		t.Fatal("watched.entities absent before Close")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.collect()["p.watched.entities"]; ok {
		t.Error("watched.entities still reported after Close")
	}
}

func TestObserversAreSafeUnderConcurrentUse(t *testing.T) {
	f := newFixture(t)
	m := f.build("p.", func(c *Config) {
		c.Sources.ConfiguredQueries = func() []string { return []string{"example-query"} }
		c.Sources.Consumers = positions(feed.Position{Consumer: "example-a", Scope: "example-query", LagRows: 1})
		c.Sources.Pending = func(context.Context) (int, error) { return 1, nil }
	})
	co, fo := m.ChangeObserver(), m.FeedObserver()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				co.Settled(engine.ResultSettled)
				co.Detected("alpha", engine.OriginQuery, time.Second)
				fo.Redelivered("example-a", "example-query", 1)
				f.collect()
			}
		}()
	}
	wg.Wait()
}
