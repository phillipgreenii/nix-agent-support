package executor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/budget"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dtest"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/report"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/sessionlock"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/usage"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/watchdog"
)

// Supervision lease (bead pg2-g2u9m, INV-CCH-18): the dispatching handler
// stamps pgrouter.lease_until at launch and refreshes it every PollInterval
// until Dispatch returns, so a later dispatch can tell a supervised session
// from an orphan.

const (
	leaseSess = "sess-1"
	leaseTTL  = 2 * time.Minute
)

func leaseCfg() config.Config {
	cfg := fastCfg()
	cfg.PollInterval = 5 * time.Millisecond // the real lease ticker period
	cfg.LeaseTTL = leaseTTL
	cfg.WorktreeQuietWindow = time.Minute
	return cfg
}

// phaseCC hooks the dispatch phases a lease must cover.
type phaseCC struct {
	*settleCC
	onSend, onClose func()
}

func (c *phaseCC) Send(ctx context.Context, id, p string, m ccpool.SendMode) error {
	if c.onSend != nil {
		c.onSend()
	}
	return c.settleCC.Send(ctx, id, p, m)
}

func (c *phaseCC) Close(ctx context.Context, id string, purge bool) error {
	if c.onClose != nil {
		c.onClose()
	}
	return c.settleCC.Close(ctx, id, purge)
}

// phaseBD hooks the first bead read (the start of waitDone).
type phaseBD struct {
	*dtest.ScriptBD
	once    sync.Once
	onFirst func()
}

func (b *phaseBD) Run(ctx context.Context, args ...string) (string, error) {
	if args[0] == "show" && b.onFirst != nil {
		b.once.Do(b.onFirst)
	}
	return b.ScriptBD.Run(ctx, args...)
}

// waitMoreLeaseWrites blocks until the lease ticker has written n more times.
func waitMoreLeaseWrites(t *testing.T, cc *dtest.FakeCC, phase string, n int) {
	t.Helper()
	base := len(cc.SetMetaCalls())
	deadline := time.Now().Add(5 * time.Second)
	for len(cc.SetMetaCalls()) < base+n {
		if time.Now().After(deadline) {
			t.Errorf("lease was not refreshed %d more times during %s", n, phase)
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func leaseDeps(t *testing.T, cc ccpool.Runner, bd interface {
	Run(context.Context, ...string) (string, error)
}, cfg config.Config, clk *dtest.ManualClock,
) Deps {
	t.Helper()
	cfg.WorktreeDir = t.TempDir()
	deps := newExec(&dtest.FakeCC{}, &dtest.ScriptBD{}, cfg).deps
	deps.CC = cc
	deps.BD = bd
	deps.ExternalID = leaseSess
	deps.Now, deps.Tick = clk.Now, clk.TickAdvancing()
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	return deps
}

func TestLease_initialMetaCarriesLeaseAndLaunchTime(t *testing.T) {
	cfg := leaseCfg()
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	clk := &dtest.ManualClock{T: t0}
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "closed"}}}
	cc := newSettleCC(leaseSess, func() ccpool.SessionState {
		if bd.Idx["zr-w"] >= 2 {
			return ccpool.StateIdle
		}
		return ccpool.StateWorking
	})
	deps := leaseDeps(t, cc, bd, cfg, clk)
	d := DispatchContext{Role: noBudget(workerRole(cfg)), Item: item.Item{ID: "zr-w"}}
	if _, err := (ccpoolExecutor{}).Dispatch(context.Background(), d, deps); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	m := cc.EnsuredMeta
	if m[ccpool.MetaKeyLaunchedAt] != "2026-10-06T12:00:00Z" {
		t.Errorf("launched_at = %q", m[ccpool.MetaKeyLaunchedAt])
	}
	// launch + ensure timeout (12m) + TTL (2m)
	if m[ccpool.MetaKeyLeaseUntil] != "2026-10-06T12:14:00Z" {
		t.Errorf("initial lease_until = %q, want 12:14:00Z (launch + 12m + 2m)", m[ccpool.MetaKeyLeaseUntil])
	}
}

