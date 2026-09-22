package session

import (
	"context"
	"log/slog"
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
// the two passes cannot drift, mirroring pg-router's closeUnlessNeedsInput (shared by
// teardownAll and run-role's single-session teardown). pg-router's orchestrator is
// this predicate's peer across the seam; both realize the deployment set's
// INV-CCPOOL-6 ("a session projected as paused for a human decision MUST be
// preserved, not reaped").
//
// Preservation covers CLOSURE only, never Pass 0's phantom prune: a row that is not
// live and whose Claude session is gone holds no attachable context, so there is
// nothing to preserve.
func preservedForHuman(r store.Session) bool { return r.State == store.NeedsInput }

// countedSessions is the pool's occupancy for cap purposes: live rows NOT
// preserved for a human (ADR 0072, Decision 1). Preserved rows sit outside
// max_sessions entirely.
func countedSessions(live []store.Session) int {
	n := 0
	for _, r := range live {
		if !preservedForHuman(r) {
			n++
		}
	}
	return n
}

// evictable reports whether cap eviction may close a counted row: only a
// session whose turn has ended (Stop or StopFailure). A starting/ready/working
// row is never evicted for cap pressure (ADR 0072, Decision 2); a hung working
// row is Pass 1's job once idle_ttl elapses, and a never-ingested ready row is
// its dispatcher's job (close --reason handler).
func evictable(r store.Session) bool {
	if preservedForHuman(r) {
		return false
	}
	return r.State == store.Idle || r.State == store.Errored
}

// Reap reconciles liveness, prunes phantom rows whose Claude session is gone
// (ADR 0015), and closes live sessions that are idle past idleTTL or beyond the
// pool cap, evicting by least-recent activity (NOT creation age — the
// oldest-created session is often the one the operator is deepest in) — EXCEPT
// sessions parked for a human, which both closure passes spare
// (preservedForHuman, ADR 0037). Capacity counts only non-preserved rows
// (countedSessions); cap eviction may close only a row whose turn has already
// ended — idle or errored, never starting/ready/working (evictable, ADR 0072).
// If every remaining counted row is still working, the pool is deliberately
// left over cap — admission control (ccpool capacity), not eviction, bounds
// working sessions. With enough preserved sessions the pool is deliberately
// left ABOVE maxSessions; that is safe because the cap is not an admission gate
// (Ensure never consults it), so an over-cap pool grows but cannot starve new
// work. Only an operator clears a preserved session (`ccpool attend`/`attach`,
// then `ccpool close`).
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
			recordReapPhantomPruned()
			slog.Info("ccpool: reap pruned phantom session", sessionLogArgs(r.ExternalID)...)
		}
	}

	// Keep only live sessions (derived liveness), oldest-activity first.
	sort.Slice(live, func(i, j int) bool { return live[i].LastActivityAt < live[j].LastActivityAt })

	// toClose maps an about-to-close external_id to WHICH pass added it
	// ("idle_ttl" or "cap_eviction") — needed for ccpool_reap_closures_total's
	// reason label (design D6/D11); a bare bool cannot distinguish the two
	// passes.
	toClose := map[string]string{}
	// Pass 1: idle past TTL, sparing sessions parked for a human (ADR 0037).
	for _, r := range live {
		if preservedForHuman(r) {
			continue
		}
		if idleTTL > 0 && now.Sub(time.Unix(r.LastActivityAt, 0)) > idleTTL {
			toClose[r.ExternalID] = "idle_ttl"
		}
	}
	// Pass 2: still over cap AFTER the TTL closures → close more sessions, but
	// only ones whose turn has ended, oldest-activity first (ADR 0072). Capacity
	// counts only non-preserved rows; TTL closures count toward the cap (ADR
	// 0037's Context). If every remaining counted row is still working, the pool
	// is left over cap on purpose — admission control (ccpool capacity) bounds
	// working sessions, not eviction.
	capClosures := (countedSessions(live) - len(toClose)) - maxSessions
	for _, r := range live { // already sorted oldest-first
		if capClosures <= 0 {
			break
		}
		if !evictable(r) {
			continue
		}
		if _, ok := toClose[r.ExternalID]; !ok {
			toClose[r.ExternalID] = "cap_eviction"
			capClosures--
		}
	}

	// ccpool_sessions_preserved_for_human is a gauge over the whole invocation's
	// live rows, not a per-session event, so it has no D11 narration pair (design
	// D6/D11 list only retry-exhausted/cancel-outcome/reap-closure-or-phantom-
	// prune/launch-outcome as per-session narration points).
	var preserved int64
	for _, r := range live {
		if preservedForHuman(r) {
			preserved++
		}
	}
	recordSessionsPreservedForHuman(preserved)

	for _, r := range live {
		reason, ok := toClose[r.ExternalID]
		if !ok {
			continue
		}
		if err := s.closeWithReason(ctx, r.ExternalID, reason, false); err != nil {
			return err
		}
		recordReapClosure(reason)
		slog.Info("ccpool: reap closed session",
			append([]any{"reason", reason}, sessionLogArgs(r.ExternalID)...)...)
	}
	return nil
}
