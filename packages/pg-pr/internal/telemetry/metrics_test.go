package telemetry

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMetricsHandler_Smokes the package-level metrics endpoint by
// observing a value, scraping, and asserting the expected metric names
// appear in the OpenMetrics text output.
func TestMetricsHandler_ServesExpectedMetricNames(t *testing.T) {
	// Move the gauges/counters off zero so they show up in the scrape.
	SyncPRDuration.WithLabelValues("repo/a", "mine").Observe(0.123)
	SyncErrorsTotal.WithLabelValues("repo/a").Inc()
	FeedbackCreatedTotal.WithLabelValues("repo/a", "comment_thread").Inc()
	CIOnlyAttempts.WithLabelValues("repo/a", "42").Set(3)

	srv := httptest.NewServer(MetricsHandler())
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("/metrics status: got %d want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	text := string(body)
	wantNames := []string{
		"pg_pr_sync_pr_duration_seconds",
		"pg_pr_sync_errors_total",
		"pg_pr_feedback_created_total",
		"pg_pr_ci_only_attempts",
	}
	for _, name := range wantNames {
		if !strings.Contains(text, name) {
			t.Errorf("scrape missing metric %q\nbody:\n%s", name, text)
		}
	}
}

// TestDefaultRegistry_NotNil keeps the package-level registry from
// being accidentally swapped out by a future refactor.
func TestDefaultRegistry_NotNil(t *testing.T) {
	if DefaultRegistry() == nil {
		t.Fatal("DefaultRegistry returned nil")
	}
}

func TestSnapshotPresentMetric(t *testing.T) {
	SnapshotPresent.Set(1)
	srv := httptest.NewServer(MetricsHandler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), "pg_pr_snapshot_present 1") {
		t.Errorf("expected 'pg_pr_snapshot_present 1' in body, got:\n%s", body)
	}
}

func TestFingerprintMetricsRegistered(t *testing.T) {
	FingerprintPollDuration.WithLabelValues("mine").Observe(0.1)
	FingerprintChangesTotal.WithLabelValues("mine", "added").Inc()
	RefreshQueueDepth.WithLabelValues("team").Set(3)
	GraphQLRateRemaining.Set(4999)
	FingerprintPollSuccessTimestamp.WithLabelValues("team").Set(1.0)
	mfs, err := DefaultRegistry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	want := map[string]bool{
		"pg_pr_fingerprint_poll_duration_seconds":          false,
		"pg_pr_fingerprint_changes_total":                  false,
		"pg_pr_refresh_queue_depth":                        false,
		"pg_pr_graphql_rate_remaining":                     false,
		"pg_pr_fingerprint_poll_success_timestamp_seconds": false,
	}
	for _, mf := range mfs {
		if _, ok := want[mf.GetName()]; ok {
			want[mf.GetName()] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("metric %q not registered", name)
		}
	}
}

// TestReviewPreFetchFailuresTotal_Registered verifies the pre-fetch failure
// counter exists and is registered on the default registry so /metrics exports
// it.
func TestReviewPreFetchFailuresTotal_Registered(t *testing.T) {
	ReviewPreFetchFailuresTotal.WithLabelValues("credential").Inc()
	mfs, err := DefaultRegistry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() == "pg_pr_review_prefetch_failures_total" {
			return
		}
	}
	t.Fatal("pg_pr_review_prefetch_failures_total not registered")
}
