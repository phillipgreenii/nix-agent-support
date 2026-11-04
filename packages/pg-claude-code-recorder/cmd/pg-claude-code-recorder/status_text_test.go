package main

import (
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

var updateStatusGolden = flag.Bool("update", false, "rewrite testdata/status.golden")

func i64(n int64) *int64 { return &n }

func statusWithLoops() wire.Status {
	loops := map[string]wire.LoopStatus{}
	for _, l := range obs.Loops {
		loops[l] = wire.LoopStatus{}
	}
	loops[obs.LoopSettleTick] = wire.LoopStatus{LastDurationMS: i64(412), LastOutcome: "ok", AgeS: i64(3)}
	loops[obs.LoopWatcherSweep] = wire.LoopStatus{LastDurationMS: i64(9000), LastOutcome: "error", AgeS: i64(4000), Stalled: true}
	loops[obs.LoopDoctor] = wire.LoopStatus{InFlightS: 700, Stalled: true}
	return wire.Status{
		Healthy: true, Verdict: "ok: example", Short: "ok", CaughtUp: true, FreeBytes: 1000, SchemaVersion: 3, ProtocolVer: 1,
		StageCounts: map[string]int{"live": 2, "quiet": 1, "held": 0, "settled": 5, "removal_pending": 0, "removed": 0},
		LastTick:    &wire.LastTick{DurationMS: 412, Examined: 8, AgeS: 3},
		Loops:       loops,
	}
}

// The status text shows the stage counts, the last tick and one line per loop
// under a loops heading, with a marker on a stalled loop.
func TestStatusTextShowsStagesLastTickAndLoops(t *testing.T) {
	st := statusWithLoops()
	sock := fakeDaemon(t, func() wire.Status { return st }, func() wire.Coverage { return wire.Coverage{} })
	a, out, _ := testApp(nil)
	if code := a.run([]string{"status", "--socket", sock}); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	const golden = "testdata/status.golden"
	if *updateStatusGolden {
		if err := os.WriteFile(golden, out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != string(want) {
		t.Fatalf("status text differs from %s (rerun with -update to review):\ngot:\n%s\nwant:\n%s", golden, out.String(), want)
	}
	if n := strings.Count(out.String(), "STALLED"); n != 2 {
		t.Fatalf("%d stalled markers, want 2", n)
	}
}

// Before the first tick the daemon sends no last_tick and the text prints no
// last tick line; the loops that never ran say so.
func TestStatusTextWithoutALastTick(t *testing.T) {
	st := statusWithLoops()
	st.LastTick = nil
	sock := fakeDaemon(t, func() wire.Status { return st }, func() wire.Coverage { return wire.Coverage{} })
	a, out, _ := testApp(nil)
	if code := a.run([]string{"status", "--socket", sock}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.Contains(out.String(), "last tick:") || !strings.Contains(out.String(), "never run") {
		t.Fatalf("%s", out.String())
	}
}
