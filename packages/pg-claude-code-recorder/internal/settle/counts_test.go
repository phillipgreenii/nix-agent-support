package settle

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

func counter(e *env, name string) float64 {
	v, _ := e.obs.Reg.Counter(name, nil)
	return v
}

func gauge(e *env, name string, labels map[string]string) float64 {
	v, _ := e.obs.Reg.Gauge(name, labels)
	return v
}

func stageGauge(e *env, stage string) float64 {
	return gauge(e, obs.MConversationsByStage, map[string]string{"stage": stage})
}

// A Wake publishes the stage and pending-byte gauges from the kept maps, and a
// stage change a wake makes moves the count.
func TestWakeKeepsTheStageGaugesCurrent(t *testing.T) {
	e := newEnvWith(t, false, func(e *env) { e.extra = func(c *Config) { c.Mode = ModeEvent } })
	e.seed(3, time.Hour)
	e.tick() // the reconciliation
	if got := stageGauge(e, StageQuiet); got != 1 {
		t.Fatalf("quiet = %v after the reconciliation, want 1", got)
	}
	e.clk.Advance(24*time.Hour + time.Minute)
	if _, err := e.sett.Wake(e.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if e.stage() != StageSettled {
		t.Fatalf("stage %s, want settled", e.stage())
	}
	if q, s := stageGauge(e, StageQuiet), stageGauge(e, StageSettled); q != 0 || s != 1 {
		t.Fatalf("quiet %v settled %v after the wake, want 0 and 1", q, s)
	}
	counts, _, ok := e.sett.LastCounts()
	if !ok || counts[StageSettled] != 1 || counts[StageQuiet] != 0 {
		t.Fatalf("LastCounts %v (ok %v) after the wake", counts, ok)
	}
}

// Corrupting a kept counter is detected by the next reconciliation, which logs
// once, meters it and heals the count; a later reconciliation is quiet.
func TestReconcileDetectsAndHealsDriftedCounts(t *testing.T) {
	e := newEnvWith(t, false, func(e *env) { e.extra = func(c *Config) { c.Mode = ModeEvent } })
	var buf bytes.Buffer
	e.obs.Log = slog.New(slog.NewTextHandler(&buf, nil))
	e.seed(3, time.Hour)
	e.tick()
	if _, err := e.sett.Wake(e.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if got := counter(e, obs.MSettleCountsDrift); got != 0 {
		t.Fatalf("drift %v before any corruption", got)
	}
	e.sett.mu.Lock()
	e.sett.gauges.n[StageQuiet] += 5
	e.sett.mu.Unlock()

	e.tick()
	if got := counter(e, obs.MSettleCountsDrift); got != 1 {
		t.Fatalf("settle_counts_drift_total = %v, want 1", got)
	}
	if n := strings.Count(buf.String(), "settle_counts_drift"); n != 1 {
		t.Fatalf("%d drift log lines, want 1:\n%s", n, buf.String())
	}
	if !strings.Contains(buf.String(), "quiet=6") || !strings.Contains(buf.String(), "quiet=1") {
		t.Fatalf("the warning does not carry both values:\n%s", buf.String())
	}
	if got := e.sett.keptCounts()[StageQuiet]; got != 1 {
		t.Fatalf("kept quiet count %d after the reconciliation, want the healed 1", got)
	}
	if got := stageGauge(e, StageQuiet); got != 1 {
		t.Fatalf("quiet gauge %v, want 1", got)
	}
	e.tick()
	if got := counter(e, obs.MSettleCountsDrift); got != 1 {
		t.Fatalf("drift counter %v after a clean reconciliation, want still 1", got)
	}
	if n := strings.Count(buf.String(), "settle_counts_drift"); n != 1 {
		t.Fatalf("%d drift log lines after a clean reconciliation, want 1", n)
	}
}

// A stage change no event caused (a row written behind the settler's back) is
// found by the reconciliation and metered as rescued, and the kept counts follow.
func TestReconcileMetersARescuedSession(t *testing.T) {
	e := newEnvWith(t, false, func(e *env) { e.extra = func(c *Config) { c.Mode = ModeEvent } })
	e.seed(3, time.Hour)
	e.tick()
	if got := counter(e, obs.MSettleReconcileRescued); got != 0 {
		t.Fatalf("rescued %v at start", got)
	}
	e.clk.Advance(24*time.Hour + time.Minute)
	// No Wake: the due deadline passed unnoticed, the reconciliation settles it.
	e.tick()
	if e.stage() != StageSettled {
		t.Fatalf("stage %s, want settled", e.stage())
	}
	if got := counter(e, obs.MSettleReconcileRescued); got != 1 {
		t.Fatalf("settle_reconcile_rescued_total = %v, want 1", got)
	}
	if got := counter(e, obs.MSettleCountsDrift); got != 0 {
		t.Fatalf("a rescued settle is not drift, got %v", got)
	}
}

// A session with a persisted removed stage and no files is counted from its
// stage row, by the reconciliation and by a wake that touches it.
func TestRemovedSessionsWithNoFilesAreCountedFromStageRows(t *testing.T) {
	e := newEnvWith(t, false, func(e *env) { e.extra = func(c *Config) { c.Mode = ModeEvent } })
	e.seed(3, time.Hour)
	if err := e.st.Commit(&store.Rec{T: store.TStage, At: 5, Session: "0a1b2c3d-0000-4000-8000-0000000000aa", State: store.StageRemoved}); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if got := stageGauge(e, "removed"); got != 1 {
		t.Fatalf("removed = %v after the reconciliation, want 1", got)
	}
	if got := e.sett.keptCounts()["removed"]; got != 1 {
		t.Fatalf("kept removed = %d, want 1", got)
	}
	if _, err := e.sett.Wake(e.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if got := stageGauge(e, "removed"); got != 1 {
		t.Fatalf("removed = %v after a wake, want 1", got)
	}
}
