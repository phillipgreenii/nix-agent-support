package serve

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// statusOf answers the status operation in process.
func statusOf(t *testing.T, d *Daemon) wire.Status {
	t.Helper()
	body, werr := d.opStatus(context.Background(), nil)
	if werr != nil {
		t.Fatal(werr)
	}
	return body.(wire.Status)
}

func fakeClockDaemon(t *testing.T, mod func(*env)) (*env, *Daemon, *clock.Fake) {
	t.Helper()
	e := newEnv(t, true)
	if mod != nil {
		mod(e)
	}
	clk := clock.NewFake(t0)
	d, err := Open(Options{Config: e.cfg, Logger: NewLogger(e.log), NoEvents: true, Version: "test", Procs: e.procs, Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	e.d = d
	t.Cleanup(func() { _ = d.Close() })
	return e, d, clk
}

// 2 live, 1 quiet, 1 held and 1 settled Conversation: status carries all five
// counts and they equal the conversations_by_stage gauges of the one tick that
// derived them. Before that tick last_tick is absent; after it, present.
func TestStatusStageCountsHaveLiveAndQuietAndEqualTheGaugesFromOneTick(t *testing.T) {
	e, d, clk := fakeClockDaemon(t, nil)
	id := func(n int) string { return fmt.Sprintf("0a1b2c3d-0000-4000-8000-00000000030%d", n) }
	old := t0.Add(-48 * time.Hour)
	for n, age := range map[int]time.Time{1: old, 2: old, 3: t0.Add(-time.Minute), 4: old, 5: old} {
		e.transcript(id(n)+".jsonl", fmt.Sprintf("{\"type\":\"user\",\"sessionId\":%q}\n", id(n)), age)
	}
	for i, pid := range []int{481, 482} {
		e.registry(t, pid, regJSON(pid, id(i+1), t0.Add(-72*time.Hour), "idle", 1))
		e.procs.Add(pid, t0.Add(-72*time.Hour), nil)
	}
	e.settle(t) // copy and parse everything
	// An append the copier has not seen holds the fourth Conversation.
	p := filepath.Join(e.claude, "projects", "example-proj", id(4)+".jsonl")
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString("{\"type\":\"user\"}\n")
	_ = f.Close()
	_ = os.Chtimes(p, old, old)

	before := statusOf(t, d)
	if before.LastTick != nil {
		t.Fatalf("last_tick before any tick: %+v", before.LastTick)
	}
	if len(before.Loops) != len(obs.Loops) {
		t.Fatalf("loops before any tick: %d entries, want %d", len(before.Loops), len(obs.Loops))
	}

	clk.Advance(3 * time.Second)
	d.runTick(d.sett.Tick)
	st := statusOf(t, d)
	want := map[string]int{"live": 2, "quiet": 1, "held": 1, "settled": 1}
	for stage, n := range want {
		if st.StageCounts[stage] != n {
			t.Errorf("status %s = %d, want %d (all: %v)", stage, st.StageCounts[stage], n, st.StageCounts)
		}
	}
	for stage, n := range st.StageCounts {
		g, _ := d.obs.Reg.Gauge(obs.MConversationsByStage, map[string]string{"stage": stage})
		if int(g) != n {
			t.Errorf("status %s = %d, gauge %v", stage, n, g)
		}
	}
	if st.LastTick == nil || st.LastTick.Examined != 5 || st.LastTick.AgeS != 0 {
		t.Fatalf("last_tick after a tick: %+v", st.LastTick)
	}
	clk.Advance(7 * time.Second)
	if got := statusOf(t, d).LastTick; got == nil || got.AgeS != 7 {
		t.Fatalf("last_tick age after 7s: %+v", got)
	}
}

// The loops table lists all 11 loops; a loop that has not run has no age, and a
// loop that has shows its last duration, outcome and age.
func TestStatusLoopsTableCoversAllElevenLoops(t *testing.T) {
	_, d, clk := fakeClockDaemon(t, nil)
	if err := d.runLoop(obs.LoopDoctor, func() error { clk.Advance(1500 * time.Millisecond); return nil }); err != nil {
		t.Fatal(err)
	}
	_ = d.runLoop(obs.LoopTrackerPoll, func() error { return fmt.Errorf("boom") })
	clk.Advance(9 * time.Second)
	st := statusOf(t, d)
	if len(st.Loops) != 11 {
		t.Fatalf("%d loops, want 11: %v", len(st.Loops), st.Loops)
	}
	doc := st.Loops[obs.LoopDoctor]
	if doc.LastDurationMS == nil || *doc.LastDurationMS != 1500 || doc.LastOutcome != obs.OutcomeOK || doc.AgeS == nil || *doc.AgeS != 9 || doc.InFlightS != 0 {
		t.Fatalf("doctor: %+v", doc)
	}
	if p := st.Loops[obs.LoopTrackerPoll]; p.LastOutcome != obs.OutcomeError {
		t.Fatalf("tracker_poll outcome %q, want error", p.LastOutcome)
	}
	if n := st.Loops[obs.LoopWALCheckpoint]; n.LastDurationMS != nil || n.AgeS != nil || n.LastOutcome != "" || n.Stalled {
		t.Fatalf("a loop that never ran: %+v", n)
	}
}

// The stalled marker follows the bounds defined in obs: the age bound of the
// loops that have one, the 600 s in-flight rule for any loop, never an age for
// the rest or for parser_drain, and never for a loop that has not run.
func TestStatusStalledMarkerFollowsTheBounds(t *testing.T) {
	_, d, clk := fakeClockDaemon(t, func(e *env) { e.cfg.SweepInterval = 10 * time.Minute })
	noop := func() error { return nil }
	for _, l := range obs.PassLoops() {
		_ = d.runLoop(l, noop)
	}
	stalled := func() map[string]bool {
		out := map[string]bool{}
		for l, s := range statusOf(t, d).Loops {
			out[l] = s.Stalled
		}
		return out
	}
	for l, s := range stalled() {
		if s {
			t.Fatalf("%s stalled right after a pass", l)
		}
	}
	bounds := map[string]time.Duration{
		obs.LoopWatcherSweep: 30 * time.Minute, obs.LoopTrackerTick: 120 * time.Second,
		obs.LoopTrackerPoll: 120 * time.Second, obs.LoopStatusRefresh: 60 * time.Second,
	}
	for l, b := range bounds {
		if got, ok := obs.StallAgeBound(l, 10*time.Minute); !ok || got != b {
			t.Fatalf("StallAgeBound(%s) = %v %v, want %v", l, got, ok, b)
		}
	}
	// Just inside every bound: nothing stalled; just outside: exactly those with a bound.
	clk.Advance(60 * time.Second)
	if s := stalled(); s[obs.LoopStatusRefresh] {
		t.Fatal("status_refresh stalled at exactly its bound")
	}
	clk.Advance(time.Second)
	if s := stalled(); !s[obs.LoopStatusRefresh] || s[obs.LoopTrackerTick] {
		t.Fatalf("at 61s: %v", s)
	}
	clk.Advance(60 * time.Second)
	if s := stalled(); !s[obs.LoopTrackerTick] || !s[obs.LoopTrackerPoll] || s[obs.LoopWatcherSweep] {
		t.Fatalf("at 121s: %v", s)
	}
	clk.Advance(30 * time.Minute)
	s := stalled()
	for _, l := range obs.PassLoops() {
		_, bounded := bounds[l]
		if s[l] != bounded {
			t.Errorf("%s stalled=%v after 30 minutes, want %v", l, s[l], bounded)
		}
	}
	if s[obs.LoopParserDrain] || s[obs.LoopSettleTick] {
		t.Fatal("a loop without an age bound was marked stalled by age")
	}
}

// A pass running for more than 600 s marks its loop stalled, whatever the loop.
func TestStatusStalledMarkerOnALongRunningPass(t *testing.T) {
	_, d, clk := fakeClockDaemon(t, nil)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		_ = d.runLoop(obs.LoopSettleTick, func() error { close(started); <-release; return nil })
	}()
	<-started
	clk.Advance(600 * time.Second)
	if l := statusOf(t, d).Loops[obs.LoopSettleTick]; l.Stalled || l.InFlightS != 600 {
		t.Fatalf("at 600s: %+v", l)
	}
	clk.Advance(2 * time.Second)
	if l := statusOf(t, d).Loops[obs.LoopSettleTick]; !l.Stalled || l.InFlightS != 602 || l.AgeS != nil {
		t.Fatalf("at 602s: %+v", l)
	}
	close(release)
	<-done
	if l := statusOf(t, d).Loops[obs.LoopSettleTick]; l.Stalled || l.InFlightS != 0 {
		t.Fatalf("after the pass ended: %+v", l)
	}
}

// copier_ingest has no runLoop pass; its last event still shows in the table.
func TestStatusLoopsTableShowsTheLastIngest(t *testing.T) {
	e, d, clk := fakeClockDaemon(t, nil)
	if l := statusOf(t, d).Loops[obs.LoopCopierIngest]; l.AgeS != nil {
		t.Fatalf("ingest before any event: %+v", l)
	}
	p := e.transcript("0a1b2c3d-0000-4000-8000-000000000401.jsonl", "{\"type\":\"user\"}\n", t0)
	d.ingest(p)
	clk.Advance(4 * time.Second)
	if l := statusOf(t, d).Loops[obs.LoopCopierIngest]; l.AgeS == nil || *l.AgeS != 4 || l.LastOutcome != obs.OutcomeOK || l.LastDurationMS == nil {
		t.Fatalf("ingest after an event: %+v", l)
	}
}
