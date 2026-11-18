package runner

import (
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
)

// The scheduled window used by the catch-up tests is the 24 hours ending at t0.

func seedScheduled(t *testing.T, e *ccEnv, schedule, key, state string, to time.Time, window time.Duration) {
	t.Helper()
	err := e.l.WithTx(bg, func(tx *ledger.Tx) error {
		_, err := tx.InsertRun(bg, ledger.Run{
			Lens: "agent-workflow", Scope: "demo-scope", ScheduleID: schedule, RunKey: key, Trigger: "scheduled",
			State: state, Analyst: "ccpool", PID: 1, EndedAt: to,
			WindowFrom: to.Add(-window).Format(time.RFC3339Nano), WindowTo: to.Format(time.RFC3339Nano),
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func runCount(t *testing.T, e *ccEnv) int {
	t.Helper()
	runs, err := e.l.RunsInStates(bg, ledger.RunFiled, ledger.RunFailed, ledger.RunSkipped, ledger.RunRunning)
	if err != nil {
		t.Fatal(err)
	}
	return len(runs)
}

func wantNothing(t *testing.T, e *ccEnv, p CatchUpParams) {
	t.Helper()
	before := runCount(t, e)
	_, err := CatchUp(bg, e.d, p)
	var n *NothingToCatchUpError
	if !errors.As(err, &n) {
		t.Fatalf("want nothing to catch up, got %v", err)
	}
	if after := runCount(t, e); after != before {
		t.Errorf("rows %d -> %d, want no new row", before, after)
	}
}

func TestCatchUpRerunsTheSameWindowAndFilesIt(t *testing.T) {
	e := setupCcpool(t, ccCfg, 0)
	seedScheduled(t, e, "daily", "k1", ledger.RunSkipped, t0, 24*time.Hour)
	*e.now = t0.Add(time.Hour)
	e.fake.finals = []string{goldenFinal(t)}
	res, err := CatchUp(bg, e.d, CatchUpParams{})
	if err != nil {
		t.Fatal(err)
	}
	r := res.Run
	if r.State != ledger.RunFiled || r.RunKey != "k1" || r.ScheduleID != "daily" || r.Trigger != "scheduled" ||
		r.WindowFrom != t0.Add(-24*time.Hour).Format(time.RFC3339Nano) || r.WindowTo != t0.Format(time.RFC3339Nano) {
		t.Fatalf("%+v", r)
	}
	// A Filed result blocks further catch-up.
	*e.now = t0.Add(5 * time.Hour)
	wantNothing(t, e, CatchUpParams{})
}

func TestCatchUpCooldownNotElapsed(t *testing.T) {
	e := setupCcpool(t, ccCfg, 0)
	seedScheduled(t, e, "daily", "k1", ledger.RunSkipped, t0, 24*time.Hour)
	*e.now = t0.Add(54 * time.Minute)
	wantNothing(t, e, CatchUpParams{})
	*e.now = t0.Add(55 * time.Minute)
	e.fake.finals = []string{goldenFinal(t)}
	if _, err := CatchUp(bg, e.d, CatchUpParams{}); err != nil {
		t.Fatalf("at the cooldown: %v", err)
	}
}

func TestCatchUpAttemptCap(t *testing.T) {
	e := setupCcpool(t, ccCfg, 0)
	for i := 0; i < CatchUpMaxAttempts-1; i++ {
		seedScheduled(t, e, "daily", "k1", ledger.RunSkipped, t0, 24*time.Hour)
	}
	*e.now = t0.Add(time.Hour)
	e.fake.finals = []string{goldenFinal(t)}
	// 23 rows share the key: one more attempt is allowed and Files.
	if _, err := CatchUp(bg, e.d, CatchUpParams{}); err != nil {
		t.Fatalf("23 attempts: %v", err)
	}
	e2 := setupCcpool(t, ccCfg, 0)
	for i := 0; i < CatchUpMaxAttempts; i++ {
		seedScheduled(t, e2, "daily", "k1", ledger.RunSkipped, t0, 24*time.Hour)
	}
	*e2.now = t0.Add(time.Hour)
	wantNothing(t, e2, CatchUpParams{})
}

func TestCatchUpExpiresOneWindowLengthAfterTheWindowEnd(t *testing.T) {
	e := setupCcpool(t, ccCfg, 0)
	seedScheduled(t, e, "daily", "k1", ledger.RunSkipped, t0, 24*time.Hour)
	*e.now = t0.Add(24*time.Hour + time.Minute)
	wantNothing(t, e, CatchUpParams{})
}

func TestCatchUpDoesNotRetryAnalystFailures(t *testing.T) {
	e := setupCcpool(t, ccCfg, 0)
	seedScheduled(t, e, "daily", "k1", ledger.RunFailed, t0, 24*time.Hour)
	*e.now = t0.Add(2 * time.Hour)
	wantNothing(t, e, CatchUpParams{})
}

func TestCatchUpIgnoresManualRuns(t *testing.T) {
	e := setupCcpool(t, ccCfg, 0)
	seedScheduled(t, e, "", "k1", ledger.RunSkipped, t0, 24*time.Hour)
	*e.now = t0.Add(2 * time.Hour)
	wantNothing(t, e, CatchUpParams{})
}

func TestCatchUpBusyLockWritesNoRow(t *testing.T) {
	e := setupCcpool(t, ccCfg, 0)
	seedScheduled(t, e, "daily", "k1", ledger.RunSkipped, t0, 24*time.Hour)
	if err := e.l.WithTx(bg, func(tx *ledger.Tx) error {
		r, err := tx.InsertRun(bg, ledger.Run{Lens: "agent-workflow", Scope: "demo-scope", PID: 4242, StartedAt: t0.Add(-time.Hour)})
		if err != nil {
			return err
		}
		return tx.AcquireLock(bg, "agent-workflow", "demo-scope", r.ID, 4242, t0.Add(-time.Hour))
	}); err != nil {
		t.Fatal(err)
	}
	*e.now = t0.Add(2 * time.Hour)
	before := runCount(t, e)
	_, err := CatchUp(bg, e.d, CatchUpParams{})
	if !errors.Is(err, ledger.ErrLockBusy) {
		t.Fatalf("want a busy lock, got %v", err)
	}
	if after := runCount(t, e); after != before {
		t.Errorf("rows %d -> %d", before, after)
	}
}

func TestCatchUpRunsAtMostOneWindowPerInvocation(t *testing.T) {
	e := setupCcpool(t, ccCfg, 0)
	seedScheduled(t, e, "weekly", "kw", ledger.RunSkipped, t0.Add(-time.Hour), 7*24*time.Hour)
	seedScheduled(t, e, "daily", "kd", ledger.RunSkipped, t0, 24*time.Hour)
	*e.now = t0.Add(2 * time.Hour)
	e.fake.finals = []string{goldenFinal(t)}
	res, err := CatchUp(bg, e.d, CatchUpParams{})
	if err != nil {
		t.Fatal(err)
	}
	// The window that ended first is the one closest to expiring.
	if res.Run.RunKey != "kw" || runCount(t, e) != 3 {
		t.Fatalf("%+v rows %d", res.Run, runCount(t, e))
	}
}

func TestCatchUpForceIgnoresLimitsAndClearsTheGauge(t *testing.T) {
	e := setupCcpool(t, ccCfg, 0)
	for i := 0; i < CatchUpMaxAttempts; i++ {
		seedScheduled(t, e, "daily", "k1", ledger.RunSkipped, t0, 24*time.Hour)
	}
	seedScheduled(t, e, "daily", "k1", ledger.RunFailed, t0, 24*time.Hour)
	*e.now = t0.Add(100 * time.Hour)
	wantNothing(t, e, CatchUpParams{})
	if _, err := CatchUp(bg, e.d, CatchUpParams{Force: true}); err == nil {
		t.Fatal("--force without a lens must be a usage error")
	}
	e.fake.finals = []string{goldenFinal(t)}
	res, err := CatchUp(bg, e.d, CatchUpParams{Lens: "agent-workflow", Scope: "demo-scope", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Run.State != ledger.RunFiled || res.Run.RunKey != "k1" || res.Run.ScheduleID != "daily" {
		t.Fatalf("%+v", res.Run)
	}
	ws, err := e.l.ScheduledWindows(bg)
	if err != nil || len(ws) != 1 || !ws[0].Filed {
		t.Fatalf("%+v %v", ws, err)
	}
}

func TestCatchUpStillFailingCapacityLeavesTheKeyAndCountersAlone(t *testing.T) {
	e := setupCcpool(t, ccCfg, 0)
	seedScheduled(t, e, "daily", "k1", ledger.RunSkipped, t0, 24*time.Hour)
	*e.now = t0.Add(time.Hour)
	boom := errors.New("recorder unreachable")
	e.fake.capErrs = []error{boom, boom, boom}
	_, err := CatchUp(bg, e.d, CatchUpParams{})
	var sk *SkippedError
	if !errors.As(err, &sk) {
		t.Fatalf("want a skipped run, got %v", err)
	}
	ws, err := e.l.ScheduledWindows(bg)
	if err != nil || len(ws) != 1 || ws[0].Filed || ws[0].Attempts != 2 || ws[0].Latest.State != ledger.RunSkipped {
		t.Fatalf("%+v %v", ws, err)
	}
	md, err := e.l.MetricsData(bg)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range md.Schedules {
		if s.ConsecutiveFailed != 0 {
			t.Errorf("consecutive failed %d", s.ConsecutiveFailed)
		}
	}
	// The key is not consumed: the next hour's catch-up is not refused as filed.
	*e.now = t0.Add(3 * time.Hour)
	e.fake.finals = []string{goldenFinal(t)}
	if _, err := CatchUp(bg, e.d, CatchUpParams{}); err != nil {
		t.Fatalf("next attempt: %v", err)
	}
}
