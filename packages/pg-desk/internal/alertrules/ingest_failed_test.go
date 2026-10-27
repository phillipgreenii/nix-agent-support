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
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/metrics"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// wantIngestFailedExpr MUST equal the expr of rule pg-desk-ingest-failed-old.
// The semantics are modelled below and the YAML is pinned to this exact text
// (the approach of sweep_bound_test.go).
const wantIngestFailedExpr = `max by (backend) (pg_desk_ingest_failed_oldest_age_seconds) or (vector(0) and on() absent(pg_desk_liveness == 1))`

// ingestFailedBoundSeconds is the rule's FIXED threshold: the documented
// default (30 minutes) of hydration.ingest_failed_max_age, which governs
// doctor only.
const ingestFailedBoundSeconds = 1800

// ageSample is one sample of pg_desk_ingest_failed_oldest_age_seconds.
type ageSample struct {
	job, backend string
	v            float64
}

// ingestOutcome is what Grafana sees after the expression: the per-backend
// values (the backend "" being the label-less series of the pg-desk-down
// branch) or NoData.
type ingestOutcome struct {
	values map[string]float64
	noData bool
}

// evalIngestFailedExpr models
// `max by (backend) (age) or (vector(0) and on() absent(liveness == 1))`:
// the left side is one element per backend at the max over its series; the
// right side answers a label-less 0 only while NO liveness series reads 1
// (pg-desk down); `or` keeps the right side only where the left has no element
// with the same (empty) label set. Liveness up and no age series leaves both
// sides empty: NoData.
func evalIngestFailedExpr(age []ageSample, livenessUp bool) ingestOutcome {
	left := map[string]float64{}
	for _, s := range age {
		if cur, ok := left[s.backend]; !ok || s.v > cur {
			left[s.backend] = s.v
		}
	}
	if len(left) > 0 {
		return ingestOutcome{values: left}
	}
	if !livenessUp {
		return ingestOutcome{values: map[string]float64{"": 0}}
	}
	return ingestOutcome{noData: true}
}

// ingestFailedFiring models reduce(last) + threshold(gt 1800) and the rule's
// noDataState: the backends over the bound, plus a NoData instance when
// noDataState is Alerting.
func ingestFailedFiring(o ingestOutcome, noDataState string) (backends []string, noDataAlerts bool) {
	if o.noData {
		return nil, noDataState == "Alerting"
	}
	for b, v := range o.values {
		if v > ingestFailedBoundSeconds {
			backends = append(backends, b)
		}
	}
	return backends, false
}

