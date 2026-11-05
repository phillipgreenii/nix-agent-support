package settle

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

// The removal backlog gauges must not look up paths and stat files of already
// known settled Conversations on every tick, yet must still report them and
// pick up a change once the cached facts are old.
func TestRemovalBacklogGaugesReuseFactsBetweenTicks(t *testing.T) {
	var mtimeCalls int
	e := newEnvWith(t, false, func(e *env) {
		e.removeOn = true
		e.extra = func(c *Config) {
			c.Mtime = func(p string) (time.Time, error) {
				mtimeCalls++
				return e.clk.Now().Add(-removeAge - time.Hour), nil
			}
		}
	})
	e.oldSettled(5, removeAge+time.Hour)
	groups, err := e.sett.groups()
	if err != nil {
		t.Fatal(err)
	}
	live := map[string]bool{}
	refresh := func() {
		e.sett.refreshRemovalGauges(groups, live, e.clk.Now())
	}
	refresh()
	if v, _ := e.obs.Reg.Gauge(obs.MRemovalBacklogConvs, nil); v != 1 {
		t.Fatalf("backlog convs = %v, want 1", v)
	}
	bytes, _ := e.obs.Reg.Gauge(obs.MRemovalBacklogBytes, nil)
	if bytes <= 0 {
		t.Fatalf("backlog bytes = %v, want > 0", bytes)
	}
	calls := mtimeCalls
	if calls == 0 {
		t.Fatal("the first refresh never read a modification time")
	}
	for i := 0; i < 5; i++ {
		e.clk.Advance(time.Minute)
		refresh()
	}
	if mtimeCalls != calls {
		t.Fatalf("5 refreshes inside the reuse window made %d more mtime reads, want 0", mtimeCalls-calls)
	}
	if v, _ := e.obs.Reg.Gauge(obs.MRemovalBacklogBytes, nil); v != bytes {
		t.Fatalf("backlog bytes changed to %v while cached, want %v", v, bytes)
	}
	e.clk.Advance(backlogRecomputeAfter)
	refresh()
	if mtimeCalls == calls {
		t.Fatal("a refresh after the reuse window did not recompute")
	}
	if v, _ := e.obs.Reg.Gauge(obs.MRemovalBacklogConvs, nil); v != 1 {
		t.Fatalf("backlog convs = %v after recompute, want 1", v)
	}
}
