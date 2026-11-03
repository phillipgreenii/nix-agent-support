package serve

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

func TestTickLoopRunsStepAndStopsWithoutSleeping(t *testing.T) {
	stop := make(chan struct{})
	tick := make(chan time.Time, 1)
	ran := make(chan struct{}, 4)
	var steps atomic.Int32
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tickLoop(stop, tick, func() { steps.Add(1); ran <- struct{}{} })
	}()
	tick <- time.Time{}
	select {
	case <-ran:
	case <-time.After(10 * time.Second):
		t.Fatal("step did not run on a tick")
	}
	close(stop)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("loop did not stop")
	}
	// A tick after stop must not run a step.
	tick <- time.Time{}
	tickLoop(stop, tick, func() { steps.Add(1) })
	if got := steps.Load(); got != 1 {
		t.Fatalf("steps = %d, want 1", got)
	}
}

func TestCheckpointOnceRecordsOutcomeAndGauges(t *testing.T) {
	d, clk, buf := tickEnv()
	d.checkpointOnce(func() (int, int, error) { clk.Advance(2 * time.Second); return 40, 25, nil })
	reg := d.obs.Reg
	if v, _ := reg.Gauge(obs.MWALFrames, nil); v != 40 {
		t.Errorf("wal_frames = %v", v)
	}
	if v, _ := reg.Gauge(obs.MWALCheckpointedFrames, nil); v != 25 {
		t.Errorf("wal_checkpointed_frames = %v", v)
	}
	if v, _ := reg.Gauge(obs.MWALCheckpointLastTime, nil); v <= 0 {
		t.Errorf("last timestamp = %v", v)
	}
	if h := reg.Hist(obs.MWALCheckpointDuration); h == nil || h.Samples != 1 {
		t.Errorf("histogram %+v", h)
	}
	if v, _ := reg.Counter(obs.MLoopRuns, map[string]string{"loop": obs.LoopWALCheckpoint, "outcome": "ok"}); v != 1 {
		t.Errorf("ok runs = %v", v)
	}
	if len(logLines(t, buf, "wal_checkpoint_failed")) != 0 {
		t.Error("unexpected warning")
	}
}

func TestCheckpointOnceErrorIsWarnedOncePerMinute(t *testing.T) {
	d, clk, buf := tickEnv()
	fail := func() (int, int, error) { return 0, 0, errors.New("database is locked") }
	d.checkpointOnce(fail)
	clk.Advance(10 * time.Second)
	d.checkpointOnce(fail)
	if n := len(logLines(t, buf, "wal_checkpoint_failed")); n != 1 {
		t.Fatalf("warnings within a minute = %d, want 1", n)
	}
	clk.Advance(time.Minute)
	d.checkpointOnce(fail)
	if n := len(logLines(t, buf, "wal_checkpoint_failed")); n != 2 {
		t.Fatalf("warnings after a minute = %d, want 2", n)
	}
	if v, _ := d.obs.Reg.Counter(obs.MLoopRuns, map[string]string{"loop": obs.LoopWALCheckpoint, "outcome": "error"}); v != 3 {
		t.Errorf("error runs = %v", v)
	}
	if v, _ := d.obs.Reg.Gauge(obs.MWALFrames, nil); v != 0 {
		t.Errorf("gauge moved on error: %v", v)
	}
}
