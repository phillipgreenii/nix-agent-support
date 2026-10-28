package metrics

import (
	"context"
	"reflect"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// These tests pin the watched-entity and oldest-orphan series: both are read
// from the entity table at scrape time, and the orphan age counts from the last
// SUCCESSFUL read so a failing orphan keeps ageing.

var orphanNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func orphanTestConfig() *config.Config {
	cfg := &config.Config{Repos: []config.RepoConfig{{Remote: "o/r"}}}
	cfg.Watch.Issue.Queries = []string{"open"}
	return cfg
}

// seedWatchedIssue stores an issue entity hydrated at hydratedAt with the given
// watched mark, backend column ("" = NULL) and read_ok_at ("" = never).
func seedWatchedIssue(t *testing.T, st *store.Store, id, hydratedAt string, active bool, watched *bool, backend, readOK string) {
	t.Helper()
	fp := "F1"
	if _, err := st.WriteEntityStateWithLogFP(store.Entity{Repo: "o/r", EntityType: "issue", EntityID: id, Facts: `{}`, AsOf: "x"},
		0, hydratedAt, active, &fp, []string{"reconcile"}, "sweep", "2026-09-29T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	var b *string
	if backend != "" {
		b = &backend
	}
	if err := st.SetEntityColumns("o/r", "issue", id, b, watched); err != nil {
		t.Fatal(err)
	}
	if readOK != "" {
		if err := st.SetEntityReadTimes("o/r", "issue", id, nil, &readOK); err != nil {
			t.Fatal(err)
		}
	}
}

func minutesAgo(m int) string {
	return orphanNow.Add(-time.Duration(m) * time.Minute).Format(time.RFC3339Nano)
}

// scrapeGauges registers opts on a fresh meter and returns one gauge's samples
// as attribute-set string to value.
func scrapeGauges(t *testing.T, name string, opts ...ChangeFlowOption) map[string]int64 {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	flow := func() (changes.Flow, error) { return changes.Flow{Migrated: true}, nil }
	if err := RegisterChangeFlow(mp, flow, opts...); err != nil {
		t.Fatal(err)
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			for _, p := range m.Data.(metricdata.Gauge[int64]).DataPoints {
				typ, _ := p.Attributes.Value("type")
				be, _ := p.Attributes.Value("backend")
				key := be.AsString()
				if typ.AsString() != "" {
					key = typ.AsString() + "/" + key
				}
				out[key] = p.Value
			}
		}
	}
	return out
}

func TestOldestOrphanReadAgeIsTheOldestSuccessfulReadPerBackend(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	yes := true
	// Two orphans of the same backend whose last successful reads were 10 and
	// 45 minutes ago: the oldest decides.
	seedWatchedIssue(t, st, "bd-a", minutesAgo(1), true, &yes, "bd-x", minutesAgo(10))
	seedWatchedIssue(t, st, "bd-b", minutesAgo(1), true, &yes, "bd-x", minutesAgo(45))
	// A NULL-column orphan has the empty label; with no read_ok_at it ages from
	// hydrated_at.
	seedWatchedIssue(t, st, "bd-c", minutesAgo(20), true, &yes, "", "")
	fn := WithOldestOrphanReadAge(OldestOrphanReadAgeFromStore(st, orphanTestConfig(), func() time.Time { return orphanNow }))
	got := scrapeGauges(t, MetricOldestOrphanReadAge, fn)
	want := map[string]int64{"bd-x": 45 * 60, "": 20 * 60}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("series = %v, want %v", got, want)
	}
}

func TestOldestOrphanReadAgeKeepsAgeingWhileReadsFail(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	yes := true
	seedWatchedIssue(t, st, "bd-a", minutesAgo(90), true, &yes, "", minutesAgo(40))
	// A failing read advances read_attempt_at only; the age still counts from
	// read_ok_at.
	attempt := minutesAgo(1)
	if err := st.SetEntityReadTimes("o/r", "issue", "bd-a", &attempt, nil); err != nil {
		t.Fatal(err)
	}
	later := func() time.Time { return orphanNow.Add(10 * time.Minute) }
	fn := OldestOrphanReadAgeFromStore(st, orphanTestConfig(), later)
	rows, err := fn()
	if err != nil {
		t.Fatal(err)
	}
	if want := []OutcomeBackend{{Backend: "", Value: 50 * 60}}; !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows = %v, want %v", rows, want)
	}
}

