package metrics

import (
	"context"
	"errors"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

func collectChangeFlow(t *testing.T, fn ChangeFlowFunc) (metricdata.ResourceMetrics, error) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	if err := RegisterChangeFlow(mp, fn); err != nil {
		t.Fatalf("RegisterChangeFlow: %v", err)
	}
	var rm metricdata.ResourceMetrics
	err := reader.Collect(context.Background(), &rm)
	return rm, err
}

func TestChangeFlowMetricsCarryTheCatalogWithLabels(t *testing.T) {
	rm, err := collectChangeFlow(t, func() (changes.Flow, error) {
		return changes.Flow{
			Migrated: true,
			Types: []changes.TypeFlow{{
				Type:     "pr",
				Records:  []changes.RecordCount{{Kind: "created", Origin: "poll", Count: 4}},
				Due:      3,
				Stats:    changes.HydrationStats{Hydrations: 9, Failures: 2, OCCRetries: 5},
				Repeated: map[string]changes.DegradedEntity{"o/r#1": {Count: 2}},
			}},
			Consumers: []changes.ConsumerFlow{{Name: "alpha", Type: "pr", Lag: 7}},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{
		MetricChangeRecords:     4,
		MetricHydrations:        9,
		MetricHydrationFailures: 2,
		MetricOCCRetries:        5,
		MetricDueBacklog:        3,
		MetricConsumerLag:       7,
		MetricRepeatedDegraded:  1,
	}
	for name, v := range want {
		m := findMetric(t, rm, name)
		var got int64
		switch d := m.Data.(type) {
		case metricdata.Gauge[int64]:
			if len(d.DataPoints) != 1 {
				t.Fatalf("%s: %d points", name, len(d.DataPoints))
			}
			got = d.DataPoints[0].Value
			if typ, _ := d.DataPoints[0].Attributes.Value("type"); typ.AsString() != "pr" {
				t.Errorf("%s: type label = %q", name, typ.AsString())
			}
			if name == MetricChangeRecords {
				k, _ := d.DataPoints[0].Attributes.Value("kind")
				o, _ := d.DataPoints[0].Attributes.Value("origin")
				if k.AsString() != "created" || o.AsString() != "poll" {
					t.Errorf("%s: kind/origin = %q/%q", name, k.AsString(), o.AsString())
				}
			}
			if name == MetricConsumerLag {
				c, _ := d.DataPoints[0].Attributes.Value("consumer")
				if c.AsString() != "alpha" {
					t.Errorf("%s: consumer label = %q", name, c.AsString())
				}
			}
		case metricdata.Sum[int64]:
			if len(d.DataPoints) != 1 {
				t.Fatalf("%s: %d points", name, len(d.DataPoints))
			}
			got = d.DataPoints[0].Value
		default:
			t.Fatalf("%s: unexpected data %T", name, m.Data)
		}
		if got != v {
			t.Errorf("%s = %d, want %d", name, got, v)
		}
	}
}

func TestChangeFlowMetricsOmitSeriesWhenUnmigrated(t *testing.T) {
	rm, err := collectChangeFlow(t, func() (changes.Flow, error) { return changes.Flow{}, nil })
	if err != nil {
		t.Fatalf("an unmigrated store must not fail the scrape: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch d := m.Data.(type) {
			case metricdata.Gauge[int64]:
				if len(d.DataPoints) != 0 {
					t.Errorf("%s emitted %d points on an unmigrated store", m.Name, len(d.DataPoints))
				}
			case metricdata.Sum[int64]:
				if len(d.DataPoints) != 0 {
					t.Errorf("%s emitted %d points on an unmigrated store", m.Name, len(d.DataPoints))
				}
			}
		}
	}
}

func TestChangeFlowMetricsSurfaceAReadError(t *testing.T) {
	_, err := collectChangeFlow(t, func() (changes.Flow, error) { return changes.Flow{}, errors.New("store gone") })
	if err == nil {
		t.Fatal("a failing change-flow read must surface from Collect")
	}
}

// boundPoints collects pg_desk_sweep_bound_violated as "type/tier" -> value.
func boundPoints(t *testing.T, rm metricdata.ResourceMetrics) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != MetricSweepBoundViolated {
				continue
			}
			g, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				t.Fatalf("%s: unexpected data %T", m.Name, m.Data)
			}
			for _, dp := range g.DataPoints {
				typ, _ := dp.Attributes.Value("type")
				tier, _ := dp.Attributes.Value("tier")
				out[typ.AsString()+"/"+tier.AsString()] = dp.Value
			}
		}
	}
	return out
}

func sweepFlow() changes.Flow {
	return changes.Flow{Migrated: true, Types: []changes.TypeFlow{{Type: "pr", Active: 88}, {Type: "issue", Active: 3}}}
}

