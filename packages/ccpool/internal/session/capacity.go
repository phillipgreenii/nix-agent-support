package session

import (
	"context"

	"github.com/phillipgreenii/ccpool/internal/store"
)

// Capacity is the pool's occupancy under ADR 0072: preserved rows sit outside
// the cap; Free is what an admission gate consults.
type Capacity struct {
	MaxSessions int `json:"max_sessions"`
	Live        int `json:"live"`      // tmux-live rows, any state
	Preserved   int `json:"preserved"` // live and needs_input (outside the cap)
	Counted     int `json:"counted"`   // live and not preserved
	Free        int `json:"free"`      // max(0, MaxSessions - Counted)
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
