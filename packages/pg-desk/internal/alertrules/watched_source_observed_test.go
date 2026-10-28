package alertrules

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/metrics"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// outcomeGuard is the liveness guard every outcome rule of an
// owning backend ends with, copied from pg-desk-ingest-failed-old.
const outcomeGuard = ` or (vector(0) and on() absent(pg_desk_liveness == 1))`

// wantWatchedSourceExpr MUST equal the expr of rule
// pg-desk-watched-source-not-observed (the approach of sweep_bound_test.go).
const wantWatchedSourceExpr = `max by (backend) (pg_desk_watched_source_observed_age_seconds)` + outcomeGuard

// watchedSourceBoundSeconds is the rule's fixed threshold: one day.
const watchedSourceBoundSeconds = 86400

// outcomeSample is one sample of an outcome series.
type outcomeSample struct {
	job, backend string
	v            float64
}

// outcomeResult is what Grafana sees after the expression: per-backend values
// (backend "" being the label-less series of the pg-desk-down branch) or NoData.
type outcomeResult struct {
	values map[string]float64
	noData bool
}

// evalOutcomeExpr models `max by (backend) (series) or (vector(0) and on()
// absent(pg_desk_liveness == 1))`: the left side is one element per backend at
// the max over its series; the right side answers a label-less 0 only while no
// liveness series reads 1 (pg-desk down). Liveness up and no series leaves
// both sides empty: NoData.
func evalOutcomeExpr(series []outcomeSample, livenessUp bool) outcomeResult {
	left := map[string]float64{}
	for _, s := range series {
		if cur, ok := left[s.backend]; !ok || s.v > cur {
			left[s.backend] = s.v
		}
	}
	if len(left) > 0 {
		return outcomeResult{values: left}
	}
	if !livenessUp {
		return outcomeResult{values: map[string]float64{"": 0}}
	}
	return outcomeResult{noData: true}
}

// outcomeFiring models reduce(last) + threshold(gt bound) and the rule's
// noDataState.
func outcomeFiring(o outcomeResult, bound float64, noDataState string) (backends []string, noDataAlerts bool) {
	if o.noData {
		return nil, noDataState == "Alerting"
	}
	for b, v := range o.values {
		if v > bound {
			backends = append(backends, b)
		}
	}
	return backends, false
}

