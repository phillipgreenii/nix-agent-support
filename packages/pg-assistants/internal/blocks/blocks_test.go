package blocks_test

import (
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/blocks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/control"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/ladder"
)

var (
	cfg = config.Blocks{
		Points:           config.Points{Nudge: 0.95, Interrupt: 0.98, Kill: 1.0},
		UnknownResetHold: time.Hour,
	}
	t0    = time.Date(2026, 11, 16, 15, 0, 0, 0, time.UTC)
	reset = time.Date(2026, 11, 16, 20, 0, 0, 0, time.UTC)
)

func five(f float64, resetsAt, observed time.Time) control.Limit {
	return control.Limit{Kind: "five_hour", Fraction: f, ResetsAt: resetsAt, ObservedAt: observed}
}

func seven(f float64, resetsAt, observed time.Time) control.Limit {
	return control.Limit{Kind: "seven_day", Fraction: f, ResetsAt: resetsAt, ObservedAt: observed}
}

// fractions renders the current readings as "kind=fraction" for comparison.
func fractions(tr *blocks.Tracker, now time.Time) []string {
	var out []string
	for _, l := range tr.Snapshot(now) {
		out = append(out, fmt.Sprintf("%s=%v", l.Kind, l.Fraction))
	}
	return out
}

func checkHold(t *testing.T, tr *blocks.Tracker, now time.Time, want bool, until time.Time) {
	t.Helper()
	held, u := tr.Hold(now)
	if held != want || !u.Equal(until) {
		t.Errorf("Hold(%v) = %v, %v; want %v, %v", now, held, u, want, until)
	}
}

func TestMetersCarryBlockPoints(t *testing.T) {
	tr := blocks.NewTracker(cfg)
	tr.Observe([]control.Limit{five(0.96, reset, t0), seven(0.40, time.Time{}, t0)})
	got := tr.Meters(t0.Add(time.Minute))
	want := []ladder.Meter{
		{Name: "five_hour", Fraction: 0.96, Points: config.Points{Nudge: 0.95, Interrupt: 0.98, Kill: 1.0}, Detail: "resets 2026-11-16T20:00:00Z"},
		{Name: "seven_day", Fraction: 0.40, Points: config.Points{Nudge: 0.95, Interrupt: 0.98, Kill: 1.0}},
	}
	if !slices.Equal(got, want) {
		t.Errorf("meters = %+v\nwant     %+v", got, want)
	}
}

func TestExpiredReadingDiscarded(t *testing.T) {
	tr := blocks.NewTracker(cfg)
	tr.Observe([]control.Limit{five(0.99, reset, t0)})
	for _, now := range []time.Time{reset, reset.Add(time.Second)} {
		if m := tr.Meters(now); len(m) != 0 {
			t.Errorf("Meters(%v) = %+v, want none after the reset", now, m)
		}
		if s := tr.Snapshot(now); len(s) != 0 {
			t.Errorf("Snapshot(%v) = %+v, want none after the reset", now, s)
		}
		checkHold(t, tr, now, false, time.Time{})
	}
}

// TestExpiredReadingIsReplaced: once a reading's reset (or, without one, its
// unknown-reset hold) has passed at a new reading's observation time, the new
// reading replaces it whatever the merge rule would say.
func TestExpiredReadingIsReplaced(t *testing.T) {
	tr := blocks.NewTracker(cfg)
	tr.Observe([]control.Limit{five(0.99, reset, t0)})
	after := reset.Add(time.Minute)
	tr.Observe([]control.Limit{five(0.10, time.Time{}, after)})
	if got := fractions(tr, after); !slices.Equal(got, []string{"five_hour=0.1"}) {
		t.Errorf("after the reset: %v, want the new zero-reset reading", got)
	}

	tr = blocks.NewTracker(cfg)
	tr.Observe([]control.Limit{five(0.99, time.Time{}, t0)})
	later := t0.Add(time.Hour)
	tr.Observe([]control.Limit{five(0.10, time.Time{}, later)})
	if got := fractions(tr, later); !slices.Equal(got, []string{"five_hour=0.1"}) {
		t.Errorf("after the unknown-reset hold: %v, want the new reading", got)
	}
}

func TestHoldAtNudgeLevel(t *testing.T) {
	tr := blocks.NewTracker(cfg)
	tr.Observe([]control.Limit{five(0.95, reset, t0)})
	checkHold(t, tr, t0, true, reset)
	checkHold(t, tr, reset.Add(-time.Second), true, reset)
	checkHold(t, tr, reset, false, time.Time{})

	tr = blocks.NewTracker(cfg)
	tr.Observe([]control.Limit{five(0.94, reset, t0)})
	checkHold(t, tr, t0, false, time.Time{})
}

