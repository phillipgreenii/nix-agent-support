package session

import (
	"context"
	"sort"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
)

// Reap reconciles liveness, prunes phantom rows whose Claude session is gone
// (ADR 0015), and closes live sessions that are idle past idleTTL or beyond the
// pool cap, evicting by least-recent activity (spec §8.6).
func (s *Service) Reap(ctx context.Context, maxSessions int, idleTTL time.Duration) error {
	rows, err := s.d.Store.List(ctx)
	if err != nil {
		return err
	}
	now := s.now()

	// Pass 0: prune phantom rows. A row that is NOT live AND whose Claude session
	// is gone from disk is a phantom (ADR 0015) — remove it so it never resurrects
	// a finished/missing conversation. Guarded against the fresh-session race
	// (don't prune a young `starting` row that hasn't written a transcript yet).
	var live []store.Session
	for _, r := range rows {
		if s.d.Tmux.HasSession(s.d.Prefix + r.ExternalID) {
			live = append(live, r)
			continue
		}
		if !s.claudeSessionResumable(r) && !s.isFreshStarting(r) {
			if err := s.d.Store.Delete(ctx, r.ExternalID); err != nil {
				return err
			}
		}
	}

	// Keep only live sessions (derived liveness), oldest-activity first.
	sort.Slice(live, func(i, j int) bool { return live[i].LastActivityAt < live[j].LastActivityAt })

	toClose := map[string]bool{}
	// Pass 1: idle past TTL.
	for _, r := range live {
		if idleTTL > 0 && now.Sub(time.Unix(r.LastActivityAt, 0)) > idleTTL {
			toClose[r.ExternalID] = true
		}
	}
	// Pass 2: still over cap AFTER the TTL closures → close more oldest-activity
	// sessions. TTL closures COUNT toward the cap (spec §8.6: close "while over
	// the cap"), so a pool already at/under cap after TTL reaping closes nothing
	// more — otherwise we'd over-reap below the configured cap.
	capClosures := (len(live) - len(toClose)) - maxSessions
	for _, r := range live { // already sorted oldest-first
		if capClosures <= 0 {
			break
		}
		if !toClose[r.ExternalID] {
			toClose[r.ExternalID] = true
			capClosures--
		}
	}
	for _, r := range live {
		if toClose[r.ExternalID] {
			if err := s.Close(ctx, r.ExternalID, false); err != nil {
				return err
			}
		}
	}
	return nil
}
