package session

import (
	"context"
	"sort"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
)

// Reap reconciles liveness and closes live sessions that are idle past idleTTL
// or beyond the pool cap, evicting by least-recent activity (spec §8.6).
func (s *Service) Reap(ctx context.Context, maxSessions int, idleTTL time.Duration) error {
	rows, err := s.d.Store.List(ctx)
	if err != nil {
		return err
	}
	now := s.d.Now()

	// Keep only live sessions (derived liveness), oldest-activity first.
	var live []store.Session
	for _, r := range rows {
		if s.d.Tmux.HasSession(s.d.Prefix + r.Name) {
			live = append(live, r)
		}
	}
	sort.Slice(live, func(i, j int) bool { return live[i].LastActivityAt < live[j].LastActivityAt })

	toClose := map[string]bool{}
	// Pass 1: idle past TTL.
	for _, r := range live {
		if idleTTL > 0 && now.Sub(time.Unix(r.LastActivityAt, 0)) > idleTTL {
			toClose[r.Name] = true
		}
	}
	// Pass 2: over cap → close oldest-activity non-TTL sessions first.
	// capClosures is computed from the original live count so TTL and cap
	// closures are independent: e.g. 3 live, cap=2 → close 1 oldest non-TTL
	// even if a TTL session is already being closed.
	capClosures := len(live) - maxSessions
	for _, r := range live { // already sorted oldest-first
		if capClosures <= 0 {
			break
		}
		if !toClose[r.Name] {
			toClose[r.Name] = true
			capClosures--
		}
	}
	for _, r := range live {
		if toClose[r.Name] {
			if err := s.Close(ctx, r.Name, false); err != nil {
				return err
			}
		}
	}
	return nil
}
