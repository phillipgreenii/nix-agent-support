package settle

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

// The decision at the boundaries of both thresholds, with a fixed clock.
func TestRemovalDueBoundaries(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	every := 30 * 24 * time.Hour
	recent := now.Add(-every / 2)
	cases := []struct {
		name            string
		settled, quiet  int
		last            time.Time
		wantDue         bool
		wantShare       float64
		share           float64
		dueEvery        time.Duration
		lastDescription string
	}{
		{name: "nothing settled", settled: 0, quiet: 0, last: time.Time{}, share: 0.1, wantDue: false},
		{name: "none quiet and never ran is not due", settled: 100, quiet: 0, share: 0.1, wantDue: false},
		{name: "share exactly at the threshold is not due", settled: 100, quiet: 10, last: recent, share: 0.1, wantDue: false, wantShare: 0.1},
		{name: "share one over the threshold is due", settled: 100, quiet: 11, last: recent, share: 0.1, wantDue: true, wantShare: 0.11},
		{name: "a run that never completed with quiet Conversations is due", settled: 100, quiet: 1, share: 0.1, wantDue: true, wantShare: 0.01},
		{name: "last run exactly dueEvery ago is not due", settled: 100, quiet: 1, last: now.Add(-every), share: 0.1, wantDue: false, wantShare: 0.01},
		{name: "last run one nanosecond over dueEvery is due", settled: 100, quiet: 1, last: now.Add(-every - time.Nanosecond), share: 0.1, wantDue: true, wantShare: 0.01},
		{name: "stale run but nothing quiet is not due", settled: 100, quiet: 0, last: now.Add(-2 * every), share: 0.1, wantDue: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			share, due := RemovalDue(c.settled, c.quiet, c.last, now, c.share, every)
			if due != c.wantDue {
				t.Fatalf("due = %v, want %v (share %v)", due, c.wantDue, share)
			}
			if c.wantShare != 0 && (share < c.wantShare-1e-9 || share > c.wantShare+1e-9) {
				t.Fatalf("share = %v, want %v", share, c.wantShare)
			}
		})
	}
}

func gaugeOf(t *testing.T, e *env, name string) float64 {
	t.Helper()
	v, _ := e.obs.Reg.Gauge(name, nil)
	return v
}

// Real SQLite, the fake clock and the real remove path: a settled Conversation quiet past
// remove.after reads as due (no run ever completed), a dry run records nothing, a completed run
// records its time in the gauge and the state file, and a restarted Settler reads it back.
func TestRemovalDueGaugesAndLastRun(t *testing.T) {
	e := newRemoveEnv(t, false)
	e.oldSettled(5, removeAge+time.Hour)
	e.clk.Advance(removeAge + time.Hour)
	refresh := func() {
		t.Helper()
		groups, err := e.sett.groups(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		e.sett.refreshRemovalGauges(context.Background(), groups, map[string]bool{}, e.clk.Now())
	}
	refresh()
	if got := gaugeOf(t, e, obs.MRemovalQuietShare); got != 1 {
		t.Fatalf("quiet share = %v, want 1", got)
	}
	if got := gaugeOf(t, e, obs.MRemovalDue); got != 1 {
		t.Fatalf("due = %v, want 1 (quiet Conversation and no run ever completed)", got)
	}
	if got := gaugeOf(t, e, obs.MRemovalLastRun); got != 0 {
		t.Fatalf("last run = %v, want 0 before any run", got)
	}

	if _, err := e.sett.Remove(RemoveOptions{DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.state, RemovalRunFile)); err == nil {
		t.Fatal("a dry run recorded a removal run")
	}

	at := e.clk.Now()
	e.remove(sess)
	if got, want := gaugeOf(t, e, obs.MRemovalLastRun), float64(at.UnixNano())/1e9; got != want {
		t.Fatalf("last run gauge = %v, want %v", got, want)
	}
	if r, ok := LoadRemovalRun(e.state); !ok || r.AtNS != at.UnixNano() {
		t.Fatalf("removal.json = %+v ok=%v, want at_ns %d", r, ok, at.UnixNano())
	}

	// A restarted Settler on the same state directory reads the time back at its first refresh.
	e.open()
	if got := gaugeOf(t, e, obs.MRemovalLastRun); got != 0 {
		t.Fatalf("fresh Settler last run = %v before a refresh, want 0", got)
	}
	e.sett.refreshRemovalGauges(context.Background(), map[string]*conv{}, map[string]bool{}, e.clk.Now())
	if got, want := gaugeOf(t, e, obs.MRemovalLastRun), float64(at.UnixNano())/1e9; got != want {
		t.Fatalf("reloaded last run = %v, want %v", got, want)
	}
}

// An operator run that completes with nothing to remove still counts as a completed run.
func TestRemoveOfNothingStillRecordsARun(t *testing.T) {
	e := newRemoveEnv(t, false)
	if _, err := e.sett.Remove(RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadRemovalRun(e.state); !ok {
		t.Fatal("no run recorded for a completed run that removed nothing")
	}
}
