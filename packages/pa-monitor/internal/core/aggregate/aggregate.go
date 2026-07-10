package aggregate

import (
	"sort"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
)

// Build groups sessions by Cwd and totals tokens/cost. The block argument may be nil.
// blockCapUSD is the per-5h-block soft cap (0 = unknown) used for display-layer
// projections; the caller supplies it from the Account so Build no longer looks
// up the cap from the concrete cost provider.
func Build(sessions []*session.Session, enriched map[string]SessionEnrichment, prByDir map[string]*session.PRInfo, block *usage.Block, blockCapUSD float64) *Tree {
	byDir := map[string]*Directory{}
	var windowResetsAt time.Time
	for _, s := range sessions {
		d, ok := byDir[s.Cwd]
		if !ok {
			d = &Directory{Path: s.Cwd}
			byDir[s.Cwd] = d
		}
		en := enriched[s.SessionID]
		sv := &SessionView{Session: s, SessionEnrichment: en}
		d.Sessions = append(d.Sessions, sv)
		if d.Branch == "" && s.Branch != "" {
			d.Branch = s.Branch
		}
		d.TotalTokens += en.SessionTokens
		d.BurnRateSum += en.BurnRateShort
		switch s.Status {
		case session.Working:
			d.WorkingN++
		case session.Blocked:
			d.BlockedN++
			switch s.Blocker {
			case session.HumanInput:
				d.BlockedHumanInputN++
			case session.HumanAuthn:
				d.BlockedHumanAuthnN++
			case session.UsageLimit:
				d.BlockedUsageLimitN++
			case session.ErrorBlocker:
				d.BlockedErrorN++
			}
		case session.Idle:
			d.IdleN++
		}
		if en.RateLimitResetsAt.After(windowResetsAt) {
			windowResetsAt = en.RateLimitResetsAt
		}
	}
	// Sort sessions within each directory newest-first (stable across polls).
	for _, d := range byDir {
		sort.Slice(d.Sessions, func(i, j int) bool {
			return d.Sessions[i].StartedAt.After(d.Sessions[j].StartedAt)
		})
	}
	// Assign PRInfo from per-directory lookup results.
	if prByDir != nil {
		for _, d := range byDir {
			d.PRInfo = prByDir[d.Path]
		}
	}
	// directory cost = proportional share of block cost by tokens
	var grandTokens int
	for _, d := range byDir {
		grandTokens += d.TotalTokens
	}
	if block != nil && grandTokens > 0 {
		for _, d := range byDir {
			d.TotalCostUSD = block.CostUSD * float64(d.TotalTokens) / float64(grandTokens)
			for _, s := range d.Sessions {
				s.SessionEnrichment.CostUSD = block.CostUSD * float64(s.SessionEnrichment.SessionTokens) / float64(grandTokens)
			}
		}
	}
	tree := &Tree{
		ActiveBlock:    block,
		PlanCapUSD:     blockCapUSD,
		GeneratedAt:    time.Now(),
		WindowResetsAt: windowResetsAt,
	}
	for _, d := range byDir {
		tree.Dirs = append(tree.Dirs, d)
	}
	sort.Slice(tree.Dirs, func(i, j int) bool { return tree.Dirs[i].Path < tree.Dirs[j].Path })
	return tree
}

// ProjectedExhaust returns when the active block is projected to hit the plan cap
// at the current burn's cost/hour. Zero time when no block, zero burn, or the
// cap has already been reached.
func (t *Tree) ProjectedExhaust(now time.Time) time.Time {
	if t.ActiveBlock == nil || t.PlanCapUSD <= 0 {
		return time.Time{}
	}
	costRemaining := t.PlanCapUSD - t.ActiveBlock.CostUSD
	if costRemaining <= 0 {
		return now
	}
	burn := t.ActiveBlock.BurnRate.CostPerHour
	if burn <= 0 {
		return time.Time{}
	}
	hours := costRemaining / burn
	return now.Add(time.Duration(hours * float64(time.Hour)))
}
