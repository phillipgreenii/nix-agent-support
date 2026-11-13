package httpapi

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// fakeSource is a Gatherer whose Gather can be slowed or failed.
type fakeSource struct {
	calls atomic.Int32
	delay atomic.Int64 // ns
	fail  atomic.Bool
}

func (f *fakeSource) Gather() ([]*dto.MetricFamily, error) {
	n := f.calls.Add(1)
	time.Sleep(time.Duration(f.delay.Load()))
	if f.fail.Load() {
		return nil, errors.New("boom")
	}
	reg := prometheus.NewRegistry()
	reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "pg_desk_test_gen", Help: "h"}, func() float64 { return float64(n) }))
	return reg.Gather()
}

func gaugeVal(t *testing.T, g prometheus.Gatherer, name string) (float64, bool) {
	t.Helper()
	fams, err := g.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fams {
		if f.GetName() == name {
			return f.GetMetric()[0].GetGauge().GetValue() + f.GetMetric()[0].GetCounter().GetValue(), true
		}
	}
	return 0, false
}

func TestSnapshotGatherer_ServesCacheAndAges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := &fakeSource{}
		g := newSnapshotGatherer(src, 15*time.Second, 5*time.Second)
		ctx, cancel := context.WithCancel(context.Background())
		g.Start(ctx)
		synctest.Wait()

		for i := 0; i < 5; i++ { // scrapes never collect
			if v, ok := gaugeVal(t, g, "pg_desk_test_gen"); !ok || v != 1 {
				t.Fatalf("scrape %d: gen=%v ok=%v", i, v, ok)
			}
		}
		if src.calls.Load() != 1 {
			t.Fatalf("calls=%d", src.calls.Load())
		}
		time.Sleep(10 * time.Second)
		if v, _ := gaugeVal(t, g, "pg_desk_metrics_snapshot_age_seconds"); v != 10 {
			t.Fatalf("age=%v want 10", v)
		}
		time.Sleep(6 * time.Second) // tick at 15s
		synctest.Wait()
		if v, _ := gaugeVal(t, g, "pg_desk_test_gen"); v != 2 {
			t.Fatalf("gen=%v want 2", v)
		}
		cancel()
		synctest.Wait()
	})
}

func TestSnapshotGatherer_FirstScrapeDoesNotHang(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := &fakeSource{}
		src.delay.Store(int64(time.Hour))
		g := newSnapshotGatherer(src, 15*time.Second, 5*time.Second)
		ctx, cancel := context.WithCancel(context.Background())
		g.Start(ctx)
		start := time.Now()
		fams, err := g.Gather()
		if err != nil || time.Since(start) != 5*time.Second {
			t.Fatalf("err=%v waited=%v", err, time.Since(start))
		}
		for _, f := range fams {
			if f.GetName() == "pg_desk_metrics_snapshot_age_seconds" || f.GetName() == "pg_desk_test_gen" {
				t.Fatalf("unexpected family %s before first snapshot", f.GetName())
			}
		}
		cancel()
		time.Sleep(2 * time.Hour)
		synctest.Wait()
	})
}

func TestSnapshotGatherer_FailingRefreshKeepsSnapshotAndShowsAge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := &fakeSource{}
		g := newSnapshotGatherer(src, 15*time.Second, 5*time.Second)
		ctx, cancel := context.WithCancel(context.Background())
		g.Start(ctx)
		synctest.Wait()
		src.fail.Store(true)
		time.Sleep(46 * time.Second) // ticks at 15, 30, 45
		synctest.Wait()
		if v, ok := gaugeVal(t, g, "pg_desk_test_gen"); !ok || v != 1 {
			t.Fatalf("stale snapshot not served: %v %v", v, ok)
		}
		if v, _ := gaugeVal(t, g, "pg_desk_metrics_snapshot_age_seconds"); v != 46 {
			t.Fatalf("age=%v want 46", v)
		}
		if v, _ := gaugeVal(t, g, "pg_desk_metrics_snapshot_errors_total"); v != 3 {
			t.Fatalf("errors=%v want 3", v)
		}
		cancel()
		synctest.Wait()
	})
}

func TestSnapshotGatherer_SlowRefreshDoesNotBlockScrape(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := &fakeSource{}
		g := newSnapshotGatherer(src, 15*time.Second, 5*time.Second)
		ctx, cancel := context.WithCancel(context.Background())
		g.Start(ctx)
		synctest.Wait()
		src.delay.Store(int64(time.Hour))
		time.Sleep(16 * time.Second) // refresh now stuck
		start := time.Now()
		if _, ok := gaugeVal(t, g, "pg_desk_test_gen"); !ok || time.Since(start) != 0 {
			t.Fatalf("scrape blocked or lost snapshot (%v)", time.Since(start))
		}
		cancel()
		time.Sleep(2 * time.Hour)
		synctest.Wait()
	})
}

func TestNewHandler_WithMetricsSnapshotServesCachedCatalog(t *testing.T) {
	setClock(t, time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC))
	s := store.OpenNewSchemaForTest(t)
	mustUpsertInterpretation(t, s, store.Interpretation{Repo: "o/r", EntityType: "pr", EntityID: "o/r#1", Panel: PanelMineAwaitingMe, AsOf: "2026-10-10T00:00:00Z"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, err := NewHandler(s, testConfig(), WithMetricsSnapshot(ctx))
	if err != nil {
		t.Fatal(err)
	}
	body := scrapeBody(t, h)
	requireLines(t, body, `pg_desk_pr_open_anchors 0`)
	if !strings.Contains(body, "pg_desk_metrics_snapshot_age_seconds") {
		t.Fatalf("missing age gauge:\n%s", body)
	}
}

func TestSnapshotGatherer_AgeRisesOnFailureAndResetsAfterSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := &fakeSource{}
		g := newSnapshotGatherer(src, 30*time.Second, 5*time.Second)
		ctx, cancel := context.WithCancel(context.Background())
		g.Start(ctx)
		synctest.Wait()
		src.fail.Store(true)
		time.Sleep(61 * time.Second) // ticks at 30 and 60 fail
		synctest.Wait()
		if v, _ := gaugeVal(t, g, "pg_desk_metrics_snapshot_age_seconds"); v != 61 {
			t.Fatalf("age=%v want 61 while failing", v)
		}
		if v, _ := gaugeVal(t, g, "pg_desk_metrics_snapshot_errors_total"); v != 2 {
			t.Fatalf("errors=%v want 2", v)
		}
		src.fail.Store(false)
		time.Sleep(30 * time.Second) // tick at 90 succeeds
		synctest.Wait()
		if v, _ := gaugeVal(t, g, "pg_desk_metrics_snapshot_age_seconds"); v != 1 {
			t.Fatalf("age=%v want 1 after a good build", v)
		}
		if v, _ := gaugeVal(t, g, "pg_desk_test_gen"); v != 4 {
			t.Fatalf("gen=%v want the fresh snapshot (4)", v)
		}
		fams, _ := g.Gather()
		found := false
		for _, f := range fams {
			if f.GetName() == "pg_desk_metrics_snapshot_compute_duration_seconds" && f.GetMetric()[0].GetHistogram().GetSampleCount() == 4 {
				found = true
			}
		}
		if !found {
			t.Fatal("compute-duration histogram missing or not observing every build")
		}
		cancel()
		synctest.Wait()
	})
}
