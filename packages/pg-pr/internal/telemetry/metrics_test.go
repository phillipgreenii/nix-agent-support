package telemetry

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestMetricsHandler_Smokes the package-level metrics endpoint by
// observing a value, scraping, and asserting the expected metric names
// appear in the OpenMetrics text output.
func TestMetricsHandler_ServesExpectedMetricNames(t *testing.T) {
	// Move the gauges/counters off zero so they show up in the scrape.
	SyncPRDuration.WithLabelValues("repo/a").Observe(0.123)
	SyncErrorsTotal.WithLabelValues("repo/a").Inc()
	FeedbackCreatedTotal.WithLabelValues("repo/a", "comment_thread").Inc()
	CIOnlyAttempts.WithLabelValues("repo/a", "42").Set(3)
	ObserveSyncSuccess("repo/a", time.Unix(1_700_000_000, 0))

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
		"pg_pr_last_sync_success_timestamp_seconds",
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
