package poller

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/corpus"
)

// TestProducer_Lifecycle_StartSeedsStopJoins covers the Phase-3 step-4 producer
// lifecycle (modeled on service.WriteService): Start seeds one synchronous
// publish then runs a single goroutine (idempotent via sync.Once), and Stop
// cancels + joins so publishing ceases.
func TestProducer_Lifecycle_StartSeedsStopJoins(t *testing.T) {
	sessionsDir, home, pidAlive := buildEquivalenceCorpus(t)
	p := newMonitorPoller(sessionsDir, home, pidAlive, time.Now())
	prod := p.Producer()
	prod.Now = time.Now // real clock so GeneratedAt advances between publishes
	prod.Interval = 2 * time.Millisecond

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
	p := newMonitorPoller(sessionsDir, home, pidAlive, time.Now())
	prod := p.Producer()
	prod.Now = time.Now
	prod.Interval = time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.StartProducer(ctx) // async: SynchronousMode=false + producer goroutine
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
	p := newMonitorPoller(sessionsDir, home, pidAlive, time.Now())
	prod := p.Producer()
	prod.Now = time.Now
	prod.Interval = time.Second

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
