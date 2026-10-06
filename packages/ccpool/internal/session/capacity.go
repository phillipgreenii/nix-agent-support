package session

import (
	"context"

	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/usagelimit"
)

// Capacity is the pool's occupancy under ADR 0072: preserved rows sit outside
// the cap; Free is what an admission gate consults.
type Capacity struct {
	MaxSessions int `json:"max_sessions"`
	Live        int `json:"live"`      // tmux-live rows, any state
	Preserved   int `json:"preserved"` // live and needs_input (outside the cap)
	Counted     int `json:"counted"`   // live and not preserved
	Free        int `json:"free"`      // max(0, MaxSessions - Counted); 0 while UsageLimit is set
	// UsageLimit is non-nil while an account usage window (5-hour block or
	// weekly limit) is at its limit: the pool accepts no work until the window
	// resets, whatever its occupancy. omitted from the JSON when no limit is hit,
	// so the shape is unchanged for a caller that predates it.
	UsageLimit *usagelimit.Limit `json:"usage_limit,omitempty"`
}

// WithUsageLimit returns c with l recorded and Free forced to zero: a hit usage
// window means "not right now" no matter how many slots are open, and an
// admission gate reading only Free then declines without learning a new field.
// A nil l returns c unchanged.
func (c Capacity) WithUsageLimit(l *usagelimit.Limit) Capacity {
	if l == nil {
		return c
	}
	c.UsageLimit = l
	c.Free = 0
	return c
}

// Capacity derives liveness from tmux exactly as Reap does and applies the one
// capacity definition Reap's Pass 2 uses (countedSessions).
func (s *Service) Capacity(ctx context.Context, maxSessions int) (Capacity, error) {
	rows, err := s.d.Store.List(ctx)
	if err != nil {
		return Capacity{}, err
	}
	var live []store.Session
	for _, r := range rows {
		if s.d.Tmux.HasSession(TmuxName(s.d.Prefix, r.ExternalID)) {
			live = append(live, r)
		}
	}
	c := Capacity{MaxSessions: maxSessions, Live: len(live)}
	c.Counted = countedSessions(live)
	c.Preserved = c.Live - c.Counted
	if c.Free = maxSessions - c.Counted; c.Free < 0 {
		c.Free = 0
	}
	return c, nil
}
