package session

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/telemetry"
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
// work. Only an operator clears a LIVE preserved session (`ccpool attend`/
// `attach`, then `ccpool close`).
//
// One further, deliberately narrow closure exists, for a row that is NOT live:
// a `needs_input` row whose tmux session is gone and whose last activity is
// older than WithDeadNeedsInputTTL is closed (non-purge, reason
// dead_needs_input_ttl) so it stops being counted as awaiting a human forever
// (ADR 0086). It is opt-in per call: Reap without that option never closes a
// dead row.
func (s *Service) Reap(ctx context.Context, maxSessions int, idleTTL time.Duration, opts ...ReapOption) error {
	var o reapOptions
	for _, opt := range opts {
		opt(&o)
	}
	rows, err := s.d.Store.List(ctx)
	if err != nil {
		return err
	}
	now := s.now()

	// Emit runs the lock-free SessionEnd hook (or a kept-row Pass 0) ended since
	// the last sweep, BEFORE Pass 0 can delete a session row and lose them.
	s.emitPendingRuns(ctx)

	// Pass 0: prune phantom rows. A row that is NOT live AND whose Claude session
	// is gone from disk is a phantom (ADR 0015) — remove it so it never resurrects
	// a finished/missing conversation. Guarded against the fresh-session race
	// (don't prune a young `starting` row that hasn't written a transcript yet).
	var live []store.Session
	var deadKept []store.Session // not live, but not a phantom either (resumable / fresh)
	for _, r := range rows {
		if s.d.Tmux.HasSession(TmuxName(s.d.Prefix, r.ExternalID)) {
			live = append(live, r)
			continue
		}
		// The classification above was made without the lock; the decision to
		// finalize or delete is re-read under the per-external_id lock.
		kind, cur, err := s.reapDeadRow(ctx, r.ExternalID)
		if err != nil {
			return err
		}
		switch kind {
		case deadLive:
			live = append(live, cur)
		case deadKeptRow:
			deadKept = append(deadKept, cur)
		}
	}
	// Dead-row backstop (ADR 0086): after Pass 0 has classified every row and
	// before any metric or closure pass reads them, close the dead needs_input
	// rows that have waited past the TTL. It returns the slice with each closed
	// row refreshed, so the metrics below describe the post-close registry.
	deadKept, err = s.closeStaleDeadNeedsInput(ctx, deadKept, now, o.deadNeedsInputTTL)
	if err != nil {
		return err
	}
	recordSessionStates(sessionStateCounts(live, deadKept), s.poolMetricAttrs())

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
	s.recordPreservedForHuman(live, deadKept)
	// ccpool_session_info: one info point per live session, labels resolved
	// here (before any close below deletes metadata). Emitted for every live
	// row regardless of whether this sweep then closes it; a closed session's
	// series simply goes stale. Phantoms were pruned in Pass 0 and are not in
	// live, so they get none.
	for _, r := range live {
		recordSessionInfo(r.ClaudeSessionID, s.metricAttrs(r.ExternalID))
	}

	for _, r := range live {
		reason, ok := toClose[r.ExternalID]
		if !ok {
			continue
		}
		// Attributes are resolved per session, inside the loop, BEFORE the
		// close (never once per process: reap touches sessions with different
		// roles).
		attrs := s.metricAttrs(r.ExternalID)
		logArgs := append([]any{"reason", reason}, sessionLogArgs(r.ExternalID)...)
		if err := s.closeWithReason(ctx, r.ExternalID, reason, false); err != nil {
			return err
		}
		recordReapClosure(reason, attrs)
		slog.Info("ccpool: reap closed session", logArgs...)
	}
	// Last step, after every row decision above: collect orphaned lock files.
	// Best effort and never fatal (see gcLocks).
	s.gcLocks(ctx)
	return nil
}

