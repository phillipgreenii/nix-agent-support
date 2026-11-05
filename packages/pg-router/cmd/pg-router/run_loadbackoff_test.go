package main

import (
	"context"
	"io"
	"sync/atomic"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
)

func pinLoad1(t *testing.T, v float64, ok bool) {
	t.Helper()
	prev := load1
	load1 = func() (float64, bool) { return v, ok }
	t.Cleanup(func() { load1 = prev })
}

// Above the threshold the real tick skips the producer pass (the polled source
// is not polled) yet still dispatches queued work; at or below it, polling
// resumes on the very next tick.
func TestRunOneTick_loadBackoffSkipsProducingAboveThreshold(t *testing.T) {
	q, err := eventqueue.New(eventqueue.NewMemStore())
	if err != nil {
		t.Fatal(err)
	}
	var polls atomic.Int64
	o := limitTickOrchestrator(&polls)
	cfg := config.Config{Queries: o.Cfg.Queries, LoadBackoffThreshold: 50}
	tick := func() { runOneTick(context.Background(), cfg, o, &core.Service{}, q, "", io.Discard) }

	pinLoad1(t, 94, true)
	tick()
	tick()
	if got := polls.Load(); got != 0 {
		t.Fatalf("polled %d time(s) at load1 94 > 50; the producer pass must be skipped", got)
	}

	pinLoad1(t, 50, true) // exactly at the threshold: not above, so produce
	tick()
	if got := polls.Load(); got != 1 {
		t.Fatalf("polls = %d at load1 == threshold, want 1 (backoff resumes at or below)", got)
	}

	pinLoad1(t, 80, true)
	tick()
	if got := polls.Load(); got != 1 {
		t.Fatalf("polls = %d after load rose again, want still 1", got)
	}
}

func TestRunOneTick_loadBackoffDisabledOrUnreadableStillProduces(t *testing.T) {
	for name, tc := range map[string]struct {
		threshold float64
		load      float64
		ok        bool
	}{
		"disabled by zero threshold": {0, 500, true},
		"unreadable load fails open": {50, 0, false},
	} {
		t.Run(name, func(t *testing.T) {
			q, err := eventqueue.New(eventqueue.NewMemStore())
			if err != nil {
				t.Fatal(err)
			}
			var polls atomic.Int64
			o := limitTickOrchestrator(&polls)
			cfg := config.Config{Queries: o.Cfg.Queries, LoadBackoffThreshold: tc.threshold}
			pinLoad1(t, tc.load, tc.ok)
			runOneTick(context.Background(), cfg, o, &core.Service{}, q, "", io.Discard)
			if got := polls.Load(); got != 1 {
				t.Fatalf("polls = %d, want 1", got)
			}
		})
	}
}
