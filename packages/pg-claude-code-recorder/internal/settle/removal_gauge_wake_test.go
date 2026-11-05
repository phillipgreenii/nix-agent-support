package settle

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

var removalGaugeNames = []string{
	obs.MQuarantineFiles, obs.MQuarantineBytes, obs.MQuarantineOldestAge,
	obs.MRemovalBacklogConvs, obs.MRemovalBacklogBytes,
}

func removalGaugeValues(e *env) map[string]float64 {
	out := map[string]float64{}
	for _, n := range removalGaugeNames {
		out[n] = gauge(e, n, nil)
	}
	return out
}

// quietRemovalWakeEnv is removalWakeEnv after the wake that performs the due
// removal, so later wakes have nothing left to do.
func quietRemovalWakeEnv(t *testing.T) *env {
	t.Helper()
	e := removalWakeEnv(t)
	e.wake()
	return e
}

// removalWakeEnv is an event-mode removal env with one settled Conversation
// past remove.after (its removal is due on the next wake) and the
// reconciliation done.
func removalWakeEnv(t *testing.T) *env {
	t.Helper()
	e := newEnvWith(t, false, func(e *env) {
		e.removeOn = true
		e.extra = func(c *Config) { c.Mode = ModeEvent }
	})
	e.oldSettled(5, removeAge+time.Hour)
	e.tickSettleOnly() // the reconciliation
	return e
}

func (e *env) wake() {
	e.t.Helper()
	if _, err := e.sett.Wake(e.clk.Now()); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) gaugeRefreshes() int64 { return e.sett.removalGaugeRefreshes.Load() }

func TestWakesWithoutARemovalEventDoNotRefreshTheRemovalGauges(t *testing.T) {
	e := quietRemovalWakeEnv(t)
	base := e.gaugeRefreshes()
	if base == 0 {
		t.Fatal("the reconciliation did not refresh the removal gauges")
	}
	e.wake()
	e.clk.Advance(30 * time.Second)
	e.wake()
	if got := e.gaugeRefreshes() - base; got != 0 {
		t.Fatalf("%d refreshes over two wakes within a minute, want 0", got)
	}
}

func TestRemovalGaugesRefreshOnTheFiveMinuteTimer(t *testing.T) {
	e := quietRemovalWakeEnv(t)
	base := e.gaugeRefreshes()
	e.clk.Advance(rgInterval - time.Second)
	e.wake()
	if got := e.gaugeRefreshes() - base; got != 0 {
		t.Fatalf("%d refreshes before the interval, want 0", got)
	}
	e.clk.Advance(2 * time.Second)
	e.wake()
	if got := e.gaugeRefreshes() - base; got != 1 {
		t.Fatalf("%d refreshes after the interval, want 1", got)
	}
	e.wake()
	if got := e.gaugeRefreshes() - base; got != 1 {
		t.Fatalf("%d refreshes after a second wake, want 1", got)
	}
}

// One removal calls the refresh once, and the gauges equal what the scan path
// reports on the same state.
func TestRemovalRefreshesTheGaugesOnceAndMatchesTheScan(t *testing.T) {
	e := removalWakeEnv(t)
	base := e.gaugeRefreshes()
	e.wake() // the due removal runs inside this wake
	if got := e.gaugeRefreshes() - base; got != 1 {
		t.Fatalf("%d refreshes after a removal wake, want 1", got)
	}
	if e.stage() != store.StageRemoved {
		t.Fatalf("stage %s, want removed", e.stage())
	}
	e.wake()
	if got := e.gaugeRefreshes() - base; got != 1 {
		t.Fatalf("%d refreshes after a further wake, want 1", got)
	}
	wakeVals := removalGaugeValues(e)
	e.tickSettleOnly() // the scan path
	scanVals := removalGaugeValues(e)
	for _, n := range removalGaugeNames {
		if wakeVals[n] != scanVals[n] {
			t.Errorf("%s: wake %v, scan %v", n, wakeVals[n], scanVals[n])
		}
	}
}

func TestDoctorRunRefreshesTheRemovalGaugesOnTheNextWake(t *testing.T) {
	e := quietRemovalWakeEnv(t)
	base := e.gaugeRefreshes()
	if _, err := e.sett.Doctor(); err != nil {
		t.Fatal(err)
	}
	e.wake()
	if got := e.gaugeRefreshes() - base; got != 1 {
		t.Fatalf("%d refreshes after a doctor run and a wake, want 1", got)
	}
}

func TestQuarantineRestoreRefreshesTheRemovalGaugesOnTheNextWake(t *testing.T) {
	e := quietRemovalWakeEnv(t)
	dir := t.TempDir()
	q := filepath.Join(dir, "quarantined")
	if err := os.WriteFile(q, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := e.gaugeRefreshes()
	conflict, err := e.sett.restoreOne(pendingFile{Rel: "x", Path: filepath.Join(dir, "orig")}, q)
	if err != nil || conflict {
		t.Fatalf("restoreOne: conflict %v err %v", conflict, err)
	}
	e.wake()
	if got := e.gaugeRefreshes() - base; got != 1 {
		t.Fatalf("%d refreshes after a restore and a wake, want 1", got)
	}
}
