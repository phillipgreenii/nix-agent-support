package serve

import (
	"strconv"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

// startupTimer times the daemon's start, phase by phase, so a slow start
// explains itself from the log: one startup_phase per phase when it ends (and a
// startup_phase_begin before a phase that can run long, so a hang is visible
// while it happens) and one startup_complete with the total. It fires a fixed
// handful of events per start, never one per file.
type startupTimer struct {
	o      *obs.Obs
	clk    clock.Clock
	t0     time.Time
	phases int
}

func newStartupTimer(o *obs.Obs, clk clock.Clock) *startupTimer {
	return &startupTimer{o: o, clk: clk, t0: clk.Now()}
}

// begin starts the named phase and returns the function that ends it. long
// marks a phase that can exceed a few seconds: its start is logged too.
func (t *startupTimer) begin(name string, long bool) func() {
	if long {
		t.o.Event(obs.EvStartupPhaseBegin, "phase", name)
	}
	start := t.clk.Now()
	return func() {
		t.phases++
		t.o.Event(obs.EvStartupPhase, "phase", name, "duration_ms", ms(t.clk.Now().Sub(start)))
	}
}

// longPhases are the store phases that scale with the data (reading the
// journal, opening the database and its WAL, replaying the journal).
var longPhases = map[string]bool{"store_journal_read": true, "store_open_db": true, "store_replay": true}

// storeHook is the store's Options.Phase.
func (t *startupTimer) storeHook(name string) func() { return t.begin(name, longPhases[name]) }

// complete logs the total since the timer was created.
func (t *startupTimer) complete() {
	t.o.Event(obs.EvStartupComplete, "duration_ms", ms(t.clk.Now().Sub(t.t0)), "phases", strconv.Itoa(t.phases))
}

func ms(d time.Duration) string { return strconv.FormatInt(d.Milliseconds(), 10) }