func TestSweepBoundMetricReadsOneWhenViolatedAndZeroWhenHolding(t *testing.T) {
	// pr: the local tier is violated, the remote tier holds. issue: both hold.
	eval := func(tf changes.TypeFlow, tier string) changes.BoundVerdict {
		if tf.Type == "pr" && tier == changes.TierLocal {
			return changes.BoundViolated
		}
		return changes.BoundHolds
	}
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	if err := RegisterChangeFlow(mp, func() (changes.Flow, error) { return sweepFlow(), nil }, WithSweepBound(eval)); err != nil {
		t.Fatal(err)
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"pr/local": 1, "pr/remote": 0, "issue/local": 0, "issue/remote": 0}
	got := boundPoints(t, rm)
	if len(got) != len(want) {
		t.Fatalf("points = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
}

func TestSweepBoundMetricHasNoSeriesWithoutAVerdict(t *testing.T) {
	unknown := func(changes.TypeFlow, string) changes.BoundVerdict { return changes.BoundUnknown }
	for name, opts := range map[string][]ChangeFlowOption{
		"unknown poll interval": {WithSweepBound(unknown)},
		"no evaluator supplied": nil,
	} {
		reader := sdkmetric.NewManualReader()
		mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
		if err := RegisterChangeFlow(mp, func() (changes.Flow, error) { return sweepFlow(), nil }, opts...); err != nil {
			t.Fatal(err)
		}
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatal(err)
		}
		if got := boundPoints(t, rm); len(got) != 0 {
			t.Errorf("%s: emitted %v, want no series (no verdict is never a false violation)", name, got)
		}
	}
}

func TestSweepBoundMetricIgnoresNonFlowTypes(t *testing.T) {
	flow := changes.Flow{Migrated: true, Types: []changes.TypeFlow{{Type: "consumer-only-type", Active: 1}}}
	holds := func(changes.TypeFlow, string) changes.BoundVerdict { return changes.BoundHolds }
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	if err := RegisterChangeFlow(mp, func() (changes.Flow, error) { return flow, nil }, WithSweepBound(holds)); err != nil {
		t.Fatal(err)
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	if got := boundPoints(t, rm); len(got) != 0 {
		t.Errorf("emitted %v for a non-flow type", got)
	}
}

// The due-backlog gauge counts only entities the age sweep can reach: with an
// owning backend configured, an age-stale owning-backend issue is not counted
// and the stale beads issue is.
func TestDueBacklogGaugeExcludesOwningBackendEntities(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	seed := func(id, backend string) {
		t.Helper()
		e := store.Entity{Repo: "o/r", EntityType: "issue", EntityID: id, Facts: `{}`, AsOf: "2026-09-01T00:00:00Z"}
		if _, err := st.WriteEntityStateWithLog(e, 0, "2026-09-01T00:00:00Z", true, []string{"created"}, "sync", "2026-09-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
		if err := st.SetEntityColumns("o/r", "issue", id, &backend, nil); err != nil {
			t.Fatal(err)
		}
	}
	seed("bd-1", "beads-a")
	seed("PROJ-1", "jira-a")
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name   string
		owning map[string]bool
		want   int64
	}{
		{"no owning backend", nil, 2},
		{"jira-a owns changes", map[string]bool{"jira-a": true}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rm, err := collectChangeFlow(t, func() (changes.Flow, error) {
				return changes.Observe(st, now, 6*time.Hour, tc.owning)
			})
			if err != nil {
				t.Fatal(err)
			}
			m := findMetric(t, rm, MetricDueBacklog)
			d, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				t.Fatalf("%s is %T, want an int64 gauge", MetricDueBacklog, m.Data)
			}
			var got int64 = -1
			for _, p := range d.DataPoints {
				if typ, _ := p.Attributes.Value("type"); typ.AsString() == "issue" {
					got = p.Value
				}
			}
			if got != tc.want {
				t.Errorf("%s{type=issue} = %d, want %d", MetricDueBacklog, got, tc.want)
			}
		})
	}
}

// ingestPoints collects the two failed-ingest families into per-backend
// values: count, and oldest age in seconds.
func ingestPoints(t *testing.T, rm metricdata.ResourceMetrics) (count, age map[string]int64) {
	t.Helper()
	read := func(name string) map[string]int64 {
		out := map[string]int64{}
		d, ok := findMetric(t, rm, name).Data.(metricdata.Gauge[int64])
		if !ok {
			t.Fatalf("%s is not an int64 gauge", name)
		}
		for _, p := range d.DataPoints {
			be, _ := p.Attributes.Value("backend")
			out[be.AsString()] = p.Value
		}
		return out
	}
	return read(MetricIngestFailed), read(MetricIngestFailedOldestAge)
}

func collectIngest(t *testing.T, fn IngestFailedFunc, migrated bool) (metricdata.ResourceMetrics, error) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	flow := func() (changes.Flow, error) { return changes.Flow{Migrated: migrated}, nil }
	if err := RegisterChangeFlow(mp, flow, WithIngestFailed(fn)); err != nil {
		t.Fatalf("RegisterChangeFlow: %v", err)
	}
	var rm metricdata.ResourceMetrics
	err := reader.Collect(context.Background(), &rm)
	return rm, err
}

