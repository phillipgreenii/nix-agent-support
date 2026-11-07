package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Subject: the body's Context: section (dashboard link, rule annotations,
// current value, recent logs, related open beads) for a grafana alert.

func TestRenderBodyAlertContextHasAllFiveItems(t *testing.T) {
	f := finding{
		Kind: kindGrafanaAlert, Fingerprint: "fp1", RuleUID: "rule-a", Summary: "s", Evidence: "e",
		DashboardURL: "https://grafana.example/d/dash1?viewPanel=4",
		Annotations:  map[string]string{"summary": "queue stalled", "runbook_url": "https://rb/x", "logql": `{app="pg-router"}`},
		Values:       "B=0.5, C=1",
		Context: alertContext{
			LogQuery: `{app="pg-router"}`,
			Logs:     []string{"line one", "line two"},
			Related:  []string{"zr-7", "zr-9"},
		},
	}
	body := renderBody(f, time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC), "", "")
	for _, want := range []string{
		"Dashboard: https://grafana.example/d/dash1?viewPanel=4",
		"  runbook_url: https://rb/x\n",
		"  summary: queue stalled\n",
		"Current value: B=0.5, C=1",
		"Log query: {app=\"pg-router\"}",
		"Recent logs (last 2):\n  line one\n  line two\n",
		"Related open beads (same rule uid): zr-7, zr-9",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
}

func TestRenderBodyAlertContextDegradesVisibly(t *testing.T) {
	f := finding{
		Kind: kindGrafanaAlert, Fingerprint: "fp1", Summary: "s", Evidence: "e",
		Context: alertContext{LogsNote: "log fetch failed: boom"},
	}
	body := renderBody(f, time.Now(), "", "")
	for _, want := range []string{"(none)", "Current value: unavailable", "Recent logs: log fetch failed: boom", "Related open beads (same rule uid): none"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
}

func TestRenderBodyNoContextForNonAlert(t *testing.T) {
	f := finding{Kind: kindBinaryHashMismatch, Fingerprint: "fp1", Summary: "s", Evidence: "e"}
	if body := renderBody(f, time.Now(), "", ""); strings.Contains(body, "Context:") {
		t.Fatalf("unexpected Context in %q", body)
	}
}

func TestRelatedBeadsMatchesRuleUIDOnly(t *testing.T) {
	f := finding{Kind: kindGrafanaAlert, Fingerprint: "fp", RuleUID: "r1"}
	existing := []connectorIssue{
		{ID: "b2", Metadata: map[string]string{metaRuleUID: "r1", metaFingerprint: "other"}},
		{ID: "b1", Metadata: map[string]string{metaRuleUID: "r1", metaFingerprint: "x"}},
		{ID: "b3", Metadata: map[string]string{metaRuleUID: "r2"}},
		{ID: "self", Metadata: map[string]string{metaRuleUID: "r1", metaFingerprint: "fp"}},
	}
	got := relatedBeads(f, existing)
	if strings.Join(got, ",") != "b1,b2" {
		t.Fatalf("got %v", got)
	}
}

func TestBuildAlertContextLogPaths(t *testing.T) {
	f := finding{RuleUID: "r", Annotations: map[string]string{"logql": "q"}}
	ok := buildAlertContext(context.Background(), f, nil, func(context.Context, string) ([]string, error) { return []string{"a"}, nil })
	if len(ok.Logs) != 1 || ok.LogQuery != "q" {
		t.Fatalf("got %+v", ok)
	}
	bad := buildAlertContext(context.Background(), f, nil, func(context.Context, string) ([]string, error) { return nil, errors.New("boom") })
	if !strings.Contains(bad.LogsNote, "boom") {
		t.Fatalf("got %+v", bad)
	}
	none := buildAlertContext(context.Background(), f, nil, nil)
	if !strings.Contains(none.LogsNote, "--loki-url") {
		t.Fatalf("got %+v", none)
	}
	noq := buildAlertContext(context.Background(), finding{}, nil, nil)
	if !strings.Contains(noq.LogsNote, "logql") {
		t.Fatalf("got %+v", noq)
	}
}

func TestDashboardURLPreference(t *testing.T) {
	if got := dashboardURL("http://g/", "r", "http://gen", map[string]string{"__dashboardUid__": "d1", "__panelId__": "3"}); got != "http://g/d/d1?viewPanel=3" {
		t.Fatal(got)
	}
	if got := dashboardURL("http://g", "r", "http://gen", nil); got != "http://gen" {
		t.Fatal(got)
	}
	if got := dashboardURL("http://g", "r", "", nil); got != "http://g/alerting/grafana/r/view" {
		t.Fatal(got)
	}
}

func TestFiringAlertsCarriesAnnotationsAndDashboard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"labels":      map[string]string{"__alert_rule_uid__": "pg-router-liveness-down"},
			"annotations": map[string]string{"summary": "down", "runbook_url": "rb", "__values__": `{"B":1}`, "__dashboardUid__": "d9"},
			"status":      map[string]string{"state": "active"},
		}})
	}))
	defer srv.Close()
	alerts, err := newGrafanaClient(srv.URL, "", srv.Client()).firingAlerts(context.Background(), []string{"pg-router-liveness-down"})
	if err != nil || len(alerts) != 1 {
		t.Fatalf("%v %v", alerts, err)
	}
	a := alerts[0]
	if a.Annotations["runbook_url"] != "rb" || a.Annotations["summary"] != "down" || len(a.Annotations) != 2 {
		t.Fatalf("annotations %v", a.Annotations)
	}
	if a.DashboardURL != srv.URL+"/d/d9" {
		t.Fatalf("dashboard %q", a.DashboardURL)
	}
}

func TestLokiRecentLinesNewestFirstAndLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/api/v1/query_range" || r.URL.Query().Get("query") != "q" || r.URL.Query().Get("limit") != "2" {
			t.Errorf("bad request %s", r.URL)
		}
		_, _ = w.Write([]byte(`{"data":{"result":[{"values":[["1","old"],["3","new"]]},{"values":[["2","mid"]]}]}}`))
	}))
	defer srv.Close()
	c := &lokiClient{baseURL: srv.URL, httpClient: srv.Client()}
	got, err := c.recentLines(context.Background(), "q", 2, time.Now())
	if err != nil || strings.Join(got, ",") != "new,mid" {
		t.Fatalf("%v %v", got, err)
	}
}

func TestRunProbeCreatedBeadBodyCarriesContext(t *testing.T) {
	cmd, _ := testCmd()
	opts := baseOpts(t)
	opts.grafanaURL = "http://example.invalid"
	opts.lokiURL = "http://loki.invalid"
	spy := &spyDeps{
		fetchAlertsFn: func(ctx context.Context, opts runOptions) ([]grafanaAlert, error) {
			return []grafanaAlert{{
				RuleUID: "pg-router-liveness-down", Labels: map[string]string{"instance": "a"}, State: "active",
				Values: "B=1", Annotations: map[string]string{"logql": "{x=\"y\"}", "summary": "down"},
				DashboardURL: "http://example.invalid/d/abc",
			}}, nil
		},
		listEscalatedResult: []connectorIssue{{ID: "zr-5", Metadata: map[string]string{metaRuleUID: "pg-router-liveness-down", metaFingerprint: "z"}}},
	}
	deps := spy.toRunDeps(time.Now())
	deps.fetchLogs = func(ctx context.Context, o runOptions, q string) ([]string, error) {
		if q != `{x="y"}` {
			t.Errorf("query %q", q)
		}
		return []string{"log-a"}, nil
	}
	if err := runProbe(cmd, opts, deps); err != nil {
		t.Fatal(err)
	}
	if len(spy.created) != 1 {
		t.Fatalf("created %d", len(spy.created))
	}
	body := spy.created[0].body
	for _, want := range []string{"Dashboard: http://example.invalid/d/abc", "summary: down", "Current value: B=1", "  log-a", "zr-5"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if spy.created[0].metadata[metaRuleUID] != "pg-router-liveness-down" {
		t.Fatalf("metadata %v", spy.created[0].metadata)
	}
}
