package poller

import (
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/burnrate"
)

// burnSampler is the emit-tick's burn-rate state (C5): the Δtokens ring buffers
// and the previous per-session token totals. It is TICK-owned — burn-rate is a
// stateful sample over the token counts the producer publishes, so it must run
// at the tick cadence, never in the producer. Kept as its own component so the
// tick/producer field split (Phase 4) is explicit and cannot drift.
type burnSampler struct {
	short    map[string]*burnrate.Buffer
	long     map[string]*burnrate.Buffer
	prev     map[string]int
	winShort time.Duration
	winLong  time.Duration
}

// sample adds the Δ (totalTokens since the previous sample) to the session's
// short/long ring buffers at now and returns the two current rates. Buffers are
// created on first sight of a session, sized by the configured windows.
func (b *burnSampler) sample(sid string, now time.Time, totalTokens int) (shortRate, longRate float64) {
	if b.short == nil {
		b.short = map[string]*burnrate.Buffer{}
		b.long = map[string]*burnrate.Buffer{}
		b.prev = map[string]int{}
	}
	winShort := b.winShort
	if winShort == 0 {
		winShort = 60 * time.Second
	}
	winLong := b.winLong
	if winLong == 0 {
		winLong = 300 * time.Second
	}
	prev := b.prev[sid]
	delta := max(totalTokens-prev, 0)
	b.prev[sid] = totalTokens
	if _, ok := b.short[sid]; !ok {
		b.short[sid] = burnrate.New(winShort)
		b.long[sid] = burnrate.New(winLong)
	}
	b.short[sid].Add(now, delta)
	b.long[sid].Add(now, delta)
	return b.short[sid].Rate(now), b.long[sid].Rate(now)
}

// prune drops per-session state for sessions no longer in the active set.
func (b *burnSampler) prune(active map[string]bool) {
	for id := range b.short {
		if !active[id] {
			delete(b.short, id)
			delete(b.long, id)
			delete(b.prev, id)
		}
	}
}
