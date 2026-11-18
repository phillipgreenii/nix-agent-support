package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFiringAlertsFiltersToRegisteredRuleUIDs(t *testing.T) {
	instances := []map[string]any{
		{
			"labels": map[string]string{"__alert_rule_uid__": "pg-router-liveness-down", "instance": "a"},
			"status": map[string]string{"state": "active"},
		},
		{
			// Not one of the registered rule UIDs -- must be dropped.
			"labels": map[string]string{"__alert_rule_uid__": "some-other-rule", "instance": "b"},
			"status": map[string]string{"state": "active"},
		},
		{
			// Registered rule UID, but not firing -- must be dropped.
			"labels": map[string]string{"__alert_rule_uid__": "pg-router-failure-rate", "instance": "c"},
			"status": map[string]string{"state": "suppressed"},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/alertmanager/grafana/api/v2/alerts" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(instances)
	}))
	defer srv.Close()

	client := newGrafanaClient(srv.URL, "", &http.Client{Timeout: 5 * time.Second})
	alerts, err := client.firingAlerts(context.Background(), []string{"pg-router-liveness-down", "pg-router-failure-rate"})
	if err != nil {
		t.Fatalf("firingAlerts: %v", err)
	}
	if len(alerts) != 1 {
		t.Fatalf("expected exactly 1 firing+registered alert, got %d: %+v", len(alerts), alerts)
	}
	if alerts[0].RuleUID != "pg-router-liveness-down" {
		t.Fatalf("got rule uid %q", alerts[0].RuleUID)
	}
}

// pg2-6k0l9: pg2-irowq split session-budget hard-stops into their own
// pg-router-budget-stops rule; the default registered set must let a firing
// instance of it through the Grafana filter, or the probe never sees it.
func TestRegisteredRuleUIDsPassBudgetStopsAlert(t *testing.T) {
	instances := []map[string]any{
		{
			"labels": map[string]string{"__alert_rule_uid__": "pg-router-budget-stops", "role": "review"},
			"status": map[string]string{"state": "active"},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(instances)
	}))
	defer srv.Close()

	client := newGrafanaClient(srv.URL, "", &http.Client{Timeout: 5 * time.Second})
	alerts, err := client.firingAlerts(context.Background(), registeredRuleUIDs)
	if err != nil {
		t.Fatalf("firingAlerts: %v", err)
	}
	if len(alerts) != 1 || alerts[0].RuleUID != "pg-router-budget-stops" {
		t.Fatalf("expected the budget-stops alert to pass the default filter, got %+v", alerts)
	}
	findings := checkGrafanaAlerts(alerts)
	if len(findings) != 1 || !strings.HasPrefix(findings[0].Fingerprint, "pg-router-budget-stops|") {
		t.Fatalf("unexpected findings: %+v", findings)
	}
}

func TestFiringAlertsNonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()

	client := newGrafanaClient(srv.URL, "", &http.Client{Timeout: 5 * time.Second})
	_, err := client.firingAlerts(context.Background(), []string{"x"})
	if err == nil {
		t.Fatalf("expected an error for a non-200 response")
	}
}

// TestFiringAlertsHonorsExplicitTimeout proves this client's own
// firingAlerts call respects an explicit context deadline rather than
// hanging indefinitely against a slow server -- the "every external call
// in run MUST carry an explicit timeout" binding decision, exercised at
// this client's own level (run.go's own context.WithTimeout is what
// actually derives the short-lived ctx in production; this test builds
// one directly to keep the test itself fast).
func TestFiringAlertsHonorsExplicitTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	// Close(), unlike a plain defer ordering, BLOCKS until every
	// outstanding request's handler returns -- so block must be closed
	// (unsticking the handler) BEFORE srv.Close() is called, or Close()
	// deadlocks forever waiting on the very handler this test is holding
	// open. Declaring close(block) as the LATER defer would run it AFTER
	// srv.Close() (defers unwind LIFO), which is backwards; call both
	// explicitly, in the right order, instead of relying on defer order.
	defer func() {
		close(block)
		srv.Close()
	}()

	client := newGrafanaClient(srv.URL, "", &http.Client{Timeout: 50 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := client.firingAlerts(ctx, []string{"x"})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("expected a timeout error")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("firingAlerts did not respect its timeout: took %v", elapsed)
	}
}

func TestFiringAlertsSendsBearerToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer srv.Close()

	client := newGrafanaClient(srv.URL, "s3cr3t", &http.Client{Timeout: 5 * time.Second})
	if _, err := client.firingAlerts(context.Background(), []string{"x"}); err != nil {
		t.Fatalf("firingAlerts: %v", err)
	}
	if gotAuth != "Bearer s3cr3t" {
		t.Fatalf("got Authorization header %q", gotAuth)
	}
}

func TestRenderGrafanaValues(t *testing.T) {
	for in, want := range map[string]string{
		"":                "",
		`{"B":1.5,"A":0}`: "A=0, B=1.5",
		`{"B":1e3}`:       "B=1e3",
		"[no data]":       "[no data]",
		`  {"C":2}  `:     "C=2",
		`null`:            "null",
	} {
		if got := renderGrafanaValues(in); got != want {
			t.Errorf("renderGrafanaValues(%q) = %q, want %q", in, got, want)
		}
	}
}

// pg2-3tt2e: startsAt and __values__ are decoded; a missing or malformed
// startsAt degrades to the zero time rather than failing the whole poll,
// and a suppressed instance is still ignored.
func TestFiringAlertsDecodesStartsAtAndValues(t *testing.T) {
	srv := newAlertServer(t, `[
	  {"labels":{"__alert_rule_uid__":"pg-router-liveness-down"},"startsAt":"2026-10-05T09:00:00.5Z","annotations":{"__values__":"{\"B\":2}"},"status":{"state":"active"}},
	  {"labels":{"__alert_rule_uid__":"pg-router-failure-rate","x":"1"},"startsAt":"garbage","status":{"state":"active"}},
	  {"labels":{"__alert_rule_uid__":"pg-router-budget-stops"},"startsAt":"2026-10-05T09:00:00Z","status":{"state":"suppressed"}}
	]`)
	defer srv.Close()
	client := newGrafanaClient(srv.URL, "", srv.Client())
	alerts, err := client.firingAlerts(context.Background(), registeredRuleUIDs)
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 2 {
		t.Fatalf("suppressed instance must be ignored, got %+v", alerts)
	}
	if want := time.Date(2026, 10, 5, 9, 0, 0, 500_000_000, time.UTC); !alerts[0].StartsAt.Equal(want) || alerts[0].Values != "B=2" {
		t.Fatalf("got StartsAt=%v Values=%q", alerts[0].StartsAt, alerts[0].Values)
	}
	if !alerts[1].StartsAt.IsZero() || alerts[1].Values != "" {
		t.Fatalf("malformed startsAt must decode to zero, got %+v", alerts[1])
	}
	if ev := grafanaAlertEvidence(alerts[0]); !strings.Contains(ev, "starts_at=2026-10-05T09:00:00Z") {
		t.Fatalf("evidence = %q", ev)
	}
	if ev := grafanaAlertEvidence(alerts[1]); !strings.Contains(ev, "starts_at=unknown") {
		t.Fatalf("evidence = %q", ev)
	}
}

// pg2-x7ie2: annotations.summary/description are decoded (trimmed) for the
// body's Remediation: section; missing ones decode empty.
func TestFiringAlertsDecodesSummaryAndDescription(t *testing.T) {
	srv := newAlertServer(t, `[
	  {"labels":{"__alert_rule_uid__":"pg-router-liveness-down"},"annotations":{"summary":"  s  ","description":"d"},"status":{"state":"active"}},
	  {"labels":{"__alert_rule_uid__":"pg-router-failure-rate"},"status":{"state":"active"}}
	]`)
	defer srv.Close()
	alerts, err := newGrafanaClient(srv.URL, "", srv.Client()).firingAlerts(context.Background(), registeredRuleUIDs)
	if err != nil || len(alerts) != 2 {
		t.Fatalf("firingAlerts: %v %+v", err, alerts)
	}
	if alerts[0].AnnotationSummary != "s" || alerts[0].AnnotationDescription != "d" {
		t.Fatalf("got %+v", alerts[0])
	}
	if alerts[1].AnnotationSummary != "" || alerts[1].AnnotationDescription != "" {
		t.Fatalf("got %+v", alerts[1])
	}
}
