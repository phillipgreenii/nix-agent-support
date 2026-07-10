package daemon

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// pctTree builds a minimal tree carrying only the authoritative 5h reading, as
// the daemon tick loop sees it post-applyLimits.
func pctTree(pct *float64, reset time.Time) *aggregate.Tree {
	return &aggregate.Tree{FiveHourPct: pct, FiveHourResetsAt: reset}
}

func f64(v float64) *float64 { return &v }

// TestBlockLimitHit_CrossingFiresExactlyOnce proves the account-level block
// limit-hit fires exactly once when the authoritative FiveHourPct crosses to
// >=100 and does NOT re-fire while it stays >=100 across ticks (ADR 0024 D3/R7).
func TestBlockLimitHit_CrossingFiresExactlyOnce(t *testing.T) {
	var latch limitHitLatch
	reset := time.Unix(1782958200, 0)
	fires := 0
	tick := func(pct *float64) {
		tr := pctTree(pct, reset)
		if latch.observe(blockLimitTrigger(tr), tr.FiveHourResetsAt) {
			fires++
		}
	}

	tick(f64(50))  // below cap → no fire
	tick(f64(100)) // crossing → fire
	tick(f64(150)) // stays over → no re-fire
	tick(f64(436)) // stays over → no re-fire

	if fires != 1 {
		t.Fatalf("fires = %d, want exactly 1 (crossing fires once, staying over does not re-fire)", fires)
	}
}

// TestBlockLimitHit_NewResetReArms proves a changed FiveHourResetsAt re-arms the
// latch so a subsequent >=100 in the new window fires again (ADR 0024 R7).
func TestBlockLimitHit_NewResetReArms(t *testing.T) {
	var latch limitHitLatch
	r1 := time.Unix(1782958200, 0)
	r2 := r1.Add(5 * time.Hour) // next window
	fires := 0
	tick := func(pct *float64, reset time.Time) {
		tr := pctTree(pct, reset)
		if latch.observe(blockLimitTrigger(tr), tr.FiveHourResetsAt) {
			fires++
		}
	}

	tick(f64(120), r1) // fire (window 1)
	tick(f64(300), r1) // no re-fire (same window)
	if fires != 1 {
		t.Fatalf("after window 1: fires = %d, want 1", fires)
	}
	tick(f64(110), r2) // new window → re-arm → fire again
	if fires != 2 {
		t.Fatalf("after re-arm: fires = %d, want 2 (new FiveHourResetsAt re-arms)", fires)
	}
	tick(f64(200), r2) // no re-fire in window 2
	if fires != 2 {
		t.Fatalf("staying over in window 2: fires = %d, want 2", fires)
	}
}

// TestBlockLimitHit_NilOrBelowNeverFires proves a nil (unknown) or <100
// FiveHourPct never fires, and nil never panics (ADR 0024 D3).
func TestBlockLimitHit_NilOrBelowNeverFires(t *testing.T) {
	var latch limitHitLatch
	fires := 0
	obs := func(tr *aggregate.Tree) {
		if latch.observe(blockLimitTrigger(tr), tr.FiveHourResetsAt) {
			fires++
		}
	}

	obs(&aggregate.Tree{})                   // FiveHourPct nil → unknown
	obs(pctTree(f64(99.9), time.Unix(1, 0))) // just below 100
	obs(pctTree(f64(0), time.Unix(1, 0)))    // real 0%
	obs(pctTree(nil, time.Time{}))           // nil, zero reset

	if fires != 0 {
		t.Fatalf("fires = %d, want 0 (nil/<100 must never fire)", fires)
	}
}

// TestBlockLimitHit_TerminalUsageLimitFires proves that a session blocked on a
// terminal usage-limit fires the block limit-hit even when FiveHourPct is
// unknown (ADR 0024 D3: per-session usage_limit is an authoritative signal).
func TestBlockLimitHit_TerminalUsageLimitFires(t *testing.T) {
	tree := &aggregate.Tree{
		Dirs: []*aggregate.Directory{
			{
				Sessions: []*aggregate.SessionView{
					{Session: &session.Session{SessionID: "s1", Status: session.Working}},
					{Session: &session.Session{SessionID: "s2", Status: session.Blocked, Blocker: session.UsageLimit}},
				},
			},
		},
		// FiveHourPct intentionally nil (unknown).
	}
	if !blockLimitTrigger(tree) {
		t.Fatal("blockLimitTrigger = false; a session blocked on usage_limit must trigger the block limit-hit")
	}

	var latch limitHitLatch
	if !latch.observe(blockLimitTrigger(tree), tree.FiveHourResetsAt) {
		t.Fatal("first observe of a usage_limit session must fire")
	}
	if latch.observe(blockLimitTrigger(tree), tree.FiveHourResetsAt) {
		t.Fatal("second observe (same zero-reset window) must not re-fire")
	}
}

// TestBlockLimitTrigger_NoUsageLimitBlockerNoFire: a session blocked on a
// non-usage_limit blocker (e.g. human input) does not trigger the limit-hit.
func TestBlockLimitTrigger_NoUsageLimitBlockerNoFire(t *testing.T) {
	tree := &aggregate.Tree{
		Dirs: []*aggregate.Directory{
			{Sessions: []*aggregate.SessionView{
				{Session: &session.Session{SessionID: "s1", Status: session.Blocked, Blocker: session.HumanInput}},
			}},
		},
	}
	if blockLimitTrigger(tree) {
		t.Fatal("a human-input block must not trigger the usage limit-hit")
	}
}

// TestWeekLimitHit_NilGuard proves the weekly detection nil-guards SevenDayPct:
// a nil reading never fires and never panics (ADR 0024 R11), while >=100 fires.
func TestWeekLimitHit_NilGuard(t *testing.T) {
	// nil SevenDayPct → no fire, no panic.
	if weekLimitTrigger(&aggregate.Tree{}) {
		t.Fatal("nil SevenDayPct must not trigger the weekly limit-hit")
	}
	if weekLimitTrigger(nil) {
		t.Fatal("nil tree must not trigger the weekly limit-hit")
	}
	// Below 100 → no fire.
	if weekLimitTrigger(&aggregate.Tree{SevenDayPct: f64(83.3)}) {
		t.Fatal("SevenDayPct < 100 must not fire")
	}

	// >=100 fires exactly once per window; a nil-guarded latch still latches.
	var latch limitHitLatch
	tr := &aggregate.Tree{SevenDayPct: f64(100), SevenDayResetsAt: time.Unix(1783000000, 0)}
	fires := 0
	if latch.observe(weekLimitTrigger(tr), tr.SevenDayResetsAt) {
		fires++
	}
	if latch.observe(weekLimitTrigger(tr), tr.SevenDayResetsAt) {
		fires++
	}
	if fires != 1 {
		t.Fatalf("weekly fires = %d, want 1 (>=100 fires once per window)", fires)
	}
}
