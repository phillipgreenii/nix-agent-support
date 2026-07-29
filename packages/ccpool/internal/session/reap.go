package session

import (
	"context"
	"sort"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
)

// preservedForHuman reports whether a LIVE row is parked awaiting a human decision
// and therefore MUST NOT be closed by either reap pass (ADR 0037). A `needs_input`
// row is idle by construction — the session is stopped mid-turn on a question — so
// without this predicate it is the FIRST session both passes would take, and the
// human's in-flight context would be gone before they could `ccpool attach`.
//
// Deliberately ONE predicate shared by Pass 1 (TTL) and Pass 2 (cap eviction) so
// the two passes cannot drift, mirroring pr-pool's closeUnlessNeedsInput (shared by
// teardownAll and run-role's single-session teardown). pr-pool's orchestrator is
// this predicate's peer across the seam; both realize the deployment set's
// INV-CCPOOL-6 ("a session projected as paused for a human decision MUST be
// preserved, not reaped").
//
// Preservation covers CLOSURE only, never Pass 0's phantom prune: a row that is not
// live and whose Claude session is gone holds no attachable context, so there is
// nothing to preserve.
func preservedForHuman(r store.Session) bool { return r.State == store.NeedsInput }

// Reap reconciles liveness, prunes phantom rows whose Claude session is gone
// (ADR 0015), and closes live sessions that are idle past idleTTL or beyond the
// pool cap, evicting by least-recent activity (NOT creation age — the
// oldest-created session is often the one the operator is deepest in) — EXCEPT
// sessions parked for a human, which both closure passes spare
// (preservedForHuman, ADR 0037). With
// enough preserved sessions the pool is deliberately left ABOVE maxSessions; that
// is safe because the cap is not an admission gate (Ensure never consults it), so
// an over-cap pool grows but cannot starve new work. Only an operator clears a
// preserved session (`ccpool attend`/`attach`, then `ccpool close`).
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
		if s.d.Tmux.HasSession(TmuxName(s.d.Prefix, r.ExternalID)) {
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
	// Pass 1: idle past TTL, sparing sessions parked for a human (ADR 0037).
	for _, r := range live {
		if preservedForHuman(r) {
			continue
		}
		if idleTTL > 0 && now.Sub(time.Unix(r.LastActivityAt, 0)) > idleTTL {
			toClose[r.ExternalID] = true
		}
	}
	// Pass 2: still over cap AFTER the TTL closures → close more oldest-activity
	// sessions. TTL closures COUNT toward the cap — ADR 0037's Context records both
	// passes, cap eviction running "while still over max_sessions AFTER the TTL
	// closures" — so a pool already at/under cap after TTL reaping closes nothing
	// more; otherwise we'd over-reap below the configured cap.
	//
	// A preserved session is skipped here too (ADR 0037) — the carve-out has NO
	// last-resort override. It still COUNTS in len(live), so the eviction pressure
	// on the non-preserved sessions is computed against the real pool size; the
	// pressure just goes unrelieved once only preserved sessions remain, leaving the
	// pool over cap by design.
	capClosures := (len(live) - len(toClose)) - maxSessions
	for _, r := range live { // already sorted oldest-first
		if capClosures <= 0 {
			break
		}
		if preservedForHuman(r) {
			continue
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