func ingestFailedRuleBlock(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../grafana/alerting/alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "- uid: pg-desk-ingest-failed-old")
	if i < 0 {
		t.Fatal("rule pg-desk-ingest-failed-old not found")
	}
	rest := src[i+1:]
	if j := strings.Index(rest, "- uid:"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

func ingestFailedNoDataState(t *testing.T) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^\s*noDataState: (\S+)$`).FindStringSubmatch(ingestFailedRuleBlock(t))
	if m == nil {
		t.Fatal("no noDataState in rule")
	}
	return m[1]
}

func TestIngestFailedRuleExprMatchesModel(t *testing.T) {
	rest := ingestFailedRuleBlock(t)
	m := regexp.MustCompile(`(?m)^\s*expr: (.+)$`).FindStringSubmatch(rest)
	if m == nil {
		t.Fatal("no expr in rule")
	}
	if m[1] != wantIngestFailedExpr {
		t.Errorf("rule expr drifted from the modelled one:\n got %q\nwant %q", m[1], wantIngestFailedExpr)
	}
	for _, need := range []string{"for: 10m", "noDataState: Alerting", "execErrState: Error", "severity: warning", "type: gt", "params: [1800]"} {
		if !strings.Contains(rest, need) {
			t.Errorf("rule lost %q", need)
		}
	}
	// A rule that read process liveness only (pg_desk_liveness without the
	// failed-ingest series) would never see an old entry.
	if !strings.Contains(m[1], "pg_desk_ingest_failed_oldest_age_seconds") {
		t.Error("the expression does not read the failed-ingest oldest-age series")
	}
}

func TestIngestFailedFiresAboveTheBoundAndClearsWhenTheTableEmpties(t *testing.T) {
	nd := ingestFailedNoDataState(t)
	old := []ageSample{{"pg-desk", "jira-a", 2400}, {"pg-desk-legacy", "jira-a", 2400}, {"pg-desk", "jira-b", 600}}
	got, _ := ingestFailedFiring(evalIngestFailedExpr(old, true), nd)
	if len(got) != 1 || got[0] != "jira-a" {
		t.Errorf("firing = %v, want exactly [jira-a] (the duplicate job series is ONE instance)", got)
	}
	// Exactly the bound is quiet, one second over fires.
	if got, _ := ingestFailedFiring(evalIngestFailedExpr([]ageSample{{"pg-desk", "jira-a", 1800}}, true), nd); len(got) != 0 {
		t.Errorf("age == bound fired %v", got)
	}
	if got, _ := ingestFailedFiring(evalIngestFailedExpr([]ageSample{{"pg-desk", "jira-a", 1801}}, true), nd); len(got) != 1 {
		t.Errorf("age == bound+1 fired %v, want one instance", got)
	}
	// The table empties: the series reads 0 (never absent), nothing fires.
	got, noData := ingestFailedFiring(evalIngestFailedExpr([]ageSample{{"pg-desk", "jira-a", 0}}, true), nd)
	if len(got) != 0 || noData {
		t.Errorf("an empty table fired %v (noData alert %v)", got, noData)
	}
}

func TestIngestFailedNoDataAlertsOnlyWhilePgDeskIsUp(t *testing.T) {
	nd := ingestFailedNoDataState(t)
	if nd != "Alerting" {
		t.Fatalf("noDataState = %q, want Alerting", nd)
	}
	// pg-desk up, the series gone: the store cannot answer. NoData alerts.
	if _, noData := ingestFailedFiring(evalIngestFailedExpr(nil, true), nd); !noData {
		t.Error("liveness up and no ingest series did not alert")
	}
	// pg-desk down: liveness is gone as well. The guard answers 0, so this rule
	// stays quiet and pg-desk-liveness-down is the alert that speaks.
	got, noData := ingestFailedFiring(evalIngestFailedExpr(nil, false), nd)
	if len(got) != 0 || noData {
		t.Errorf("pg-desk down: fired %v (noData alert %v), want quiet", got, noData)
	}
}

// metricsAgeSeries drives the SAME metrics code /metrics uses and returns the
// pg_desk_ingest_failed_oldest_age_seconds samples as the model's input.
func metricsAgeSeries(t *testing.T, st *store.Store, owning []string, now time.Time) []ageSample {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	flow := func() (changes.Flow, error) { return changes.Flow{Migrated: true}, nil }
	opt := metrics.WithIngestFailed(metrics.IngestFailedFromStore(st, owning, func() time.Time { return now }))
	if err := metrics.RegisterChangeFlow(mp, flow, opt); err != nil {
		t.Fatal(err)
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	var out []ageSample
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != metrics.MetricIngestFailedOldestAge {
				continue
			}
			for _, p := range m.Data.(metricdata.Gauge[int64]).DataPoints {
				be, _ := p.Attributes.Value("backend")
				out = append(out, ageSample{"pg-desk", be.AsString(), float64(p.Value)})
			}
		}
	}
	return out
}

// TestIngestFailedRuleAgreesWithTheEmittedSeries feeds the model with what the
// gauge really emits for a real table: an entry first failed 31 minutes ago
// fires, and clearing it (the table empties) clears the alert.
func TestIngestFailedRuleAgreesWithTheEmittedSeries(t *testing.T) {
	nd := ingestFailedNoDataState(t)
	st := store.OpenNewSchemaForTest(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if err := st.RecordIngestFailure(store.IngestFailure{
		Instance: "jira-a", EntityID: "PROJ-1", Seq: 7, StoreIdentity: "s1",
		FirstFailedAt: now.Add(-31 * time.Minute).Format(time.RFC3339), LastError: "boom",
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := ingestFailedFiring(evalIngestFailedExpr(metricsAgeSeries(t, st, []string{"jira-a"}, now), true), nd)
	if len(got) != 1 || got[0] != "jira-a" {
		t.Errorf("a 31 minute old entry: firing = %v, want [jira-a]", got)
	}
	// 29 minutes old: below the bound.
	got, _ = ingestFailedFiring(evalIngestFailedExpr(metricsAgeSeries(t, st, []string{"jira-a"}, now.Add(-2*time.Minute)), true), nd)
	if len(got) != 0 {
		t.Errorf("a 29 minute old entry fired %v", got)
	}
	if err := st.ClearIngestFailure("jira-a", "PROJ-1", 7); err != nil {
		t.Fatal(err)
	}
	series := metricsAgeSeries(t, st, []string{"jira-a"}, now)
	if len(series) != 1 || series[0].v != 0 {
		t.Fatalf("the emptied table must still emit the owning instance at 0, got %+v", series)
	}
	got, noData := ingestFailedFiring(evalIngestFailedExpr(series, true), nd)
	if len(got) != 0 || noData {
		t.Errorf("the emptied table: firing %v, noData alert %v, want cleared", got, noData)
	}
}

// TestIngestFailedUIDIsRegisteredWithTheRouterProbe pins the registration: the
// probe's default rule list (registeredRuleUIDs in pg-router-probe) names the
// uid, so a firing instance reaches the escalation flow. The probe's own test
// pins the full list; this one fails here, in the module that owns the rule,
// when the uid is renamed or dropped on either side.
func TestIngestFailedUIDIsRegisteredWithTheRouterProbe(t *testing.T) {
	b, err := os.ReadFile("../../../pg-router-probe/cmd/pg-router-probe/run.go")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^\t"pg-desk-ingest-failed-old",$`).Match(b) {
		t.Error("pg-desk-ingest-failed-old is not listed in registeredRuleUIDs")
	}
}
