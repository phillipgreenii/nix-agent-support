package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
