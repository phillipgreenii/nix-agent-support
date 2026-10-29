package metrics

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
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
	return changes.Flow{Migrated: true, Types: []changes.TypeFlow{{Type: "pr", Active: 88}, {Type: "issue", Active: 3}, {Type: "thread", Active: 5}}}
}

func TestSweepBoundMetricReadsOneWhenViolatedAndZeroWhenHolding(t *testing.T) {
	// thread: the local tier is violated, the remote tier holds. issue: the
	// evaluator would call both tiers violated, but the issue type has no age
	// sweep, so no series is emitted for it. pr: the evaluator would call it
	// violated too, but pr is not a change-flow type (the daemon is the PR
	// feed), so no series is emitted for it either.
	eval := func(tf changes.TypeFlow, tier string) changes.BoundVerdict {
		if tf.Type == "issue" || tf.Type == "pr" {
			return changes.BoundViolated
		}
		if tf.Type == "thread" && tier == changes.TierLocal {
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
	want := map[string]int64{"thread/local": 1, "thread/remote": 0}
	got := boundPoints(t, rm)
	for k := range got {
		if strings.HasPrefix(k, "issue/") || strings.HasPrefix(k, "pr/") {
			t.Errorf("series %s emitted: only thread has a sweep-bound series", k)
		}
	}
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

// For issue the due-backlog gauge counts only pending hydrations: the watched
// never-hydrated entities plus the failed-ingest entries. A hydrated set, however
// old, reads 0 (nothing re-hydrates an issue on a clock).
func TestDueBacklogGaugeForIssueCountsOnlyPendingHydrations(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	seed := func(id, hydratedAt string, watched bool) {
		t.Helper()
		e := store.Entity{Repo: "o/r", EntityType: "issue", EntityID: id, Facts: `{}`, AsOf: "2026-09-01T00:00:00Z"}
		if _, err := st.WriteEntityStateWithLog(e, 0, hydratedAt, true, []string{"created"}, "sync", "2026-09-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
		backend := "beads-a"
		if err := st.SetEntityColumns("o/r", "issue", id, &backend, &watched); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	issueDue := func(t *testing.T) int64 {
		t.Helper()
		rm, err := collectChangeFlow(t, func() (changes.Flow, error) { return changes.Observe(st, now, 6*time.Hour) })
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
		return got
	}

	for i := 0; i < 50; i++ {
		seed(fmt.Sprintf("bd-old-%d", i), "2026-01-01T00:00:00Z", true)
	}
	if got := issueDue(t); got != 0 {
		t.Errorf("%s{type=issue} with 50 hydrated watched issues = %d, want 0", MetricDueBacklog, got)
	}

	for i := 0; i < 3; i++ {
		seed(fmt.Sprintf("bd-new-%d", i), "", true)
	}
	seed("bd-unwatched", "", false)
	for _, f := range []store.IngestFailure{
		{Instance: "jira-a", EntityID: "PROJ-1", Seq: 1, StoreIdentity: "s", FirstFailedAt: "2026-10-01T11:00:00Z"},
		{Instance: "jira-a", EntityID: "PROJ-2", Seq: 2, StoreIdentity: "s", FirstFailedAt: "2026-10-01T11:00:00Z"},
	} {
		if err := st.RecordIngestFailure(f); err != nil {
			t.Fatal(err)
		}
	}
	if got := issueDue(t); got != 5 {
		t.Errorf("%s{type=issue} with 3 pending and 2 failed ingests = %d, want 5", MetricDueBacklog, got)
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

// outcomePoints collects one outcome family into per-backend values.
func outcomePoints(t *testing.T, name string, opt ChangeFlowOption, migrated bool) (map[string]int64, error) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	flow := func() (changes.Flow, error) { return changes.Flow{Migrated: migrated}, nil }
	if err := RegisterChangeFlow(mp, flow, opt); err != nil {
		t.Fatal(err)
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			for _, p := range m.Data.(metricdata.Gauge[int64]).DataPoints {
				be, _ := p.Attributes.Value("backend")
				out[be.AsString()] = p.Value
			}
		}
	}
	return out, nil
}

// The watched-source age is the OLDEST feed_observed value across an instance's
// own queries: a key of another instance or of a query that is not configured
// for the instance does not count, and an instance with no key reads 0.
func TestWatchedSourceObservedAgeIsTheOldestOfTheInstancesQueries(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for k, at := range map[string]time.Duration{
		changes.MetaFeedObservedPrefix + "jira-a.mine":  10 * time.Minute,
		changes.MetaFeedObservedPrefix + "jira-a.team":  26 * time.Hour,
		changes.MetaFeedObservedPrefix + "jira-a.stray": 90 * time.Hour, // not a configured query of the instance
		changes.MetaFeedObservedPrefix + "jira-b.mine":  time.Hour,
	} {
		if err := st.SetMeta(k, now.Add(-at).Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	queries := map[string][]string{"jira-a": {"mine", "team"}, "jira-b": {"mine"}, "jira-c": {"mine"}}
	got, err := outcomePoints(t, MetricWatchedSourceObservedAge,
		WithWatchedSourceObserved(WatchedSourceObservedFromStore(st, queries, func() time.Time { return now })), true)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"jira-a": 26 * 3600, "jira-b": 3600, "jira-c": 0}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ages = %v, want %v", got, want)
	}
}

func TestOutcomeSeriesExistWithNoOwningInstanceAndFailLoudlyOnBadData(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	for name, opt := range map[string]ChangeFlowOption{
		MetricWatchedSourceObservedAge: WithWatchedSourceObserved(WatchedSourceObservedFromStore(st, nil, time.Now)),
		MetricIssueUnflaggedMembers:    WithUnflaggedMembers(UnflaggedMembersFromStore(st, nil)),
	} {
		got, err := outcomePoints(t, name, opt, true)
		if err != nil || !reflect.DeepEqual(got, map[string]int64{"": 0}) {
			t.Errorf("%s with no instance = %v, %v, want one backendless 0", name, got, err)
		}
		// An unmigrated store emits nothing and never calls the reader.
		if got, err := outcomePoints(t, name, opt, false); err != nil || len(got) != 0 {
			t.Errorf("%s on an unmigrated store = %v, %v", name, got, err)
		}
	}
	queries := map[string][]string{"jira-a": {"mine"}}
	if err := st.SetMeta(changes.MetaFeedObservedPrefix+"jira-a.mine", "yesterday"); err != nil {
		t.Fatal(err)
	}
	if _, err := outcomePoints(t, MetricWatchedSourceObservedAge,
		WithWatchedSourceObserved(WatchedSourceObservedFromStore(st, queries, time.Now)), true); err == nil {
		t.Error("an unparseable observation must fail the scrape")
	}
	if err := st.SetMeta(changes.MetaUnflaggedPrefix+"jira-a", "{not json"); err != nil {
		t.Fatal(err)
	}
	if _, err := outcomePoints(t, MetricIssueUnflaggedMembers,
		WithUnflaggedMembers(UnflaggedMembersFromStore(st, queries)), true); err == nil {
		t.Error("an undecodable unflagged record must fail the scrape")
	}
}

func TestUnflaggedMembersSeriesIsTheStoredCountPerInstance(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	if err := st.SetMeta(changes.MetaUnflaggedPrefix+"jira-a", `{"count":4,"at":"2026-10-10T11:55:00Z"}`); err != nil {
		t.Fatal(err)
	}
	queries := map[string][]string{"jira-a": {"mine"}, "jira-b": {"mine"}}
	got, err := outcomePoints(t, MetricIssueUnflaggedMembers, WithUnflaggedMembers(UnflaggedMembersFromStore(st, queries)), true)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]int64{"jira-a": 4, "jira-b": 0}; !reflect.DeepEqual(got, want) {
		t.Errorf("counts = %v, want %v", got, want)
	}
}

// listedOutcomeConfig serves the watched queries "open" and "mine" from the
// beads instance beads-a, and "other" from beads-b; "stray" has no backends
// entry and "feed" is an owning (feed) query.
func listedOutcomeConfig() *config.Config {
	cfg := &config.Config{Repos: []config.RepoConfig{{Remote: "o/r"}}}
	cfg.Watch.Issue.Queries = []string{"open", "mine", "other", "stray", "feed"}
	cfg.Watch.Issue.Backends = map[string]config.WatchBackend{
		"open":  {Instance: "beads-a"},
		"mine":  {Instance: "beads-a"},
		"other": {Instance: "beads-b"},
		"feed":  {Instance: "jira-a", OwnsChanges: true},
	}
	return cfg
}

func TestListedQueriesByInstanceIsTheNonOwningQueriesWithABackendsEntry(t *testing.T) {
	got := ListedQueriesByInstance(listedOutcomeConfig())
	want := map[string][]string{"beads-a": {"open", "mine"}, "beads-b": {"other"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListedQueriesByInstance = %v, want %v", got, want)
	}
}

// The beads observed age is the OLDEST of the instance's listing_ok times and
// its orphans' last successful read; another instance's orphan and a query
// with no key do not count, and an instance with nothing reads 0.
func TestListedSourceObservedAgeIsTheOldestOfListingsAndOrphans(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	cfg := listedOutcomeConfig()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	yes := true
	for k, ago := range map[string]time.Duration{
		changes.MetaListingOKPrefix + "open":  48 * time.Hour,
		changes.MetaListingOKPrefix + "other": time.Hour,
		changes.MetaListingOKPrefix + "stray": 90 * time.Hour, // names no instance
	} {
		if err := st.SetMeta(k, now.Add(-ago).Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	hyd := now.Add(-time.Hour).Format(time.RFC3339Nano)
	okAt := now.Add(-30 * time.Minute).Format(time.RFC3339Nano)
	seedWatchedIssue(t, st, "bd-orphan", hyd, true, &yes, "beads-a", okAt) // an orphan of beads-a, read 30 minutes ago
	seedWatchedIssue(t, st, "bd-other", hyd, true, &yes, "beads-b", now.Add(-72*time.Hour).Format(time.RFC3339Nano))
	reader := func() map[string]int64 {
		t.Helper()
		got, err := outcomePoints(t, MetricWatchedSourceObservedAge,
			WithWatchedSourceObserved(WatchedSourceObservedForConfig(st, cfg, func() time.Time { return now })), true)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	got := reader()
	// beads-a: listing 48h against orphan 30m: the 48h listing is oldest.
	// beads-b: its orphan read 72h ago is older than its 1h listing.
	// jira-a (owning, "feed"): no feed_observed key yet.
	want := map[string]int64{"beads-a": 48 * 3600, "beads-b": 72 * 3600, "jira-a": 0}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ages = %v, want %v", got, want)
	}
	// A fresh listing leaves the 30 minute orphan as the oldest source of beads-a.
	if err := st.SetMeta(changes.MetaListingOKPrefix+"open", now.Add(-time.Minute).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMeta(changes.MetaListingOKPrefix+"mine", now.Add(-2*time.Minute).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if got := reader()["beads-a"]; got != 30*60 {
		t.Errorf("beads-a age = %d, want %d (the orphan's last read)", got, 30*60)
	}
	// No value recorded at all reads 0.
	empty, err := outcomePoints(t, MetricWatchedSourceObservedAge,
		WithWatchedSourceObserved(WatchedSourceObservedForConfig(store.OpenNewSchemaForTest(t), cfg, func() time.Time { return now })), true)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]int64{"beads-a": 0, "beads-b": 0, "jira-a": 0}; !reflect.DeepEqual(empty, want) {
		t.Errorf("fresh ages = %v, want %v", empty, want)
	}
	if err := st.SetMeta(changes.MetaListingOKPrefix+"open", "yesterday"); err != nil {
		t.Fatal(err)
	}
	if _, err := outcomePoints(t, MetricWatchedSourceObservedAge,
		WithWatchedSourceObserved(WatchedSourceObservedForConfig(st, cfg, func() time.Time { return now })), true); err == nil {
		t.Error("an unparseable listing_ok must fail the scrape")
	}
}

// The unflagged series carries every beads instance too, from the same stored
// record, and a deployment with neither kind keeps the one backendless 0.
func TestUnflaggedSeriesCoversBeadsInstancesAndKeepsThePlaceholder(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	if err := st.SetMeta(changes.MetaUnflaggedPrefix+"beads-a", `{"count":3,"at":"2026-10-10T11:55:00Z"}`); err != nil {
		t.Fatal(err)
	}
	got, err := outcomePoints(t, MetricIssueUnflaggedMembers, WithUnflaggedMembers(UnflaggedMembersForConfig(st, listedOutcomeConfig())), true)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]int64{"beads-a": 3, "beads-b": 0, "jira-a": 0}; !reflect.DeepEqual(got, want) {
		t.Errorf("counts = %v, want %v", got, want)
	}
	none := &config.Config{}
	for name, opt := range map[string]ChangeFlowOption{
		MetricWatchedSourceObservedAge: WithWatchedSourceObserved(WatchedSourceObservedForConfig(st, none, time.Now)),
		MetricIssueUnflaggedMembers:    WithUnflaggedMembers(UnflaggedMembersForConfig(st, none)),
	} {
		got, err := outcomePoints(t, name, opt, true)
		if err != nil || !reflect.DeepEqual(got, map[string]int64{"": 0}) {
			t.Errorf("%s with no instance = %v, %v, want one backendless 0", name, got, err)
		}
	}
}
