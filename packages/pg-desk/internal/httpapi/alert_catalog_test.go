package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/lanestate"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// The metrics catalog check (PR lane refactor, P8a-1): every pg-desk or pg-decider
// metric that an alert rule of grafana/alerting/alerts.yaml names MUST be emitted
// by a real scrape of a schema-2 store, and the lane series by the composed
// command's own write path. A rule that reads a name nothing emits is silent
// forever, which is the failure that let the 2026-10-19 outage read healthy.

// alertRuleFile is the shape of the parts of alerts.yaml this check reads.
type alertRuleFile struct {
	Groups []struct {
		Rules []struct {
			UID  string `yaml:"uid"`
			Data []struct {
				RefID string `yaml:"refId"`
				Model struct {
					Expr string `yaml:"expr"`
				} `yaml:"model"`
			} `yaml:"data"`
		} `yaml:"rules"`
	} `yaml:"groups"`
}

// namedMetrics returns, for every rule, the pg_desk_ and pg_decider_ metric names
// its refId A expression names (other services' series are out of scope here:
// each service's own catalog check owns them).
func namedMetrics(t *testing.T) map[string][]string {
	t.Helper()
	b, err := os.ReadFile("../../grafana/alerting/alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var f alertRuleFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`\bpg_(?:desk|decider)_[a-z0-9_]+`)
	out := map[string][]string{}
	for _, g := range f.Groups {
		for _, r := range g.Rules {
			for _, d := range r.Data {
				if d.RefID != "A" {
					continue
				}
				seen := map[string]bool{}
				for _, m := range re.FindAllString(d.Model.Expr, -1) {
					if !seen[m] {
						seen[m] = true
						out[r.UID] = append(out[r.UID], m)
					}
				}
			}
		}
	}
	if len(out) < 20 {
		t.Fatalf("read only %d rules from alerts.yaml; the parse is wrong", len(out))
	}
	return out
}

func scrapeWith(t *testing.T, s *store.Store, opts ...Option) string {
	t.Helper()
	handler, err := NewHandler(s, focusConfig(), opts...)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	return rr.Body.String()
}

func emitted(body, name string) bool {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, name+"{") || strings.HasPrefix(line, name+" ") {
			return true
		}
	}
	return false
}

// TestEveryMetricNamedByAnAlertIsEmittedByARealScrape scrapes the real serve
// handler over a schema-2 store seeded with the focus data and a supplied router
// poll interval (the two inputs that gate the conditional families), and
// requires every name an alert rule reads to appear.
func TestEveryMetricNamedByAnAlertIsEmittedByARealScrape(t *testing.T) {
	s := seedFocusStore(t)
	// A consumer's lag is emitted per registered consumer (the focus role's
	// issue consumer registers on its first read).
	if err := s.RegisterConsumer("focus", "issue", time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	body := scrapeWith(t, s, WithPollInterval(func(string) (time.Duration, bool) { return time.Minute, true }))
	var missing []string
	for uid, names := range namedMetrics(t) {
		for _, n := range names {
			if !emitted(body, n) {
				missing = append(missing, uid+" reads "+n)
			}
		}
	}
	sort.Strings(missing)
	if len(missing) != 0 {
		t.Errorf("alert rules read metrics a real scrape does not emit:\n  %s", strings.Join(missing, "\n  "))
	}
}

// TestTheCatalogCheckRejectsAMetricNothingEmits is the negative control: the
// check's own emitted() reports a name a scrape lacks as missing.
func TestTheCatalogCheckRejectsAMetricNothingEmits(t *testing.T) {
	body := scrapeWith(t, seedFocusStore(t))
	if emitted(body, "pg_desk_pr_lane_no_such_metric") {
		t.Fatal("emitted() reports a metric that does not exist")
	}
	if !emitted(body, "pg_desk_liveness") {
		t.Fatal("emitted() misses a metric that does exist")
	}
}

// TestTheLaneSeriesAnAlertReadsAreWrittenByTheComposedCommandsPath records
// a run through lanestate.Record, the function `pg-desk pr lane-record` (the
// composed command's only write path) calls, and requires every lane series an
// alert reads to move: the run counter by stage and class, the residual by mode
// and the decisions counter.
func TestTheLaneSeriesAnAlertReadsAreWrittenByTheComposedCommandsPath(t *testing.T) {
	s := seedFocusStore(t)
	cfg := focusConfig()
	cfg.PRLane.Mode = "apply"
	resid := 2
	if err := lanestate.Record(s, "apply", time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC), lanestate.RunInput{
		PR: "o/r#1", Mode: "apply", Residual: &resid,
		Events:    []lanestate.RunEvent{{Stage: "hydrate", Class: "unauthenticated"}},
		Decisions: []lanestate.DecisionCount{{Rule: "all.closed", Action: "close", Outcome: "applied"}},
	}); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(s, cfg)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	requireLines(
		t, rr.Body.String(),
		`pg_desk_pr_lane_runs_total{class="unauthenticated",stage="hydrate"} 1`,
		`pg_desk_pr_lane_residual_prs{mode="apply"} 1`,
		`pg_decider_decisions_total{action="close",outcome="applied",reason="",rule="all.closed"} 1`,
	)
	if age := rr.Body.String(); !strings.Contains(age, `pg_desk_pr_lane_residual_oldest_age_seconds{mode="apply"}`) {
		t.Errorf("the residual age gauge is not emitted for mode apply")
	}
}
