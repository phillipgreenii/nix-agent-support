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