// recordPreservedForHuman emits ccpool_sessions_preserved_for_human: ONE value
// per (pool, allowlisted label set), counting the rows awaiting a human decision.
// That is every LIVE row preservedForHuman, plus every NOT-live (dead-kept) row
// that is still awaitingHumanDead (ADR 0086): before that, a dead needs_input
// row was invisible here and the gauge read 0 while the row sat unreaped for
// weeks. Every label set that has a row is emitted, zeros included, so a set
// whose sessions were all cleared reads 0 rather than a stale prior value; a
// pool with no rows emits a single pool-only 0. The grouping key is each row's
// resolved attribute set, resolved per row (the labels differ per session).
func (s *Service) recordPreservedForHuman(live, deadKept []store.Session) {
	type group struct {
		attrs []attribute.KeyValue
		count int64
	}
	groups := map[attribute.Distinct]*group{}
	var order []attribute.Distinct // first-seen order (live is activity-sorted): deterministic emission
	add := func(r store.Session, counts bool) {
		attrs := s.metricAttrs(r.ExternalID)
		set := attribute.NewSet(attrs...)
		key := set.Equivalent()
		g, ok := groups[key]
		if !ok {
			g = &group{attrs: attrs}
			groups[key] = g
			order = append(order, key)
		}
		if counts {
			g.count++
		}
	}
	for _, r := range live {
		add(r, preservedForHuman(r))
	}
	for _, r := range deadKept {
		add(r, awaitingHumanDead(r))
	}
	if len(order) == 0 {
		recordSessionsPreservedForHuman(0, s.poolMetricAttrs())
		return
	}
	for _, key := range order {
		recordSessionsPreservedForHuman(groups[key].count, groups[key].attrs)
	}
}

// ReapOption configures one Reap call.
type ReapOption func(*reapOptions)

type reapOptions struct {
	deadNeedsInputTTL time.Duration
}

// WithDeadNeedsInputTTL enables the dead-needs_input backstop: a row that is
// not live, is still `needs_input`, and whose last activity is older than ttl is
// closed with reason dead_needs_input_ttl. A ttl of 0 (or negative) disables it,
// which is also what omitting the option means.
func WithDeadNeedsInputTTL(ttl time.Duration) ReapOption {
	return func(o *reapOptions) { o.deadNeedsInputTTL = ttl }
}

// closedSinceActivity reports whether ccpool closed the row at or after its last
// activity, i.e. nothing has happened to it since the close. close_reason and
// closed_at are never cleared when a session is resumed, so the stamp alone does
// not mean "currently closed"; a resume that reaches needs_input again bumps
// last_activity_at past closed_at and the row counts as open once more.
func closedSinceActivity(r store.Session) bool {
	return r.ClosedAt > 0 && r.ClosedAt >= r.LastActivityAt
}

// awaitingHumanDead reports whether a NOT-live row is still a `needs_input` row
// that no one has closed: the human-awaited question is still on record. Once
// ccpool (or an operator) has closed the row, nothing awaits a human any more,
// so it leaves the needs_input count even though its stored state is unchanged
// (ADR 0015: a close does not fabricate a state).
func awaitingHumanDead(r store.Session) bool {
	return preservedForHuman(r) && !closedSinceActivity(r)
}