// The ticker refreshes the lease every PollInterval through Send, waitDone, the
// worktree cleanup's quiet wait and the settled-session close, and stops when
// Dispatch returns.
func TestLease_refreshedThroughEveryPhaseThenStops(t *testing.T) {
	cfg := leaseCfg()
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	clk := &dtest.ManualClock{T: t0}
	sbd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "closed"}}}
	settle := newSettleCC(leaseSess, func() ccpool.SessionState {
		if sbd.Idx["zr-w"] >= 2 {
			return ccpool.StateIdle
		}
		return ccpool.StateWorking
	})
	settle.transcript = "/t"
	cc := &phaseCC{settleCC: settle}
	cc.onSend = func() { waitMoreLeaseWrites(t, settle.FakeCC, "Send", 3) }
	cc.onClose = func() { waitMoreLeaseWrites(t, settle.FakeCC, "the settled-session close", 3) }
	bd := &phaseBD{ScriptBD: sbd, onFirst: func() { waitMoreLeaseWrites(t, settle.FakeCC, "waitDone", 3) }}
	deps := leaseDeps(t, cc, bd, cfg, clk)
	deps.LatestActivity = func(string) (time.Time, bool) {
		waitMoreLeaseWrites(t, settle.FakeCC, "the worktree cleanup quiet wait", 3)
		return t0.Add(-time.Hour), true // long quiet
	}
	d := DispatchContext{Role: noBudget(workerRole(cfg)), Item: item.Item{ID: "zr-w"}}
	if _, err := (ccpoolExecutor{}).Dispatch(context.Background(), d, deps); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	calls := settle.FakeCC.SetMetaCalls()
	if len(calls) < 12 {
		t.Fatalf("want >= 12 lease writes across the four phases, got %d", len(calls))
	}
	for _, c := range calls {
		if c.ExternalID != leaseSess || c.Key != ccpool.MetaKeyLeaseUntil {
			t.Fatalf("unexpected SetMeta %+v", c)
		}
		until, ok := ccpool.ParseMetaTime(c.Value)
		if !ok || until.Before(t0.Add(leaseTTL)) {
			t.Fatalf("lease value %q must parse and be >= launch + TTL", c.Value)
		}
	}
	// Stopped when Dispatch returned.
	n := len(calls)
	time.Sleep(60 * time.Millisecond)
	if got := len(settle.FakeCC.SetMetaCalls()); got != n {
		t.Errorf("lease ticker still running after Dispatch returned: %d -> %d writes", n, got)
	}
}

// A lease-write failure never fails the dispatch; it logs WARN, and ERROR once
// failures are consecutive enough to matter.
func TestLease_writeFailureDoesNotFailDispatchAndEscalatesToError(t *testing.T) {
	cfg := leaseCfg()
	clk := &dtest.ManualClock{T: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	sbd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "closed"}}}
	settle := newSettleCC(leaseSess, func() ccpool.SessionState {
		if sbd.Idx["zr-w"] >= 2 {
			return ccpool.StateIdle
		}
		return ccpool.StateWorking
	})
	settle.SetMetaErr = errors.New("ccpool meta set: disk I/O error")
	cc := &phaseCC{settleCC: settle}
	cc.onSend = func() { waitMoreLeaseWrites(t, settle.FakeCC, "Send", leaseErrorAfter+1) }
	logs := captureLog(t)
	deps := leaseDeps(t, cc, sbd, cfg, clk)
	d := DispatchContext{Role: noBudget(workerRole(cfg)), Item: item.Item{ID: "zr-w"}}
	res, err := (ccpoolExecutor{}).Dispatch(context.Background(), d, deps)
	if err != nil || verbOf(res) != "" {
		t.Fatalf("a lease-write failure must not fail the dispatch: res=%v err=%v", res, err)
	}
	out := logs.String()
	if !strings.Contains(out, "level=WARN msg=\"lease refresh failed\"") {
		t.Errorf("want WARN on a failed refresh; logs=%s", out)
	}
	if !strings.Contains(out, "level=ERROR msg=\"lease refresh keeps failing") {
		t.Errorf("want ERROR after %d consecutive failures; logs=%s", leaseErrorAfter, out)
	}
}