func TestOldestOrphanReadAgeIsOneZeroSeriesWithNoOrphan(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	yes, no := true, false
	seedWatchedIssue(t, st, "unwatched", minutesAgo(500), true, &no, "", "")
	seedWatchedIssue(t, st, "never-stated", minutesAgo(500), true, nil, "", "")
	seedWatchedIssue(t, st, "inactive", minutesAgo(500), false, &yes, "", "")
	fn := WithOldestOrphanReadAge(OldestOrphanReadAgeFromStore(st, orphanTestConfig(), func() time.Time { return orphanNow }))
	got := scrapeGauges(t, MetricOldestOrphanReadAge, fn)
	if want := map[string]int64{"": 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("series = %v, want %v", got, want)
	}
}

func TestOldestOrphanReadAgeIgnoresAWatchedEntityAQueryStillHolds(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	yes := true
	seedWatchedIssue(t, st, "bd-held", minutesAgo(500), true, &yes, "", "")
	if err := st.SetMeta("change_flow.watchset.issue.open", `["bd-held"]`); err != nil {
		t.Fatal(err)
	}
	orphans, err := changes.OrphanEntities(st, orphanTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	rows, err := OldestOrphanReadAgeFromStore(st, orphanTestConfig(), func() time.Time { return orphanNow })()
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 0 || !reflect.DeepEqual(rows, []OutcomeBackend{{}}) {
		t.Fatalf("orphans = %d, rows = %v: a held member must not age as an orphan", len(orphans), rows)
	}
}

func TestOrphanReadTimeFallsBackFromReadOKToHydratedToFirstSeenToNothing(t *testing.T) {
	at := func(v string) time.Time {
		tm, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	for name, tc := range map[string]struct {
		e      store.Entity
		want   string
		wantOK bool
	}{
		"read ok wins":   {store.Entity{ReadOKAt: "2026-10-01T10:00:00Z", HydratedAt: "2026-10-01T09:00:00Z", FirstSeenAt: "2026-10-01T08:00:00Z"}, "2026-10-01T10:00:00Z", true},
		"hydrated next":  {store.Entity{HydratedAt: "2026-10-01T09:00:00Z", FirstSeenAt: "2026-10-01T08:00:00Z"}, "2026-10-01T09:00:00Z", true},
		"first seen":     {store.Entity{FirstSeenAt: "2026-10-01T08:00:00Z"}, "2026-10-01T08:00:00Z", true},
		"nothing is 0":   {store.Entity{}, "", false},
		"nano precision": {store.Entity{ReadOKAt: "2026-10-01T10:00:00.123456789Z"}, "2026-10-01T10:00:00.123456789Z", true},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok, err := orphanReadTime(tc.e)
			if err != nil || ok != tc.wantOK {
				t.Fatalf("ok=%v err=%v, want ok=%v", ok, err, tc.wantOK)
			}
			if ok && !got.Equal(at(tc.want)) {
				t.Errorf("time = %v, want %v", got, tc.want)
			}
		})
	}
	if _, _, err := orphanReadTime(store.Entity{EntityID: "bd-z", ReadOKAt: "not a time"}); err == nil {
		t.Error("an unparseable time must fail the scrape, not read as 0")
	}
}

func TestWatchedEntitiesCountsActiveWatchedByTypeAndBackend(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	yes, no := true, false
	seedWatchedIssue(t, st, "bd-1", minutesAgo(1), true, &yes, "bd-x", "")
	seedWatchedIssue(t, st, "bd-2", minutesAgo(1), true, &yes, "bd-x", "")
	seedWatchedIssue(t, st, "bd-3", minutesAgo(1), true, &yes, "", "")
	seedWatchedIssue(t, st, "bd-off", minutesAgo(1), true, &no, "bd-x", "")
	seedWatchedIssue(t, st, "bd-unset", minutesAgo(1), true, nil, "bd-x", "")
	seedWatchedIssue(t, st, "bd-gone", minutesAgo(1), false, &yes, "bd-x", "")
	got := scrapeGauges(t, MetricWatchedEntities, WithWatchedEntities(WatchedEntitiesFromStore(st)))
	want := map[string]int64{"issue/bd-x": 2, "issue/": 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("series = %v, want %v", got, want)
	}
}

func TestWatchedEntitiesWithNoneWatchedIsOneZeroIssueSeries(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	got := scrapeGauges(t, MetricWatchedEntities, WithWatchedEntities(WatchedEntitiesFromStore(st)))
	if want := map[string]int64{"issue/": 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("series = %v, want %v", got, want)
	}
}

func TestOrphanSeriesAreAbsentWithoutTheirOptions(t *testing.T) {
	if got := scrapeGauges(t, MetricWatchedEntities); len(got) != 0 {
		t.Errorf("watched series without option: %v", got)
	}
	if got := scrapeGauges(t, MetricOldestOrphanReadAge); len(got) != 0 {
		t.Errorf("orphan series without option: %v", got)
	}
}
