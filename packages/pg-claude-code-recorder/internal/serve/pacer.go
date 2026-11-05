package serve

import (
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
)

// pacer spaces the sweep's and the catch-up's ingests so they never exceed a
// rate of perSecond files per second: each wait returns no earlier than 1 /
// perSecond after the previous one (no burst credit, so a long idle does not
// buy a fast run later). A zero rate never waits.
type pacer struct {
	clk      clock.Clock
	interval time.Duration
	mu       sync.Mutex
	next     time.Time
}

func newPacer(clk clock.Clock, perSecond int) *pacer {
	p := &pacer{clk: clk}
	if perSecond > 0 {
		p.interval = time.Second / time.Duration(perSecond)
	}
	return p
}

// wait blocks (by the clock's Sleep) until the next ingest slot.
func (p *pacer) wait() {
	if p == nil || p.interval <= 0 {
		return
	}
	p.mu.Lock()
	now := p.clk.Now()
	slot := p.next
	if slot.Before(now) {
		slot = now
	}
	p.next = slot.Add(p.interval)
	p.mu.Unlock()
	if d := slot.Sub(now); d > 0 {
		p.clk.Sleep(d)
	}
}