func TestLease_disabledWhenTTLZero(t *testing.T) {
	cfg := leaseCfg()
	cfg.LeaseTTL = 0
	clk := &dtest.ManualClock{T: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	sbd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "closed"}}}
	settle := newSettleCC(leaseSess, func() ccpool.SessionState {
		if sbd.Idx["zr-w"] >= 2 {
			return ccpool.StateIdle
		}
		return ccpool.StateWorking
	})
	deps := leaseDeps(t, settle, sbd, cfg, clk)
	d := DispatchContext{Role: noBudget(workerRole(cfg)), Item: item.Item{ID: "zr-w"}}
	if _, err := (ccpoolExecutor{}).Dispatch(context.Background(), d, deps); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if n := len(settle.FakeCC.SetMetaCalls()); n != 0 {
		t.Errorf("no lease writes expected with TTL 0, got %d", n)
	}
	if _, ok := settle.EnsuredMeta[ccpool.MetaKeyLeaseUntil]; ok {
		t.Errorf("no initial lease expected with TTL 0: %v", settle.EnsuredMeta)
	}
}

// --- absorb ---

// absorbRow is a live, working duplicate of a worker dispatch for zr-w, as an
// earlier (now dead) handler left it.
func absorbRow(meta map[string]string) ccpool.Session {
	return ccpool.Session{
		ExternalID: "att-old", Name: "pg-router-worker-zr-w", Live: true, State: ccpool.StateWorking,
		CWD: "/wt/zr-w", Meta: meta,
	}
}

// Absorbing an orphan keeps its ORIGINAL launch time as the budget start: a
// session launched 30 minutes ago is over a 25-minute budget at once, instead
// of being granted a fresh 25 minutes (INV-CCH-18; pg2-3j76b's case).
func TestAbsorb_usesLaunchedAtAsWatchdogStart(t *testing.T) {
	cases := []struct {
		name       string
		launchedAt func(t0 time.Time) map[string]string
		wantStop   bool
	}{
		{"launched 30m ago is over a 25m budget", func(t0 time.Time) map[string]string {
			return map[string]string{ccpool.MetaKeyLaunchedAt: t0.Add(-30 * time.Minute).Format(time.RFC3339)}
		}, true},
		{"launched 5m ago is under budget", func(t0 time.Time) map[string]string {
			return map[string]string{ccpool.MetaKeyLaunchedAt: t0.Add(-5 * time.Minute).Format(time.RFC3339)}
		}, false},
		{"no launched_at (older build) starts now", func(time.Time) map[string]string { return nil }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := fastCfg()
			cfg.MaxWait = 300 * time.Millisecond // manual-clock time; the loop advances it by PollInterval per poll
			t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
			clk := &dtest.ManualClock{T: t0}
			bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress"}}}
			cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{absorbRow(tc.launchedAt(t0))}}}
			deps := newExec(cc, bd, cfg).deps
			deps.ExternalID = "att-new"
			deps.Now = clk.Now
			orig := clk.TickAdvancing()
			deps.Tick = func(ctx context.Context, d time.Duration) error { // wall-clock pacing, as in the other watchdog-race tests
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
			d := timeBudgetDispatch(cfg)
			res, err := (ccpoolExecutor{}).Dispatch(context.Background(), d, deps)
			if err == nil {
				t.Fatal("expected a failure (budget stop or not-complete)")
			}
			stopped := errors.Is(err, watchdog.ErrBudgetExceeded)
			if stopped != tc.wantStop {
				t.Errorf("budget hard stop = %v, want %v (err=%v)", stopped, tc.wantStop, err)
			}
			if tc.wantStop && verbOf(res) != report.Unclaimed {
				t.Errorf("hard stop must report Unclaimed, got %q", verbOf(res))
			}
		})
	}
}

func timeBudgetDispatch(cfg config.Config) DispatchContext {
	role := workerRole(cfg)
	role.CCPool.Budget = budget.Budget{
		Time:       25 * time.Minute,
		Thresholds: budget.Thresholds{Reminder: 0.725, Cancel: 0.90, Hard: 1.00},
	}
	return DispatchContext{Role: role, Item: item.Item{ID: "zr-w"}}
}

// absorbSettled runs a dispatch that absorbs a settled (idle) duplicate, with a
// LockDir so the lease handshake takes the real flock.
func absorbSettled(t *testing.T, lockDir string, cc *dtest.FakeCC, beforeWait func()) (report.Result, error) {
	t.Helper()
	cfg := leaseCfg()
	clk := &dtest.ManualClock{T: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	sbd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"closed"}}}
	bd := &phaseBD{ScriptBD: sbd, onFirst: beforeWait}
	deps := leaseDeps(t, cc, bd, cfg, clk)
	deps.ExternalID = "att-new"
	deps.LockDir = lockDir
	d := DispatchContext{Role: noBudget(workerRole(cfg)), Item: item.Item{ID: "zr-w"}}
	return (ccpoolExecutor{}).Dispatch(context.Background(), d, deps)
}

