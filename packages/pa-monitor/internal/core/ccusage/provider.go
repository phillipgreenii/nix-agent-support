package ccusage

import "context"

// Provider is the ccusage-backed cost adapter (ADR 0021 §3 — "route the
// existing ccusage path through CostPricer as its first adapter"). It wraps the
// existing CachedRunner (active-block bytes + probe state) and Runner (weekly),
// moving the parse that used to live in the poller into the adapter so
// consumers depend only on the port method set.
//
// Provider does NOT import the consumer packages: it satisfies the
// poller.CostPricer interface (ActiveBlock/Probed) structurally, so the
// satisfaction check happens for free at the composition-root assignment in
// cmd/pa-monitor. Putting a compile-time assertion here would import poller and
// create a cycle (poller already imports ccusage).
type Provider struct {
	cache  *CachedRunner
	weekly *Runner
}

// NewProvider builds a ccusage cost provider from a started CachedRunner (for
// the active 5h block) and a Runner (for the weekly entry).
func NewProvider(cache *CachedRunner, weekly *Runner) *Provider {
	return &Provider{cache: cache, weekly: weekly}
}

// ActiveBlock returns the parsed active 5h block, or nil when none is available
// yet. It preserves the CachedRunner contract that (nil,nil) bytes before the
// first successful refresh mean "not yet available" — it MUST NOT parse nil.
func (p *Provider) ActiveBlock(ctx context.Context) (*Block, error) {
	body, err := p.cache.Get(ctx)
	if err != nil {
		return nil, err
	}
	if body == nil {
		return nil, nil
	}
	return ParseActiveBlock(body)
}

// Probed reports whether the first background refresh has completed and the
// error from the most recent attempt — the same (probed, lastErr) pair the
// poller previously read via CCUsageStateFn. "probed but errored" is preserved
// distinctly from "not probed".
func (p *Provider) Probed() (probed bool, err error) {
	return p.cache.Probed(), p.cache.LastErr()
}

// CurrentWeekly returns the current week's cost entry (or nil), delegating to
// the wrapped Runner exactly as the previous weeklyFn did.
func (p *Provider) CurrentWeekly(ctx context.Context) (*WeeklyEntry, error) {
	return p.weekly.CurrentWeekly(ctx)
}
