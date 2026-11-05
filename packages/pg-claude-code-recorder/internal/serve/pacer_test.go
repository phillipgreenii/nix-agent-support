package serve

import (
	"fmt"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
)

func TestPacerNeverExceedsTheCapAndFinishes(t *testing.T) {
	const rate, n = 20, 200
	clk := clock.NewFake(time.Unix(1_000_000, 0))
	p := newPacer(clk, rate)
	start := clk.Now()
	stamps := make([]time.Time, 0, n)
	for i := 0; i < n; i++ {
		p.wait()
		stamps = append(stamps, clk.Now())
	}
	// Finishes: n files take (n-1)/rate seconds, no more than that plus one slot.
	if got, want := stamps[n-1].Sub(start), time.Duration(n-1)*time.Second/rate; got != want {
		t.Fatalf("pass took %v, want %v", got, want)
	}
	// Never exceeds the cap: in any window of one second at most rate+1 releases
	// (rate slots plus the one on the window's opening edge).
	for i := range stamps {
		c := 0
		for j := i; j < n && stamps[j].Sub(stamps[i]) < time.Second; j++ {
			c++
		}
		if c > rate {
			t.Fatalf("%d releases within one second of release %d, cap %d", c, i, rate)
		}
	}
}

func TestPacerEarnsNoBurstCreditWhileIdleAndZeroIsUnlimited(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_000_000, 0))
	p := newPacer(clk, 10)
	p.wait()
	clk.Advance(time.Hour) // idle
	t0 := clk.Now()
	for i := 0; i < 10; i++ {
		p.wait()
	}
	if got := clk.Now().Sub(t0); got < 800*time.Millisecond {
		t.Fatalf("10 releases after an idle hour took %v: idle time must not buy a burst", got)
	}
	u := newPacer(clk, 0)
	t1 := clk.Now()
	for i := 0; i < 1000; i++ {
		u.wait()
	}
	if !clk.Now().Equal(t1) {
		t.Fatal("a zero cap waited")
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