func TestHoldUntilTheLatestHeldReset(t *testing.T) {
	tr := blocks.NewTracker(cfg)
	weekEnd := reset.Add(72 * time.Hour)
	tr.Observe([]control.Limit{five(0.97, reset, t0), seven(0.95, weekEnd, t0)})
	checkHold(t, tr, t0, true, weekEnd)

	tr = blocks.NewTracker(cfg)
	tr.Observe([]control.Limit{five(0.97, reset, t0), seven(0.50, weekEnd, t0)})
	checkHold(t, tr, t0, true, reset)
}

func TestHoldUnknownReset(t *testing.T) {
	tr := blocks.NewTracker(cfg)
	tr.Observe([]control.Limit{five(0.96, time.Time{}, t0)})
	checkHold(t, tr, t0.Add(30*time.Minute), true, t0.Add(time.Hour))
	checkHold(t, tr, t0.Add(time.Hour), false, time.Time{})
}

func TestNoReadingNoHold(t *testing.T) {
	tr := blocks.NewTracker(cfg)
	checkHold(t, tr, t0, false, time.Time{})
	if m := tr.Meters(t0); len(m) != 0 {
		t.Errorf("meters = %+v, want none", m)
	}
	if s := tr.Snapshot(t0); len(s) != 0 {
		t.Errorf("snapshot = %+v, want none", s)
	}
	tr.Observe(nil)
	checkHold(t, tr, t0, false, time.Time{})
}

func TestStaleLowerReadingDoesNotLowerHold(t *testing.T) {
	tr := blocks.NewTracker(cfg)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }

	tr.Observe([]control.Limit{five(0.96, reset, at(0))})
	tr.Observe([]control.Limit{five(0.70, reset, at(1))}) // an idle session's stale figure
	if got := fractions(tr, at(1)); !slices.Equal(got, []string{"five_hour=0.96"}) {
		t.Fatalf("same reset, lower fraction: %v, want 0.96 kept", got)
	}
	checkHold(t, tr, at(1), true, reset)

	tr.Observe([]control.Limit{five(0.99, time.Time{}, at(2))})
	if got := fractions(tr, at(2)); !slices.Equal(got, []string{"five_hour=0.96"}) {
		t.Fatalf("zero reset: %v, want the reading with a reset kept", got)
	}

	// a later reset that a five-hour block can still have (TestImplausibleResetIsUnknown)
	next := reset.Add(30 * time.Minute)
	tr.Observe([]control.Limit{five(0.10, next, at(3))})
	if got := fractions(tr, at(3)); !slices.Equal(got, []string{"five_hour=0.1"}) {
		t.Fatalf("later reset: %v, want 0.10", got)
	}
	checkHold(t, tr, at(3), false, time.Time{})

	tr.Observe([]control.Limit{five(0.99, time.Time{}, at(4))})
	tr.Observe([]control.Limit{five(0.96, reset, at(5))}) // an older block, not yet past its reset
	if got := fractions(tr, at(5)); !slices.Equal(got, []string{"five_hour=0.1"}) {
		t.Fatalf("zero or earlier reset: %v, want 0.10 kept", got)
	}

	tr.Observe([]control.Limit{five(0.30, next, at(6))})
	if got := fractions(tr, at(6)); !slices.Equal(got, []string{"five_hour=0.3"}) {
		t.Fatalf("same reset, higher fraction: %v, want 0.30", got)
	}
}

func TestEqualReadingRefreshesObservation(t *testing.T) {
	tr := blocks.NewTracker(cfg)
	tr.Observe([]control.Limit{five(0.96, time.Time{}, t0)})
	later := t0.Add(30 * time.Minute)
	tr.Observe([]control.Limit{five(0.96, time.Time{}, later)})
	checkHold(t, tr, later, true, later.Add(time.Hour))
}

func TestObserveIgnoresInvalidReadings(t *testing.T) {
	tr := blocks.NewTracker(cfg)
	tr.Observe([]control.Limit{
		{Kind: "spend", Fraction: 0.99, ResetsAt: reset, ObservedAt: t0},
		five(1.5, reset, t0),
		five(-0.1, reset, t0),
		seven(0.2, reset, t0),
	})
	if got := fractions(tr, t0); !slices.Equal(got, []string{"seven_day=0.2"}) {
		t.Errorf("readings = %v, want only the valid seven_day", got)
	}
}

