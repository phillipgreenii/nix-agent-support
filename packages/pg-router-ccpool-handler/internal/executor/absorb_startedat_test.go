package executor

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dtest"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/usage"
)

// The reported start of an adopted dispatch (INV-CCH-30, DEC-EVENT-5): a
// dispatch that absorbs a session an earlier offer launched returns that
// session's own pgrouter.launched_at as the Result's StartedAt, so the dispatch
// reply carries it; a dispatch that launches its own session, and an absorbed
// session that recorded no launch time, return none.

// An absorbing dispatch (no budget, so the plain wait) reports the adopted
// session's launch time, to the second, not its own clock.
func TestAbsorb_reportsAdoptedLaunchTimeAsStartedAt(t *testing.T) {
	launched := time.Date(2026, 10, 6, 11, 27, 13, 0, time.UTC) // far from the dispatch clock (12:00:00), not on a minute
	row := absorbRow(map[string]string{
		ccpool.MetaKeyLaunchedAt: ccpool.FormatMetaTime(launched),
		ccpool.MetaKeyLeaseUntil: "2026-10-06T11:00:00Z", // handler long gone
	})
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{row}}}
	res, err := absorbTurn(t, t.TempDir(), cc, func() {})
	if err != nil {
		t.Fatalf("absorb dispatch: %v", err)
	}
	if got := cc.Ensured; len(got) != 0 {
		t.Fatalf("an absorbing dispatch must not launch a session; Ensured=%v", got)
	}
	if !res.StartedAt.Equal(launched) {
		t.Errorf("StartedAt = %v, want the adopted session's launched_at %v", res.StartedAt, launched)
	}
}

// The budget-gated absorb path (the watchdog race) reports it too.
func TestAbsorb_withBudget_reportsAdoptedLaunchTimeAsStartedAt(t *testing.T) {
	cfg := fastCfg()
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	launched := t0.Add(-5 * time.Minute) // under the 25m budget, so the watchdog never stops it
	clk := &dtest.ManualClock{T: t0}
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{absorbRow(map[string]string{
		ccpool.MetaKeyLaunchedAt: ccpool.FormatMetaTime(launched),
	})}}}
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = "att-new"
	deps.Now = clk.Now
	orig := clk.TickAdvancing()
	deps.Tick = func(ctx context.Context, d time.Duration) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
		return orig(ctx, d)
	}
	deps.UsageReader = &dtest.RampReader{Seq: []usage.Snapshot{{}}}
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := (ccpoolExecutor{}).Dispatch(ctx, timeBudgetDispatch(cfg), deps)
	if err != nil {
		t.Fatalf("absorb dispatch: %v", err)
	}
	if !res.StartedAt.Equal(launched) {
		t.Errorf("StartedAt = %v, want the adopted session's launched_at %v", res.StartedAt, launched)
	}
}

// An adopted session with no (or an unparseable) launched_at, one an older
// build launched, reports no start: the offer's own start stands.
func TestAbsorb_noLaunchedAt_reportsNoStartedAt(t *testing.T) {
	for name, meta := range map[string]map[string]string{
		"absent":      {ccpool.MetaKeyLeaseUntil: "2026-10-06T11:00:00Z"},
		"unparseable": {ccpool.MetaKeyLeaseUntil: "2026-10-06T11:00:00Z", ccpool.MetaKeyLaunchedAt: "not-a-time"},
	} {
		t.Run(name, func(t *testing.T) {
			cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{absorbRow(meta)}}}
			res, err := absorbTurn(t, t.TempDir(), cc, func() {})
			if err != nil {
				t.Fatalf("absorb dispatch: %v", err)
			}
			if got := cc.Ensured; len(got) != 0 {
				t.Fatalf("an absorbing dispatch must not launch a session; Ensured=%v", got)
			}
			if !res.StartedAt.IsZero() {
				t.Errorf("StartedAt = %v, want none for an adopted session with no launched_at", res.StartedAt)
			}
		})
	}
}

// A dispatch that launches its own session reports no start: it IS the start.
func TestDispatch_freshSession_reportsNoStartedAt(t *testing.T) {
	cfg := leaseCfg()
	clk := &dtest.ManualClock{T: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "closed"}}}
	cc := newSettleCC(leaseSess, func() ccpool.SessionState {
		if bd.Idx["zr-w"] >= 2 {
			return ccpool.StateIdle
		}
		return ccpool.StateWorking
	})
	deps := leaseDeps(t, cc, bd, cfg, clk)
	d := DispatchContext{Role: noBudget(workerRole(cfg)), Item: item.Item{ID: "zr-w"}, EventID: absorbEvent}
	res, err := (ccpoolExecutor{}).Dispatch(context.Background(), d, deps)
	if err != nil {
		t.Fatalf("fresh dispatch: %v", err)
	}
	if cc.EnsuredMeta[ccpool.MetaKeyLaunchedAt] == "" {
		t.Fatal("the fresh session must have been launched (launched_at stamped); the test would otherwise be vacuous")
	}
	if !res.StartedAt.IsZero() {
		t.Errorf("StartedAt = %v, want none for a fresh session", res.StartedAt)
	}
}
