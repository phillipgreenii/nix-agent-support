package serve

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/watcher"
)

// The pacer tests run on the real clock: clock.Fake.Sleep advances and never
// blocks, so a wait test on it would pass with the cancellable wait reverted.
func TestPacerNeverExceedsTheCapAndFinishes(t *testing.T) {
	const rate, n = 200, 20 // one slot per 5 ms
	p := newPacer(clock.Real{}, rate)
	ctx := context.Background()
	start := time.Now()
	stamps := make([]time.Time, 0, n)
	for i := 0; i < n; i++ {
		if !p.wait(ctx) {
			t.Fatal("wait reported cancelled on a live context")
		}
		stamps = append(stamps, time.Now())
	}
	// Never exceeds the cap: n releases take at least (n-1) slots.
	if got, min := stamps[n-1].Sub(start), time.Duration(n-1)*time.Second/rate; got < min {
		t.Fatalf("pass took %v, the cap needs at least %v", got, min)
	}
	// Finishes: well inside a generous multiple of the ideal time.
	if got := stamps[n-1].Sub(start); got > 2*time.Second {
		t.Fatalf("pass took %v, never released at its cap", got)
	}
}

func TestPacerEarnsNoBurstCreditWhileIdleAndZeroIsUnlimited(t *testing.T) {
	ctx := context.Background()
	p := newPacer(clock.Real{}, 20) // one slot per 50 ms
	p.wait(ctx)
	// 200 ms idle is 4 slots of 50 ms: a pacer that banked idle time would
	// release the next 4 at once, which the 140 ms floor below rejects. Real
	// clock on purpose (built with clock.Real{}); whole test 0.35 s measured.
	time.Sleep(200 * time.Millisecond) // idle
	t0 := time.Now()
	for i := 0; i < 4; i++ {
		p.wait(ctx)
	}
	if got := time.Since(t0); got < 140*time.Millisecond {
		t.Fatalf("4 releases after an idle spell took %v: idle time must not buy a burst", got)
	}
	u := newPacer(clock.Real{}, 0)
	t1 := time.Now()
	for i := 0; i < 1000; i++ {
		u.wait(ctx)
	}
	if time.Since(t1) > 100*time.Millisecond {
		t.Fatal("a zero cap waited")
	}
}

// Cancelling the context wakes every cancellable wait of a sweep at once: the
// pacer, the copy throttle and the backfill's per-path pacing. Each case blocks
// for an hour unless the context frees it.
func TestWaitsWakeOnCancel(t *testing.T) {
	cases := []struct {
		name string
		// setup builds the case (outside any ctx) and returns what runs under ctx.
		setup func(t *testing.T) func(ctx context.Context) bool
	}{
		{"pacer", func(*testing.T) func(ctx context.Context) bool {
			return func(ctx context.Context) bool {
				p := newPacer(clock.Real{}, 1)
				p.wait(ctx) // takes the free slot; the next is a second away
				p.next = time.Now().Add(time.Hour)
				return p.wait(ctx)
			}
		}},
		{"copy throttle", func(*testing.T) func(ctx context.Context) bool {
			return func(ctx context.Context) bool { return clock.SleepCtx(ctx, clock.Real{}, time.Hour) }
		}},
		{"backfill pacing", func(t *testing.T) func(ctx context.Context) bool {
			e := newEnv(t, false)
			e.cfg.MaxFilesPerSecond = 1
			e.transcript("a.jsonl", "a\n", time.Now().Add(-time.Hour))
			e.transcript("b.jsonl", "b\n", time.Now().Add(-time.Hour))
			d := e.open(t)
			t.Cleanup(func() { _ = d.Close() })
			d.sweepPace.next = time.Now().Add(time.Hour)
			return func(ctx context.Context) bool {
				ctx2, cancel := context.WithCancel(ctx)
				d.ctx = ctx2
				go func() { time.Sleep(20 * time.Millisecond); cancel() }()
				d.backfill(ctx2, []watcher.FileInfo{{Path: filepath.Join(e.claude, "projects", "example-proj", "a.jsonl")}})
				return false
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan bool, 1)
			run := tc.setup(t)
			go func() { done <- run(ctx) }()
			time.Sleep(30 * time.Millisecond)
			cancel()
			select {
			case ok := <-done:
				if ok {
					t.Fatal("a cancelled wait reported completion")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the wait ignored the cancelled context")
			}
		})
	}
}

// The sweep and the first catch-up stay under the configured cap on the daemon's
// clock and still ingest every file.
func TestSweepAndCatchUpRespectTheCapAndStillFinish(t *testing.T) {
	const rate, files = 10, 30
	e, d, clk := fakeClockDaemon(t, func(e *env) { e.cfg.MaxFilesPerSecond = rate })
	old := clk.Now().Add(-time.Hour)
	for i := 0; i < files; i++ {
		e.transcript(fmt.Sprintf("f%02d.jsonl", i), "{\"type\":\"user\"}\n", old)
	}
	start := clk.Now()
	// The pacer waits on the clock; the fake clock advances only when told to.
	stopAdv := make(chan struct{})
	defer close(stopAdv)
	go func() {
		for {
			select {
			case <-stopAdv:
				return
			default:
			}
			if clk.Waiters() > 0 {
				clk.Advance(10 * time.Millisecond)
			}
			time.Sleep(50 * time.Microsecond)
		}
	}()
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
	// One pass ingests each file twice (catch-up, then the sweep's own event on
	// the first sweep); both are paced against the same cap.
	min := time.Duration(2*files-1) * time.Second / rate
	if got := clk.Now().Sub(start); got < min {
		t.Fatalf("pass took %v on the fake clock, cap %d/s over %d ingests needs at least %v", got, rate, 2*files, min)
	}
	n := ingestCount(d, "copied") + ingestCount(d, "new_generation") + ingestCount(d, "unchanged")
	if n < files {
		t.Fatalf("only %v of %d files ingested", n, files)
	}
}