// The failed-ingest series come from the table at scrape time: two entries of
// one instance read as count 2 and the age of the older one, a configured
// owning instance with no entry reads 0 and 0 (never absent), and an entry of
// an instance that is not configured still gets its own series.
func TestIngestFailedSeriesReadTheTableAtScrapeTime(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, f := range []store.IngestFailure{
		{Instance: "jira-a", EntityID: "PROJ-1", Seq: 4, StoreIdentity: "s1", FirstFailedAt: now.Add(-10 * time.Minute).Format(time.RFC3339), LastError: "boom"},
		{Instance: "jira-a", EntityID: "PROJ-2", Seq: 5, StoreIdentity: "s1", FirstFailedAt: now.Add(-40 * time.Minute).Format(time.RFC3339), LastError: "boom"},
		{Instance: "stray", EntityID: "X-1", Seq: 1, StoreIdentity: "s9", FirstFailedAt: now.Add(-5 * time.Minute).Format(time.RFC3339), LastError: "boom"},
	} {
		if err := st.RecordIngestFailure(f); err != nil {
			t.Fatal(err)
		}
	}
	rm, err := collectIngest(t, IngestFailedFromStore(st, []string{"jira-a", "jira-b"}, func() time.Time { return now }), true)
	if err != nil {
		t.Fatal(err)
	}
	count, age := ingestPoints(t, rm)
	wantCount := map[string]int64{"jira-a": 2, "jira-b": 0, "stray": 1}
	wantAge := map[string]int64{"jira-a": 2400, "jira-b": 0, "stray": 300}
	for be, v := range wantCount {
		if got, ok := count[be]; !ok || got != v {
			t.Errorf("%s{backend=%s} = %d (present %v), want %d", MetricIngestFailed, be, got, ok, v)
		}
		if got, ok := age[be]; !ok || got != wantAge[be] {
			t.Errorf("%s{backend=%s} = %d (present %v), want %d", MetricIngestFailedOldestAge, be, got, ok, wantAge[be])
		}
	}
	if len(count) != len(wantCount) || len(age) != len(wantCount) {
		t.Errorf("series = %v / %v, want exactly the backends %v", count, age, wantCount)
	}

	// Clearing the entries empties the table: both families read 0 again.
	for _, f := range []store.IngestFailure{{Instance: "jira-a", EntityID: "PROJ-1", Seq: 4}, {Instance: "jira-a", EntityID: "PROJ-2", Seq: 5}} {
		if err := st.ClearIngestFailure(f.Instance, f.EntityID, f.Seq); err != nil {
			t.Fatal(err)
		}
	}
	rm, err = collectIngest(t, IngestFailedFromStore(st, []string{"jira-a"}, func() time.Time { return now }), true)
	if err != nil {
		t.Fatal(err)
	}
	count, age = ingestPoints(t, rm)
	if count["jira-a"] != 0 || age["jira-a"] != 0 {
		t.Errorf("after clearing: count %d, age %d, want 0 and 0", count["jira-a"], age["jira-a"])
	}
}

// With no owning instance and an empty table the families still exist (one
// series without a backend, valued 0), so a deployment without an owning
// backend never reads as "the store cannot answer".
func TestIngestFailedSeriesExistWithNoInstance(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	rm, err := collectIngest(t, IngestFailedFromStore(st, nil, time.Now), true)
	if err != nil {
		t.Fatal(err)
	}
	count, age := ingestPoints(t, rm)
	if v, ok := count[""]; !ok || v != 0 || len(count) != 1 {
		t.Errorf("count = %v, want one backendless series at 0", count)
	}
	if v, ok := age[""]; !ok || v != 0 || len(age) != 1 {
		t.Errorf("age = %v, want one backendless series at 0", age)
	}
}

// A store that is not on the new schema yields no series, and a failed read
// or an unparseable timestamp fails the scrape rather than reading as 0.
func TestIngestFailedSeriesAbsentWhenTheStoreCannotAnswer(t *testing.T) {
	called := false
	rm, err := collectIngest(t, func() ([]IngestFailedBackend, error) { called = true; return nil, nil }, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == MetricIngestFailed || m.Name == MetricIngestFailedOldestAge {
				if d, ok := m.Data.(metricdata.Gauge[int64]); ok && len(d.DataPoints) != 0 {
					t.Errorf("%s emitted %d points on an unmigrated store", m.Name, len(d.DataPoints))
				}
			}
		}
	}
	if called {
		t.Error("the table was read on an unmigrated store")
	}
	if _, err := collectIngest(t, func() ([]IngestFailedBackend, error) { return nil, errors.New("store gone") }, true); err == nil {
		t.Error("a failing read must surface from Collect")
	}
	st := store.OpenNewSchemaForTest(t)
	if err := st.RecordIngestFailure(store.IngestFailure{Instance: "jira-a", EntityID: "P-1", Seq: 1, FirstFailedAt: "not-a-time"}); err != nil {
		t.Fatal(err)
	}
	if _, err := collectIngest(t, IngestFailedFromStore(st, []string{"jira-a"}, time.Now), true); err == nil {
		t.Error("an unparseable first_failed_at must fail the scrape")
	}
}
