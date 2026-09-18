package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/phillipgreenii/pg-router/internal/metrics"
)

// TestStartMetricsServer_ServesCatalogMemberOverHTTP proves the OTel
// Prometheus bridge actually reaches an HTTP GET /metrics response: it
// records one MetricQueueDepth-shaped counter through the returned
// MeterProvider, force-flushes it (metrics.Flush's own seam), and asserts
// the scraped text contains that instrument's name and label.
func TestStartMetricsServer_ServesCatalogMemberOverHTTP(t *testing.T) {
	var ln net.Listener
	mp, shutdown, err := startMetricsServer("127.0.0.1:0", func(l net.Listener) { ln = l })
	if err != nil {
		t.Fatalf("startMetricsServer: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = shutdown(ctx)
	})
	if ln == nil {
		t.Fatal("onListen was never called")
	}

	meter := mp.Meter("test")
	counter, err := meter.Int64Counter("pg_router_test_counter")
	if err != nil {
		t.Fatalf("Int64Counter: %v", err)
	}
	counter.Add(context.Background(), 3, metric.WithAttributes(attribute.String("type", "probe")))

	if err := forceFlush(context.Background(), mp); err != nil {
		t.Fatalf("force flush: %v", err)
	}

	url := fmt.Sprintf("http://%s/metrics", ln.Addr().String())
	body := scrape(t, url)
	if !strings.Contains(body, "pg_router_test_counter") {
		t.Errorf("scrape missing instrument name: %q", body)
	}
	if !strings.Contains(body, `type="probe"`) {
		t.Errorf("scrape missing attribute label: %q", body)
	}
}

// TestStartMetricsServer_CatalogSurvivesUTF8EscapingNegotiation is pg2-y3u22's
// regression test for the root cause it fixed: Prometheus 3.x scrapers send an
// Accept header offering UTF-8 metric-name-escaping negotiation
// ("escaping=allow-utf-8"), and go.opentelemetry.io/otel/exporters/prometheus
// honors it — so an OTel-dotted instrument name (e.g. the old
// "pg_router.backlog") used to come back on the wire with its dot preserved
// literally, instead of translated to "pg_router_backlog", mismatching the
// underscore names the pg-router.json Grafana dashboard queries (the same
// convention pg-pr-ops.json already uses). Registering the real catalog
// (internal/metrics.New) directly with underscore/legacy-safe names — the
// fix — sidesteps the negotiation question entirely: there is no dot left to
// preserve or escape, regardless of which scheme the scraper offers. This
// test proves that by sending exactly that Accept header at a real HTTP
// scrape of the real catalog and asserting every one of the ten members
// (Task 3.3) is present, dot-free, and suffixed the way Prometheus's own
// counter/histogram conventions expect (_total, _bucket/_sum/_count).
func TestStartMetricsServer_CatalogSurvivesUTF8EscapingNegotiation(t *testing.T) {
	var ln net.Listener
	mp, shutdown, err := startMetricsServer("127.0.0.1:0", func(l net.Listener) { ln = l })
	if err != nil {
		t.Fatalf("startMetricsServer: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = shutdown(ctx)
	})

	emitter, err := metrics.New(mp, func() map[string]int {
		return map[string]int{"review-requested": 4}
	}, metrics.WithLiveness(func() bool { return true }))
	if err != nil {
		t.Fatalf("metrics.New: %v", err)
	}
	emitter.RecordFailure(metrics.FailureClassDeclined)
	emitter.OnSourceFailure("srcA")
	emitter.OnDeduped("review-requested")
	emitter.RecordThroughput("review-requested")
	emitter.RecordDispatchLatency(12.5, "accepted")
	emitter.OnUnconsumedExpired("review-requested")
	emitter.OnUnknownTypeRejected("review-requested")

	if err := forceFlush(context.Background(), mp); err != nil {
		t.Fatalf("force flush: %v", err)
	}

	url := fmt.Sprintf("http://%s/metrics", ln.Addr().String())
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	// The exact negotiation a Prometheus 3.x scraper offers (INV-OBS-1's
	// live deployment target — see pg2-nm2jg's dashboard verification).
	req.Header.Set("Accept", "text/plain;version=0.0.4;escaping=allow-utf-8")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	text := string(body)

	// Every catalog member's expected wire name (Prometheus's own
	// counter/_total and histogram/_bucket conventions), matching what
	// pg-router.json's panel queries expect.
	want := []string{
		metrics.MetricQueueDepth,
		metrics.MetricBacklog,
		metrics.MetricLiveness,
		metrics.MetricFailures + "_total",
		metrics.MetricUnconsumedExpired + "_total",
		metrics.MetricUnknownTypeRejected + "_total",
		metrics.MetricThroughput + "_total",
		metrics.MetricSourceFailures + "_total",
		metrics.MetricDeduped + "_total",
		metrics.MetricDispatchLatency + "_milliseconds_bucket",
	}
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Errorf("scrape body missing expected underscore-named series %q:\n%s", w, text)
		}
	}
	// The historical bug: the OTel-native dotted form surviving negotiation
	// instead of being translated. MUST be entirely absent now.
	for _, dotted := range []string{"pg_router.backlog", "pg_router.queue_depth", "pg_router.liveness"} {
		if strings.Contains(text, dotted) {
			t.Errorf("scrape body still contains dotted name %q — escaping negotiation preserved the dot", dotted)
		}
	}
}

// TestStartMetricsServer_ListenErrorPropagates proves a bind failure (an
// unparseable address, the cheapest deterministic way to make net.Listen
// fail without racing a real port) surfaces as an error rather than a
// silently-nil server, mirroring pg-pr's own daemon.go contract ("a bind
// failure is fatal because metrics is a daemon contract").
func TestStartMetricsServer_ListenErrorPropagates(t *testing.T) {
	if _, _, err := startMetricsServer("not-a-valid-address", nil); err == nil {
		t.Fatal("expected an error for an unparseable listen address")
	}
}

// forceFlush is the same ForceFlush-if-supported seam
// internal/metrics.Flush exposes, duplicated here at package scope so this
// test does not need to import internal/metrics just to flush the fresh
// SDK-backed provider startMetricsServer returns before scraping it.
func forceFlush(ctx context.Context, mp metric.MeterProvider) error {
	if f, ok := mp.(interface {
		ForceFlush(context.Context) error
	}); ok {
		return f.ForceFlush(ctx)
	}
	return nil
}

// scrape performs one GET against url and returns the response body,
// failing the test on any transport error or non-200 status.
func scrape(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec,noctx // test-local loopback URL, no untrusted input
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}
