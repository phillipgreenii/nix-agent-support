package alertrules

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/freshness"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// wantSourcesExpr MUST equal the expr of rule pg-desk-sources-stale. It reuses
// the `unless on()` router pause suppression of pg-desk-sync-stale, so
// evalStaleRule models it too.
const wantSourcesExpr = `pg_desk_sources_stale unless on() (pg_router_resource_level{resource="router"} == 1)`

func sourcesRuleBlock(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../grafana/alerting/alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "- uid: pg-desk-sources-stale")
	if i < 0 {
		t.Fatal("rule pg-desk-sources-stale not found")
	}
	rest := src[i+1:]
	if j := strings.Index(rest, "- uid:"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

func TestSourcesStaleRuleExprMatchesModel(t *testing.T) {
	rest := sourcesRuleBlock(t)
	m := regexp.MustCompile(`(?m)^\s*expr: (.+)$`).FindStringSubmatch(rest)
	if m == nil {
		t.Fatal("no expr in rule")
	}
	if m[1] != wantSourcesExpr {
		t.Errorf("rule expr drifted from the modelled one:\n got %q\nwant %q", m[1], wantSourcesExpr)
	}
	for _, need := range []string{"for: 10m", "noDataState: OK", "execErrState: Error", "severity: warning", "type: gte", "params: [1]", "router pause"} {
		if !strings.Contains(rest, need) {
			t.Errorf("rule lost %q", need)
		}
	}
}

// staleSourcesSeries is pg_desk_sources_stale as pg-desk exports it: the count
// of stale rows in the REAL freshness report built from the given ledger rows.
func staleSourcesSeries(t *testing.T, cfg *config.Config, now time.Time, rows ...freshness.LedgerRow) []series {
	t.Helper()
	st := store.OpenForTest(t)
	if err := freshness.Update(st, rows); err != nil {
		t.Fatal(err)
	}
	rep, err := freshness.Build(st, cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range rep.Sources {
		if r.Stale {
			n++
		}
	}
	return []series{{"pg-desk", float64(n)}}
}

func ledger(backend string, success, seen *time.Time) freshness.LedgerRow {
	r := freshness.LedgerRow{Type: "pr", Backend: backend, Query: "mine", RefreshedAt: success}
	if seen != nil {
		r.Consumers = map[string]freshness.LedgerConsumer{"pg-router": {LastSeen: seen}}
	}
	return r
}

func TestOneLiveSourceForcedStaleFires(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { u := now.Add(d); return &u }
	cfg := &config.Config{}
	stale := staleSourcesSeries(
		t, cfg, now,
		ledger("pg-connector-issue-jira", at(-time.Minute), at(-time.Minute)),
		ledger("pg-connector-pr-github", at(-2*time.Hour), at(-time.Minute)), // polled, last success 2h ago
	)
	if !firing(evalStaleRule(stale, nil)) {
		t.Fatalf("one stale live source did not fire (series %v)", stale)
	}
	// A live source that never succeeded is stale too, though it has no age series.
	never := staleSourcesSeries(t, cfg, now, ledger("pg-connector-thread-slack", nil, at(-time.Minute)))
	if !firing(evalStaleRule(never, nil)) {
		t.Fatalf("a live never-succeeded source did not fire (series %v)", never)
	}
	if firing(evalStaleRule(stale, []series{{"pg-router", 1}})) {
		t.Fatal("fired during a router pause")
	}
}

func TestOnlyRetiredGhostRowsDoNotFire(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { u := now.Add(d); return &u }
	ghostSeen := at(-freshness.AbandonedAfter - time.Hour)
	cfg := &config.Config{Freshness: config.FreshnessConfig{RetiredSources: []string{"pr-github"}}}
	got := staleSourcesSeries(
		t, cfg, now,
		ledger("pg-connector-pr-github", nil, ghostSeen),
		ledger("pg-connector-github", at(-time.Minute), at(-time.Minute)),
	)
	if firing(evalStaleRule(got, nil)) {
		t.Fatalf("retired ghost rows fired (series %v)", got)
	}
}
