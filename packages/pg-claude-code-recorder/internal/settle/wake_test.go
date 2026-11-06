package settle

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

func wakes(s *Settler, reason string) float64 {
	v, _ := s.cfg.Obs.Reg.Counter(obs.MSettleWakes, map[string]string{"reason": reason})
	return v
}

// Each reason label is incremented by its own trigger.
func TestWakeReasonsAreCountedByTheirOwnTrigger(t *testing.T) {
	e := newEnvWith(t, false, func(e *env) { e.extra = func(c *Config) { c.Mode = ModeEvent } })
	e.seed(3, time.Hour)
	e.tick() // reconcile
	if got := wakes(e.sett, "reconcile"); got != 1 {
		t.Fatalf("reconcile %v after one Tick, want 1", got)
	}
	want := map[string]float64{"reconcile": 1}
	check := func(step string) {
		t.Helper()
		for _, r := range []string{"dirty", "due", "reconcile", "poll"} {
			if got := wakes(e.sett, r); got != want[r] {
				t.Fatalf("%s: wakes{%s} = %v, want %v", step, r, got, want[r])
			}
		}
	}
	wake := func() {
		t.Helper()
		if _, err := e.sett.Wake(e.clk.Now()); err != nil {
			t.Fatal(err)
		}
	}
	e.sett.TakeDirty()
	wake() // nothing happened: the poll
	want["poll"]++
	check("idle wake")

	e.write(e.transcript(), line(9), e.clk.Now(), true)
	e.ingest(e.transcript())
	wake()
	want["dirty"]++
	check("a write")

	e.clk.Advance(24*time.Hour + time.Minute) // the quiet deadline passes
	wake()
	want["due"]++
	check("a deadline")
	if got := e.stage(); got != StageSettled {
		t.Fatalf("stage %s, want settled by the due wake", got)
	}
}
