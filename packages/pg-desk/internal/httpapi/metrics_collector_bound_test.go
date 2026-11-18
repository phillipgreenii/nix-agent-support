package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
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
func newScrapeRequest() *http.Request {
	return httptest.NewRequestWithContext(context.Background(), "GET", "/metrics", nil)
}

// collectorHist reads the duration histogram of one collector label.
func collectorHist(t *testing.T, name string) *dto.Histogram {
	t.Helper()
	var m dto.Metric
	if err := collectorDuration.WithLabelValues(name).(interface{ Write(*dto.Metric) error }).Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetHistogram()
}

func TestBoundedCollector_ObservesWallTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newBounded("dur_fast", func() (int, error) { time.Sleep(3500 * time.Millisecond); return 1, nil })
		if _, err := b(); err != nil {
			t.Fatal(err)
		}
		h := collectorHist(t, "dur_fast")
		if h.GetSampleCount() != 1 || h.GetSampleSum() != 3.5 {
			t.Fatalf("count=%d sum=%v want 1 and 3.5", h.GetSampleCount(), h.GetSampleSum())
		}
		// 3.5 s must land in a bucket narrower than the old 2.5..5 jump.
		for _, bk := range h.GetBucket() {
			if bk.GetUpperBound() == 3 && bk.GetCumulativeCount() != 0 {
				t.Fatalf("3.5 s counted in le=3")
			}
			if bk.GetUpperBound() == 3.5 && bk.GetCumulativeCount() != 1 {
				t.Fatalf("3.5 s not counted in le=3.5")
			}
		}
	})
}

// A call that outlives the timeout is still measured, once it finishes.
func TestBoundedCollector_ObservesCallPastTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newBounded("dur_slow", func() (int, error) { time.Sleep(collectorTimeout + 2*time.Second); return 1, nil })
		if _, err := b(); err == nil {
			t.Fatal("want timeout error")
		}
		if n := collectorHist(t, "dur_slow").GetSampleCount(); n != 0 {
			t.Fatalf("observed %d before the call finished", n)
		}
		time.Sleep(3 * time.Second) // let the call finish
		synctest.Wait()
		h := collectorHist(t, "dur_slow")
		if h.GetSampleCount() != 1 || h.GetSampleSum() != (collectorTimeout+2*time.Second).Seconds() {
			t.Fatalf("count=%d sum=%v", h.GetSampleCount(), h.GetSampleSum())
		}
	})
}

// The series is part of the served catalog, with the collector label.
func TestCollectorDuration_ServedOnMetrics(t *testing.T) {
	setClock(t, time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC))
	s := store.OpenNewSchemaForTest(t)
	mustUpsertInterpretation(t, s, store.Interpretation{Repo: "o/r", EntityType: "pr", EntityID: "o/r#1", Panel: PanelMineAwaitingMe, AsOf: "2026-10-10T00:00:00Z"})
	h, err := NewHandler(context.Background(), s, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	body := scrapeBody(t, h)
	if !strings.Contains(body, `pg_desk_metrics_collector_duration_seconds_bucket{collector="dashboard",le="3"}`) {
		t.Fatalf("collector duration series missing:\n%s", body)
	}
}