func outcomeRuleBlock(t *testing.T, uid string) string {
	t.Helper()
	b, err := os.ReadFile("../../grafana/alerting/alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "- uid: "+uid+"\n")
	if i < 0 {
		t.Fatalf("rule %s not found", uid)
	}
	rest := src[i+1:]
	if j := strings.Index(rest, "- uid:"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

func outcomeNoDataState(t *testing.T, uid string) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^\s*noDataState: (\S+)$`).FindStringSubmatch(outcomeRuleBlock(t, uid))
	if m == nil {
		t.Fatal("no noDataState in rule")
	}
	return m[1]
}

// pinOutcomeRule asserts the rule's expression text equals the modelled one and
// that it keeps its fixed Grafana fields.
func pinOutcomeRule(t *testing.T, uid, wantExpr, series, threshold string) {
	t.Helper()
	rest := outcomeRuleBlock(t, uid)
	m := regexp.MustCompile(`(?m)^\s*expr: (.+)$`).FindStringSubmatch(rest)
	if m == nil {
		t.Fatal("no expr in rule")
	}
	if m[1] != wantExpr {
		t.Errorf("rule expr drifted from the modelled one:\n got %q\nwant %q", m[1], wantExpr)
	}
	for _, need := range []string{"for: 10m", "noDataState: Alerting", "execErrState: Error", "severity: warning", "type: gt", "params: [" + threshold + "]"} {
		if !strings.Contains(rest, need) {
			t.Errorf("rule lost %q", need)
		}
	}
	// A rule that read process liveness only would never see the outcome.
	if !strings.Contains(m[1], series) {
		t.Errorf("the expression does not read %s", series)
	}
}

// emittedOutcome drives the SAME metrics code /metrics uses over a real store
// and returns the samples of one outcome series.
func emittedOutcome(t *testing.T, name string, opt metrics.ChangeFlowOption) []outcomeSample {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	flow := func() (changes.Flow, error) { return changes.Flow{Migrated: true}, nil }
	if err := metrics.RegisterChangeFlow(mp, flow, opt); err != nil {
		t.Fatal(err)
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	var out []outcomeSample
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			for _, p := range m.Data.(metricdata.Gauge[int64]).DataPoints {
				be, _ := p.Attributes.Value("backend")
				out = append(out, outcomeSample{"pg-desk", be.AsString(), float64(p.Value)})
			}
		}
	}
	return out
}

func TestWatchedSourceNotObservedRuleExprMatchesModel(t *testing.T) {
	pinOutcomeRule(t, "pg-desk-watched-source-not-observed", wantWatchedSourceExpr, "pg_desk_watched_source_observed_age_seconds", "86400")
}

func TestWatchedSourceNotObservedFiresAboveADayAndClears(t *testing.T) {
	const uid = "pg-desk-watched-source-not-observed"
	nd := outcomeNoDataState(t, uid)
	old := []outcomeSample{{"pg-desk", "jira-a", 100000}, {"pg-desk-legacy", "jira-a", 100000}, {"pg-desk", "jira-b", 43200}}
	got, _ := outcomeFiring(evalOutcomeExpr(old, true), watchedSourceBoundSeconds, nd)
	if len(got) != 1 || got[0] != "jira-a" {
		t.Errorf("firing = %v, want exactly [jira-a] (the duplicate job series is ONE instance)", got)
	}
	if got, _ := outcomeFiring(evalOutcomeExpr([]outcomeSample{{"pg-desk", "jira-a", 86400}}, true), watchedSourceBoundSeconds, nd); len(got) != 0 {
		t.Errorf("age == one day fired %v", got)
	}
	if got, _ := outcomeFiring(evalOutcomeExpr([]outcomeSample{{"pg-desk", "jira-a", 86401}}, true), watchedSourceBoundSeconds, nd); len(got) != 1 {
		t.Errorf("age == one day + 1s fired %v, want one instance", got)
	}
	// The source is observed again: the age drops and the alert clears.
	got, noData := outcomeFiring(evalOutcomeExpr([]outcomeSample{{"pg-desk", "jira-a", 30}}, true), watchedSourceBoundSeconds, nd)
	if len(got) != 0 || noData {
		t.Errorf("a fresh observation fired %v (noData alert %v)", got, noData)
	}
}

func TestWatchedSourceNotObservedNoDataAlertsOnlyWhilePgDeskIsUp(t *testing.T) {
	nd := outcomeNoDataState(t, "pg-desk-watched-source-not-observed")
	if nd != "Alerting" {
		t.Fatalf("noDataState = %q, want Alerting", nd)
	}
	if _, noData := outcomeFiring(evalOutcomeExpr(nil, true), watchedSourceBoundSeconds, nd); !noData {
		t.Error("liveness up and no series did not alert")
	}
	got, noData := outcomeFiring(evalOutcomeExpr(nil, false), watchedSourceBoundSeconds, nd)
	if len(got) != 0 || noData {
		t.Errorf("pg-desk down: fired %v (noData alert %v), want quiet", got, noData)
	}
}

func watchedSourceOption(st *store.Store, now time.Time, queries map[string][]string) metrics.ChangeFlowOption {
	return metrics.WithWatchedSourceObserved(metrics.WatchedSourceObservedFromStore(st, queries, func() time.Time { return now }))
}

// TestWatchedSourceNotObservedRuleAgreesWithTheEmittedSeries feeds the model
// with what the gauge really emits for the stored observations: the OLDEST
// query of the instance decides, and a fresh observation clears the alert.
func TestWatchedSourceNotObservedRuleAgreesWithTheEmittedSeries(t *testing.T) {
	nd := outcomeNoDataState(t, "pg-desk-watched-source-not-observed")
	st := store.OpenNewSchemaForTest(t)
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	queries := map[string][]string{"jira-a": {"mine", "team"}}
	set := func(q string, at time.Time) {
		t.Helper()
		if err := st.SetMeta(changes.MetaFeedObservedPrefix+"jira-a."+q, at.Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	set("mine", now.Add(-1*time.Minute))
	set("team", now.Add(-25*time.Hour))
	series := emittedOutcome(t, metrics.MetricWatchedSourceObservedAge, watchedSourceOption(st, now, queries))
	if len(series) != 1 || series[0].v != 25*3600 {
		t.Fatalf("series = %+v, want one jira-a sample at %d", series, 25*3600)
	}
	if got, _ := outcomeFiring(evalOutcomeExpr(series, true), watchedSourceBoundSeconds, nd); len(got) != 1 || got[0] != "jira-a" {
		t.Errorf("a 25 hour old query: firing = %v, want [jira-a]", got)
	}
	set("team", now.Add(-23*time.Hour))
	series = emittedOutcome(t, metrics.MetricWatchedSourceObservedAge, watchedSourceOption(st, now, queries))
	if got, _ := outcomeFiring(evalOutcomeExpr(series, true), watchedSourceBoundSeconds, nd); len(got) != 0 {
		t.Errorf("a 23 hour old query fired %v", got)
	}
	// A fresh deployment with no observation yet never pages.
	empty := store.OpenNewSchemaForTest(t)
	series = emittedOutcome(t, metrics.MetricWatchedSourceObservedAge, watchedSourceOption(empty, now, queries))
	if len(series) != 1 || series[0].v != 0 {
		t.Fatalf("no observation must read 0, got %+v", series)
	}
	if got, noData := outcomeFiring(evalOutcomeExpr(series, true), watchedSourceBoundSeconds, nd); len(got) != 0 || noData {
		t.Errorf("a fresh deployment fired %v (noData alert %v)", got, noData)
	}
}

// TestWatchedSourceNotObservedUIDIsRegisteredWithTheRouterProbe pins the
// registration in the probe's default rule list.
func TestWatchedSourceNotObservedUIDIsRegisteredWithTheRouterProbe(t *testing.T) {
	assertProbeRegistered(t, "pg-desk-watched-source-not-observed")
}

func assertProbeRegistered(t *testing.T, uid string) {
	t.Helper()
	b, err := os.ReadFile("../../../pg-router-probe/cmd/pg-router-probe/run.go")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^\t"` + regexp.QuoteMeta(uid) + `",$`).Match(b) {
		t.Errorf("%s is not listed in registeredRuleUIDs", uid)
	}
}

// TestBeadsWatchedSourceNotObservedRuleAgreesWithTheEmittedSeries feeds the
// model with what the gauge emits for a beads instance: a listing_ok two days
// old fires, an orphan read 30 minutes ago does not hide it (the oldest source
// decides), a fresh listing clears it, and a deployment that never listed
// reads 0 and never pages.
func TestBeadsWatchedSourceNotObservedRuleAgreesWithTheEmittedSeries(t *testing.T) {
	nd := outcomeNoDataState(t, "pg-desk-watched-source-not-observed")
	st := store.OpenNewSchemaForTest(t)
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	cfg := &config.Config{Repos: []config.RepoConfig{{Remote: "o/r"}}}
	cfg.Watch.Issue.Queries = []string{"open"}
	cfg.Watch.Issue.Backends = map[string]config.WatchBackend{"open": {Instance: "beads-a"}}
	opt := func(s *store.Store) metrics.ChangeFlowOption {
		return metrics.WithWatchedSourceObserved(metrics.WatchedSourceObservedForConfig(s, cfg, func() time.Time { return now }))
	}
	if err := st.SetMeta(changes.MetaListingOKPrefix+"open", now.Add(-48*time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	fp, yes := "F1", true
	if _, err := st.WriteEntityStateWithLogFP(store.Entity{Repo: "o/r", EntityType: "issue", EntityID: "bd-o", Facts: `{}`, AsOf: "x"},
		0, now.Add(-time.Hour).Format(time.RFC3339), true, &fp, []string{"reconcile"}, "sweep", now.Add(-time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	backend, okAt := "beads-a", now.Add(-30*time.Minute).Format(time.RFC3339Nano)
	if err := st.SetEntityColumns("o/r", "issue", "bd-o", &backend, &yes); err != nil {
		t.Fatal(err)
	}
	if err := st.SetEntityReadTimes("o/r", "issue", "bd-o", nil, &okAt); err != nil {
		t.Fatal(err)
	}
	series := emittedOutcome(t, metrics.MetricWatchedSourceObservedAge, opt(st))
	if len(series) != 1 || series[0].backend != "beads-a" || series[0].v != 48*3600 {
		t.Fatalf("series = %+v, want one beads-a sample at %d", series, 48*3600)
	}
	if got, _ := outcomeFiring(evalOutcomeExpr(series, true), watchedSourceBoundSeconds, nd); len(got) != 1 || got[0] != "beads-a" {
		t.Errorf("a two day old listing: firing = %v, want [beads-a]", got)
	}
	if err := st.SetMeta(changes.MetaListingOKPrefix+"open", now.Add(-time.Minute).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	series = emittedOutcome(t, metrics.MetricWatchedSourceObservedAge, opt(st))
	if len(series) != 1 || series[0].v != 30*60 {
		t.Fatalf("series = %+v, want the orphan's 30 minutes", series)
	}
	if got, noData := outcomeFiring(evalOutcomeExpr(series, true), watchedSourceBoundSeconds, nd); len(got) != 0 || noData {
		t.Errorf("a fresh listing fired %v (noData alert %v)", got, noData)
	}
	fresh := emittedOutcome(t, metrics.MetricWatchedSourceObservedAge, opt(store.OpenNewSchemaForTest(t)))
	if len(fresh) != 1 || fresh[0].v != 0 {
		t.Fatalf("a deployment that never listed must read 0, got %+v", fresh)
	}
	if got, noData := outcomeFiring(evalOutcomeExpr(fresh, true), watchedSourceBoundSeconds, nd); len(got) != 0 || noData {
		t.Errorf("a fresh deployment fired %v (noData alert %v)", got, noData)
	}
}
