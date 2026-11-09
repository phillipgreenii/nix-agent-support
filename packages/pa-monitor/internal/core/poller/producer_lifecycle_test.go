package poller

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/corpus"
	"github.com/phillipgreenii/pa-monitor/internal/core/corpus/corpustest"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// TestProducer_Lifecycle_StartSeedsStopJoins covers the Phase-3 step-4 producer
// lifecycle (modeled on service.WriteService): Start seeds one synchronous
// publish then runs a single goroutine (idempotent via sync.Once), and Stop
// cancels + joins so publishing ceases.
func TestProducer_Lifecycle_StartSeedsStopJoins(t *testing.T) {
	sessionsDir, home, pidAlive := buildEquivalenceCorpus(t)
	p := newMonitorPoller(t, sessionsDir, home, pidAlive, time.Now())
	prod := p.Producer()
	prod.Now = time.Now // real clock so GeneratedAt advances between publishes
	prod.SetChangeSource(&feedChangeSource{interval: 2 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prod.Start(ctx)
	prod.Start(ctx) // idempotent — must not spawn a second goroutine or panic

	ds := prod.Load()
	if ds == nil {
		t.Fatal("Start must seed one publish synchronously (Load is nil after Start)")
	}
	first := ds.GeneratedAt

	// The goroutine republishes on its interval; GeneratedAt advances.
	deadline := time.After(2 * time.Second)
	for prod.Load().GeneratedAt.Equal(first) {
		select {
		case <-deadline:
			t.Fatal("producer goroutine never republished after Start")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	prod.Stop()
	prod.Stop() // idempotent

	// After Stop joins, publishing has ceased.
	afterStop := prod.Load().GeneratedAt
	time.Sleep(40 * time.Millisecond)
	if !prod.Load().GeneratedAt.Equal(afterStop) {
		t.Error("producer kept publishing after Stop returned")
	}
}

// TestProducer_ConcurrentPublishAndSnapshot_NoRace stresses the single-writer
// invariant under -race: the emit tick (Snapshot in async mode → Load + build)
// runs concurrently with the producer goroutine republishing. buildTree copies
// sessions before deriving Status and the producer creates all-new objects per
// batch, so the two touch disjoint state — no data race.
func TestProducer_ConcurrentPublishAndSnapshot_NoRace(t *testing.T) {
	sessionsDir, home, pidAlive := buildEquivalenceCorpus(t)
	p := newMonitorPoller(t, sessionsDir, home, pidAlive, time.Now())
	prod := p.Producer()
	prod.Now = time.Now
	prod.SetChangeSource(&feedChangeSource{interval: time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.StartProducer(ctx) // starts the producer goroutine (async publish)
	defer p.StopProducer()

	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, _, err := p.Snapshot(ctx); err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
	}
}

// TestProducer_RegisterAfterStartPanics covers the Register-before-Start freeze:
// the observer set is fixed once the producer goroutine owns the Monitor.
func TestProducer_RegisterAfterStartPanics(t *testing.T) {
	sessionsDir, home, pidAlive := buildEquivalenceCorpus(t)
	p := newMonitorPoller(t, sessionsDir, home, pidAlive, time.Now())
	prod := p.Producer()
	prod.Now = time.Now
	prod.SetChangeSource(&feedChangeSource{interval: time.Second})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prod.Start(ctx)
	defer prod.Stop()

	defer func() {
		if recover() == nil {
			t.Error("Register after Start must panic (observer set frozen at Start)")
		}
	}()
	prod.Register(corpus.NewLimitsObserver())
}

// flakyDiscovery wraps a Discovery and fails on demand, standing in for a
// recorder query that hits its deadline.
type flakyDiscovery struct {
	inner session.Discovery
	fail  atomic.Bool
}

func (d *flakyDiscovery) Discover() ([]*session.Session, error) {
	if d.fail.Load() {
		return nil, context.DeadlineExceeded
	}
	return d.inner.Discover()
}

// TestProducer_FailedScanKeepsLastGoodStateAndAges covers the stale-scan signal:
// when the corpus scan fails, the last good session tree keeps being served (not
// zeroed), the last-success stamp stops advancing so its age is observable, and
// a later good scan refreshes both.
func TestProducer_FailedScanKeepsLastGoodStateAndAges(t *testing.T) {
	sessionsDir, home, pidAlive := buildEquivalenceCorpus(t)
	m := corpustest.StartMirror(t, sessionsDir, home, pidAlive)
	disc := &flakyDiscovery{inner: m.Discovery()}
	mon := corpus.New(m.Facts, disc)
	mon.Register(corpus.NewSessionSnapshotObserver())
	mon.Register(corpus.NewSubagentErrorObserver())
	mon.Register(corpus.NewUsagePricingObserver(eqPrices))
	mon.Register(corpus.NewLimitsObserver())
	p := &Poller{IdleThreshold: time.Hour, Monitor: mon}
	prod := p.Producer()

	clock := time.Unix(1_776_000_300, 0)
	prod.Now = func() time.Time { return clock }
	ctx := context.Background()

	if !p.ScanLastSuccess().IsZero() {
		t.Fatal("ScanLastSuccess must be zero before any scan")
	}
	prod.refresh(ctx)
	good := prod.Load()
	if good == nil || len(good.Sessions) == 0 {
		t.Fatalf("first scan published no sessions: %+v", good)
	}
	if got := p.ScanLastSuccess(); !got.Equal(clock) {
		t.Fatalf("ScanLastSuccess = %v, want %v", got, clock)
	}

	// The recorder query now times out for ten minutes.
	disc.fail.Store(true)
	clock = clock.Add(10 * time.Minute)
	prod.refresh(ctx)
	if prod.Load() != good {
		t.Error("a failed scan replaced the last good DerivedState")
	}
	if age := clock.Sub(p.ScanLastSuccess()); age != 10*time.Minute {
		t.Errorf("scan age = %v, want 10m", age)
	}

	// Recovery refreshes both the state and the stamp.
	disc.fail.Store(false)
	clock = clock.Add(time.Minute)
	prod.refresh(ctx)
	if got := p.ScanLastSuccess(); !got.Equal(clock) {
		t.Errorf("ScanLastSuccess after recovery = %v, want %v", got, clock)
	}
}

// TestProducer_NeverSucceededScanStaysZero: a daemon whose very first scan fails
// has no state to keep, and its stamp stays zero so the age alert fires.
func TestProducer_NeverSucceededScanStaysZero(t *testing.T) {
	sessionsDir, home, pidAlive := buildEquivalenceCorpus(t)
	m := corpustest.StartMirror(t, sessionsDir, home, pidAlive)
	disc := &flakyDiscovery{inner: m.Discovery()}
	disc.fail.Store(true)
	p := &Poller{IdleThreshold: time.Hour, Monitor: corpus.New(m.Facts, disc)}
	prod := p.Producer()
	prod.Now = func() time.Time { return time.Unix(1_776_000_300, 0) }
	prod.refresh(context.Background())
	if prod.Load() != nil {
		t.Error("failed first scan must publish nothing")
	}
	if !p.ScanLastSuccess().IsZero() {
		t.Error("ScanLastSuccess must stay zero when no scan ever succeeded")
	}
}
