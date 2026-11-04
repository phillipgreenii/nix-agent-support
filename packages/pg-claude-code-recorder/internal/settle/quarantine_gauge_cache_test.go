package settle

import (
	"os"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

// The gauge refresh must not walk projects/ on every tick, yet must see a
// quarantine directory a removal creates.
func TestGaugeQuarantineDirsWalksRarelyAndAfterARemoval(t *testing.T) {
	s := &Settler{}
	walks := 0
	s.walkQuarantine = func() []string { walks++; return []string{"/q"} }
	for i := 0; i < quarantineRewalkTicks; i++ {
		s.gaugeQuarantineDirs()
	}
	if walks != 1 {
		t.Fatalf("walks = %d over %d calls, want 1", walks, quarantineRewalkTicks)
	}
	s.gaugeQuarantineDirs()
	if walks != 2 {
		t.Fatalf("walks = %d after the rewalk interval, want 2", walks)
	}
	s.invalidateQuarantineDirs()
	s.gaugeQuarantineDirs()
	if walks != 3 {
		t.Fatalf("walks = %d after an invalidation, want 3", walks)
	}
}

func TestRemovalGaugesUpdateAfterARemovalDespiteTheCache(t *testing.T) {
	e := newRemoveEnv(t, false)
	e.oldSettled(5, removeAge+time.Hour)
	e.allowQuarantine = true
	e.holders = func(string) ([]int, error) { return []int{os.Getpid() + 1}, nil }
	_ = e.st.Close()
	e.open()
	e.hooks.AfterQuarantine = func(_, p string) {
		if err := os.WriteFile(p, []byte("a new file\n"), 0o600); err != nil {
			t.Error(err)
		}
	}
	walks := 0
	e.sett.walkQuarantine = func() []string { walks++; return e.sett.quarantineDirs() }
	e.tick() // primes the cache while no quarantine directory exists
	e.tick()
	if walks != 1 {
		t.Fatalf("walks = %d over two ticks, want 1", walks)
	}
	e.remove(sess) // the restore conflict leaves the original in a new quarantine directory
	e.tick()
	if v, _ := e.obs.Reg.Gauge(obs.MQuarantineFiles, nil); v != 1 {
		t.Fatalf("quarantine_files = %v after a removal, want 1", v)
	}
}
