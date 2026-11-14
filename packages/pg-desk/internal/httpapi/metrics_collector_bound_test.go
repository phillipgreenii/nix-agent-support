package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	dto "github.com/prometheus/client_model/go"
)

func TestBoundedCollector_HungCollectorTimesOutAndServesLastGood(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hang := make(chan struct{})
		var calls atomic.Int32
		b := newBounded("hang", func() (int, error) {
			n := calls.Add(1)
			if n == 2 {
				<-hang
			}
			return int(n), nil
		})
		if v, err := b(); err != nil || v != 1 {
			t.Fatalf("first: %v %v", v, err)
		}
		start := time.Now()
		v, err := b() // call 2 hangs
		if err != nil || v != 1 || time.Since(start) != collectorTimeout {
			t.Fatalf("hung: v=%v err=%v waited=%v", v, err, time.Since(start))
		}
		// A later call joins the in-flight one rather than starting another.
		if v, _ := b(); v != 1 || calls.Load() != 2 {
			t.Fatalf("joined: v=%v calls=%d", v, calls.Load())
		}
		close(hang)
		synctest.Wait()
		if v, _ := b(); v != 3 {
			t.Fatalf("after release: %v", v)
		}
	})
}

func TestBoundedCollector_HungWithNoGoodValueErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hang := make(chan struct{})
		b := newBounded("hang", func() (int, error) { <-hang; return 1, nil })
		if _, err := b(); err == nil {
			t.Fatal("want timeout error")
		}
		close(hang)
		synctest.Wait()
	})
}

func TestBoundedCollector_PassesErrorThrough(t *testing.T) {
	boom := errors.New("boom")
	b := newBounded("e", func() (int, error) { return 0, boom })
	if _, err := b(); !errors.Is(err, boom) {
		t.Fatalf("err=%v", err)
	}
}

// A hung collector must not stretch the snapshot build past the timeout.
func TestSnapshotBuild_HungCollectorStillCompletesWithinTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hang := make(chan struct{})
		slow := newBounded("slow", func() (int, error) { <-hang; return 1, nil })
		src := gathererFunc(func() { _, _ = slow() })
		g := newSnapshotGatherer(src, 15*time.Second, 5*time.Second)
		start := time.Now()
		g.refresh()
		if d := time.Since(start); d != collectorTimeout {
			t.Fatalf("build took %v want %v", d, collectorTimeout)
		}
		close(hang)
		synctest.Wait()
	})
}

// The scrape handler answers while a rebuild is held open.
func TestScrapeHandler_NotBlockedByHeldRebuild(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := &fakeSource{}
		g := newSnapshotGatherer(src, 15*time.Second, 5*time.Second)
		h := scrapeHandler(g, scrapeTimeout)
		g.refresh() // snapshot exists
		src.delay.Store(int64(time.Hour))
		go g.refresh() // rebuild held open
		synctest.Wait()
		start := time.Now()
		rec := newRecorder()
		h.ServeHTTP(rec, newScrapeRequest())
		if rec.Code != 200 || time.Since(start) > 100*time.Millisecond {
			t.Fatalf("code=%d took %v", rec.Code, time.Since(start))
		}
		src.delay.Store(0)
		time.Sleep(2 * time.Hour)
		synctest.Wait()
	})
}

type gathererFunc func()

func (f gathererFunc) Gather() ([]*dto.MetricFamily, error) { f(); return nil, nil }

func newRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }
func newScrapeRequest() *http.Request         { return httptest.NewRequest("GET", "/metrics", nil) }