func settledRow() ccpool.Session {
	r := absorbRow(map[string]string{ccpool.MetaKeyLeaseUntil: "2026-10-06T11:00:00Z"}) // long expired
	r.State = ccpool.StateIdle
	return r
}

// absorbDuplicate takes the per-session lock and refreshes the lease BEFORE it
// starts waiting on the session.
func TestAbsorb_refreshesLeaseUnderLockBeforeWaiting(t *testing.T) {
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{settledRow()}}}
	lockDir := t.TempDir()
	var writesBeforeWait int
	_, err := absorbSettled(t, lockDir, cc, func() { writesBeforeWait = len(cc.SetMetaCalls()) })
	if err != nil {
		t.Fatalf("absorbing a settled duplicate whose bead is closed must succeed: %v", err)
	}
	if writesBeforeWait < 1 {
		t.Fatalf("the lease must be refreshed before the wait starts; writes before first bead read = %d", writesBeforeWait)
	}
	first := cc.SetMetaCalls()[0]
	if first.ExternalID != "att-old" || first.Key != ccpool.MetaKeyLeaseUntil {
		t.Errorf("first write = %+v, want a lease refresh of the ABSORBED session att-old", first)
	}
	if len(cc.Ensured) != 0 {
		t.Errorf("absorbing must not launch a second session; Ensured=%v", cc.Ensured)
	}
	// The lock is released once the handshake is done.
	l, err := sessionlock.TryLock(lockDir, "att-old")
	if err != nil {
		t.Fatalf("the per-session lock must be released after the handshake: %v", err)
	}
	l.Unlock()
}

// While an orphan reconcile holds the session lock the absorbing dispatch waits
// (it neither refreshes nor waits blind), and proceeds once the lock is free.
func TestAbsorb_waitsForHeldLockThenProceeds(t *testing.T) {
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{settledRow()}}}
	lockDir := t.TempDir()
	held, err := sessionlock.TryLock(lockDir, "att-old")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := absorbSettled(t, lockDir, cc, nil)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("absorb must block while the lock is held; returned %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	if n := len(cc.SetMetaCalls()); n != 0 {
		t.Errorf("no lease refresh while another holder owns the lock; got %d writes", n)
	}
	held.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("absorb after the lock was released: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("absorb did not proceed after the lock was released")
	}
	if n := len(cc.SetMetaCalls()); n < 1 {
		t.Errorf("lease must be refreshed once the lock is taken; got %d writes", n)
	}
}

// If the orphan reconcile won the race (the row is now closed and marked
// reclaimed), the absorb handshake reports the row as not absorbable so the
// caller launches a fresh session instead.
func TestTakeOverForAbsorb_reclaimedRowIsNotAbsorbable(t *testing.T) {
	reclaimed := settledRow()
	reclaimed.Live, reclaimed.CloseReason = false, "handler"
	reclaimed.Meta = map[string]string{ccpool.MetaKeyOrphanReclaimed: "2026-10-06T12:00:00Z"}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{reclaimed}}}
	e := newExec(cc, &dtest.ScriptBD{}, leaseCfg())
	e.deps.LockDir = t.TempDir()
	_, ok, err := e.takeOverForAbsorb(context.Background(), settledRow(), "")
	if err != nil || ok {
		t.Fatalf("a reclaimed row must not be absorbable: ok=%v err=%v", ok, err)
	}
	if n := len(cc.SetMetaCalls()); n != 0 {
		t.Errorf("must not refresh the lease of a reclaimed row; got %d writes", n)
	}
}

// A session gone from the list under the lock is not absorbable either.
func TestTakeOverForAbsorb_goneRowIsNotAbsorbable(t *testing.T) {
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{}}}
	e := newExec(cc, &dtest.ScriptBD{}, leaseCfg())
	e.deps.LockDir = t.TempDir()
	if _, ok, err := e.takeOverForAbsorb(context.Background(), settledRow(), ""); err != nil || ok {
		t.Fatalf("a vanished row must not be absorbable: ok=%v err=%v", ok, err)
	}
}