func TestSeedDropsExpired(t *testing.T) {
	now := t0.Add(2 * time.Hour)
	tr := blocks.NewTracker(cfg)
	tr.Seed([]control.Limit{
		five(0.97, now.Add(-time.Minute), t0),              // reset passed
		seven(0.96, time.Time{}, now.Add(-30*time.Minute)), // unknown reset, still held
	}, now)
	if got := fractions(tr, now); !slices.Equal(got, []string{"seven_day=0.96"}) {
		t.Fatalf("seeded = %v, want only seven_day", got)
	}
	checkHold(t, tr, now, true, now.Add(30*time.Minute))

	tr = blocks.NewTracker(cfg)
	tr.Seed([]control.Limit{
		seven(0.96, time.Time{}, now.Add(-2*time.Hour)), // unknown reset, hold over
		five(0.97, reset, t0),                           // reset ahead
	}, now)
	if got := fractions(tr, now); !slices.Equal(got, []string{"five_hour=0.97"}) {
		t.Fatalf("seeded = %v, want only five_hour", got)
	}
	checkHold(t, tr, now, true, reset)

	// An expired reading with a reset time would otherwise displace a current
	// one without (a reset time beats none), and the hold would be lost.
	tr = blocks.NewTracker(cfg)
	tr.Seed([]control.Limit{
		seven(0.96, time.Time{}, now.Add(-30*time.Minute)), // unknown reset, still held
		seven(0.97, now.Add(-time.Minute), t0),             // reset passed
	}, now)
	if got := fractions(tr, now); !slices.Equal(got, []string{"seven_day=0.96"}) {
		t.Fatalf("seeded = %v, want the held seven_day kept", got)
	}
	checkHold(t, tr, now, true, now.Add(30*time.Minute))
}

// TestSeedThenObserveMerges: a seeded reading is merged like any other, so a
// fresh lower reading for the same block does not lower the hold.
func TestSeedThenObserveMerges(t *testing.T) {
	tr := blocks.NewTracker(cfg)
	tr.Seed([]control.Limit{five(0.97, reset, t0)}, t0)
	tr.Observe([]control.Limit{five(0.50, reset, t0.Add(time.Minute))})
	checkHold(t, tr, t0.Add(time.Minute), true, reset)
}

func TestSnapshotIsACopy(t *testing.T) {
	tr := blocks.NewTracker(cfg)
	tr.Observe([]control.Limit{five(0.97, reset, t0)})
	s := tr.Snapshot(t0)
	s[0].Fraction = 0
	if got := fractions(tr, t0); !slices.Equal(got, []string{"five_hour=0.97"}) {
		t.Errorf("after editing the snapshot: %v", got)
	}
}

func TestConcurrentObserveAndRead(t *testing.T) {
	tr := blocks.NewTracker(cfg)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 100 {
				f := float64(i*100+j) / 1000
				tr.Observe([]control.Limit{five(f, reset, t0), seven(f, time.Time{}, t0)})
			}
		})
		wg.Go(func() {
			for range 100 {
				_ = tr.Meters(t0)
				_, _ = tr.Hold(t0)
				_ = tr.Snapshot(t0)
			}
		})
	}
	wg.Wait()
	if got := fractions(tr, t0); !slices.Equal(got, []string{"five_hour=0.799", "seven_day=0.799"}) {
		t.Errorf("after the race: %v, want the highest fraction of each kind", got)
	}
}

// A reset time further out than the block can last (its length plus an hour
// of slack) is not believed: the reading is kept as one with no reset time, so
// its fraction still counts and a held block holds starts for the unknown
// reset hold, not for days. A plausible reset time is kept as it is.
func TestImplausibleResetIsUnknown(t *testing.T) {
	cases := []struct {
		name  string
		l     control.Limit
		reset time.Time // what the tracker keeps
	}{
		{"five_hour past its length", five(0.96, t0.Add(6*time.Hour+time.Minute), t0), time.Time{}},
		{"five_hour within its length", five(0.96, t0.Add(6*time.Hour), t0), t0.Add(6 * time.Hour)},
		{"seven_day past its length", seven(0.96, t0.Add(7*24*time.Hour+time.Hour+time.Second), t0), time.Time{}},
		{"seven_day within its length", seven(0.96, t0.Add(7*24*time.Hour), t0), t0.Add(7 * 24 * time.Hour)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, way := range []string{"observe", "seed"} {
				tr := blocks.NewTracker(cfg)
				if way == "observe" {
					tr.Observe([]control.Limit{tc.l})
				} else {
					tr.Seed([]control.Limit{tc.l}, t0)
				}
				snap := tr.Snapshot(t0.Add(time.Minute))
				if len(snap) != 1 || !snap[0].ResetsAt.Equal(tc.reset) || snap[0].Fraction != 0.96 {
					t.Fatalf("%s: snapshot = %+v, want the reading with reset %v", way, snap, tc.reset)
				}
				until := tc.reset
				if until.IsZero() {
					until = t0.Add(cfg.UnknownResetHold)
				}
				checkHold(t, tr, t0.Add(time.Minute), true, until)
			}
		})
	}
	// a reading with a plausible reset time then replaces the unbelieved one
	tr := blocks.NewTracker(cfg)
	tr.Observe([]control.Limit{five(0.99, t0.Add(30*24*time.Hour), t0)})
	tr.Observe([]control.Limit{five(0.50, reset, t0.Add(time.Minute))})
	if got := fractions(tr, t0.Add(2*time.Minute)); !slices.Equal(got, []string{"five_hour=0.5"}) {
		t.Errorf("readings = %q, want the one with a plausible reset time", got)
	}
}