// closeStaleDeadNeedsInput is the dead-row backstop. For each dead-kept row that
// is awaitingHumanDead and idle past ttl it re-reads the row UNDER the
// per-external_id lock and closes it only if it is still dead, still
// needs_input, still not closed, and still past the TTL, so a session resumed
// since Pass 0's unlocked classification is never closed. The close is
// non-purge: the row, the transcript and the worktree are untouched (the
// session stays resumable with `ccpool attach`), only close_reason/closed_at
// and the run end are written. Returns deadKept with each closed row replaced
// by its fresh copy. ttl <= 0 is a no-op.
//
// Idempotent: a closed row has closed_at >= last_activity_at, so the next sweep
// skips it (closedSinceActivity).
func (s *Service) closeStaleDeadNeedsInput(ctx context.Context, deadKept []store.Session, now time.Time, ttl time.Duration) ([]store.Session, error) {
	if ttl <= 0 {
		return deadKept, nil
	}
	out := make([]store.Session, len(deadKept))
	copy(out, deadKept)
	for i, r := range out {
		if !staleDeadNeedsInput(r, now, ttl) {
			continue
		}
		var fresh store.Session
		err := s.withLock(r.ExternalID, func() error {
			cur, ok, err := s.d.Store.GetByExternalID(ctx, r.ExternalID)
			if err != nil || !ok {
				return err
			}
			fresh = cur
			if s.d.Tmux.HasSession(TmuxName(s.d.Prefix, r.ExternalID)) || !staleDeadNeedsInput(cur, now, ttl) {
				return nil
			}
			// Attributes and log args are resolved before the close, as for the
			// live passes.
			attrs := s.metricAttrs(r.ExternalID)
			logArgs := append([]any{"reason", reasonDeadNeedsInputTTL, "idle_for", now.Sub(time.Unix(cur.LastActivityAt, 0)).String()}, sessionLogArgs(r.ExternalID)...)
			if err := s.closeLocked(ctx, r.ExternalID, reasonDeadNeedsInputTTL, false); err != nil {
				return err
			}
			recordReapClosure(reasonDeadNeedsInputTTL, attrs)
			slog.Info("ccpool: reap closed dead needs_input session", logArgs...)
			after, ok, err := s.d.Store.GetByExternalID(ctx, r.ExternalID)
			if err != nil {
				return err
			}
			if ok {
				fresh = after
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		out[i] = fresh
	}
	return out, nil
}

// reasonDeadNeedsInputTTL is the close reason (store.CloseReasons) for the
// dead-needs_input backstop.
const reasonDeadNeedsInputTTL = "dead_needs_input_ttl"

// staleDeadNeedsInput is the pure eligibility test for the backstop, applied
// both to the Pass 0 snapshot and again to the row re-read under the lock. It
// does NOT test liveness; the caller does.
func staleDeadNeedsInput(r store.Session, now time.Time, ttl time.Duration) bool {
	return awaitingHumanDead(r) && now.Sub(time.Unix(r.LastActivityAt, 0)) > ttl
}

type deadRowKind int

const (
	deadGone    deadRowKind = iota // pruned, or no longer present
	deadLive                       // resumed since the unlocked check
	deadKeptRow                    // dead but resumable / fresh: kept
)

// reapDeadRow is Pass 0 for one not-live row. Under the per-external_id lock it
// re-reads the row and skips one that is no longer dead (for example it was
// resumed). A phantom (not live, Claude session gone, not fresh-starting) has
// its open run finalized through the shared FinalizeRun (pending end_reason if
// any, else `exited`, end_source=reaper, ended_at=last_activity_at) BEFORE the
// row is deleted. A dead-but-kept row is finalized the same way unless it is a
// fresh `starting` row.
func (s *Service) reapDeadRow(ctx context.Context, externalID string) (deadRowKind, store.Session, error) {
	kind := deadGone
	var out store.Session
	err := s.withLock(externalID, func() error {
		r, ok, err := s.d.Store.GetByExternalID(ctx, externalID)
		if err != nil || !ok {
			return err
		}
		if s.d.Tmux.HasSession(TmuxName(s.d.Prefix, externalID)) {
			kind, out = deadLive, r
			return nil
		}
		fresh := s.isFreshStarting(r)
		phantom := !s.claudeSessionResumable(r) && !fresh
		if phantom || !fresh {
			if err := s.endOpenRun(ctx, externalID, "exited", store.RunEndReaper, r.LastActivityAt); err != nil {
				return err
			}
		}
		if phantom {
			// Resolve the metric attributes and log args BEFORE the delete:
			// Store.Delete also removes the session's metadata, so anything
			// resolved afterwards would always be unlabelled.
			attrs := s.metricAttrs(externalID)
			logArgs := sessionLogArgs(externalID)
			if err := s.deleteSessionLocked(ctx, externalID); err != nil {
				return err
			}
			recordReapPhantomPruned(attrs)
			slog.Info("ccpool: reap pruned phantom session", logArgs...)
			return nil
		}
		kind, out = deadKeptRow, r
		return nil
	})
	return kind, out, err
}

// sessionStateCounts buckets the surviving registry rows by (state, live) for
// ccpool_session_states. Every known state is emitted for both liveness values
// (zeros included) so a bucket that emptied reads 0, not its stale last value.
// live=false,state=working is the dead-but-working signal; live=true,
// state=needs_input is a session parked for a human; live=false,
// state=needs_input is a dead row still awaiting one (awaitingHumanDead), and
// drops out of that bucket once it is closed (ADR 0086).
func sessionStateCounts(live, dead []store.Session) []telemetry.SessionStateCount {
	states := []store.State{store.Starting, store.Ready, store.Working, store.NeedsInput, store.Idle, store.Errored}
	counts := map[store.State][2]int64{} // [0]=dead, [1]=live
	for _, r := range live {
		c := counts[r.State]
		c[1]++
		counts[r.State] = c
	}
	for _, r := range dead {
		if r.State == store.NeedsInput && !awaitingHumanDead(r) {
			continue // closed: no longer awaiting a human (ADR 0086)
		}
		c := counts[r.State]
		c[0]++
		counts[r.State] = c
	}
	out := make([]telemetry.SessionStateCount, 0, 2*len(states))
	for _, st := range states {
		out = append(out,
			telemetry.SessionStateCount{State: string(st), Live: true, Count: counts[st][1]},
			telemetry.SessionStateCount{State: string(st), Live: false, Count: counts[st][0]})
	}
	return out
}
