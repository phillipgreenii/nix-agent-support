package httpapi

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// These tests guard bead pg2-n6d8y item 3: abandoned /metrics scrapes must not
// pile up. A collection cannot be cancelled once started (the OTel exporter
// collects with a background context), so the handler instead (a) joins every
// concurrent scrape onto the one collection already in flight and (b) answers
// 503 once its own deadline passes instead of holding the connection.

// blockingCollector is a prometheus.Collector whose Collect blocks until
// release is closed, counting how many collections were started.
type blockingCollector struct {
	started atomic.Int32
	release chan struct{}
}

func (c *blockingCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- prometheus.NewDesc("pg_desk_test_blocking", "test", nil, nil)
}

func (c *blockingCollector) Collect(ch chan<- prometheus.Metric) {
	c.started.Add(1)
	<-c.release
	ch <- prometheus.MustNewConstMetric(prometheus.NewDesc("pg_desk_test_blocking", "test", nil, nil), prometheus.GaugeValue, 1)
}

func newBlockingRegistry(t *testing.T) (*prometheus.Registry, *blockingCollector) {
	t.Helper()
	c := &blockingCollector{release: make(chan struct{})}
	reg := prometheus.NewRegistry()
	if err := reg.Register(c); err != nil {
		t.Fatalf("register: %v", err)
	}
	return reg, c
}

func TestScrapeHandler_ConcurrentScrapesShareOneCollection(t *testing.T) {
	reg, c := newBlockingRegistry(t)
	h := scrapeHandler(reg, time.Minute)

	const scrapes = 5
	var wg sync.WaitGroup
	codes := make([]int, scrapes)
	serve := func(i int) {
		defer wg.Done()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		codes[i] = rec.Code
	}
	wg.Add(1)
	go serve(0)
	// Wait for the first collection to be in flight, then pile on the rest.
	deadline := time.Now().Add(5 * time.Second)
	for c.started.Load() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("first collection never started")
		}
		time.Sleep(time.Millisecond)
	}
	for i := 1; i < scrapes; i++ {
		wg.Add(1)
		go serve(i)
	}
	time.Sleep(100 * time.Millisecond) // let the followers reach the handler
	close(c.release)
	wg.Wait()

	if got := c.started.Load(); got != 1 {
		t.Fatalf("%d concurrent scrapes started %d collections, want 1 shared collection", scrapes, got)
	}
	for i, code := range codes {
		if code != http.StatusOK {
			t.Errorf("scrape %d status = %d, want 200", i, code)
		}
	}
}

func TestScrapeHandler_SlowCollectionAnswers503AtDeadline(t *testing.T) {
	reg, c := newBlockingRegistry(t)
	defer close(c.release)
	h := scrapeHandler(reg, 50*time.Millisecond)

	done := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		done <- rec.Code
	}()
	select {
	case code := <-done:
		if code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 once the scrape deadline passes", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scrape held its connection past the deadline")
	}
}
