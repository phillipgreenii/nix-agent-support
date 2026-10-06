# A handler's supervision lease, and an orphan reconcile that acts on it

**Status**: Accepted (amends 0072 and 0082)
**Date**: 2026-10-06
**Deciders**: Phillip Green II

## Context

A `pg-router-ccpool-handler` process is a per-dispatch child of the `pg-router` daemon. It alone
supervises the session it launched: the completion wait, the budget watchdog and the terminal
cleanup all die with it. If the handler dies, nothing supervises the session any more:

- Every daemon restart (every `darwin-rebuild` apply; four on 2026-10-05) drains for 20 seconds and
  then cancels in-flight dispatches, killing their handlers. The operator's pre-shutdown ruling
  (bead `pg2-hwt7v`) correctly spares working sessions, but its own text recorded the gap: no
  executor watches a spared session any more.
- A handler that gives up at `MaxWait`, or crashes, leaves its row `working`. A launch timeout
  whose purge-close fails twice still leaves an unsupervised row.

The effects are an unenforced budget, a bead that stays claimed, and an idle row that holds a pool
slot until `idle_ttl`. Observed 2026-10-05: a review session's handler was killed eight seconds
after the 90% wrap-up nudge, the session went idle, and it held the review pool's only slot for
about 35 minutes.

ADR 0082 closes a session the handler itself settles, so it does not help when the handler is gone.
The pre-shutdown sweep cannot help either: it lists only the daemon's default pool and never a
role's dedicated pool, although the behavior docs claimed it bounded spared sessions there. The
operator chose not to fix that sweep's pool coverage, because the mechanism below supersedes it, and
chose to correct the docs instead.

## Decision

Operator ruling (Phillip, 2026-10-05, bead `pg2-g2u9m`): a supervision lease to catch orphans.

1. **The handler keeps a lease.** The session metadata key `pgrouter.lease_until` (RFC3339 UTC)
   says until when a live handler vouches for the session. It is written atomically by
   `ccpool new --meta`, together with `pgrouter.launched_at`, at launch, with an initial value that
   covers the whole launch wait plus one TTL; from `Ensure` success (or absorb) until `Dispatch`
   returns, a background ticker refreshes it to now plus the TTL every poll interval. That covers
   the send, the wait, the watchdog, the worktree cleanup's quiet wait and the settled-session
   close. Neither key is ever a `ccpool` label: a per-poll value would churn telemetry. The TTL
   (`lease_ttl`, default two minutes) is validated at load to be at least ten poll intervals. A
   failed refresh is a WARN and never fails the dispatch; three consecutive failures are an ERROR.
2. **An orphan is a session of the role's own pool** with that role's `pgrouter` tags, still open,
   and an expired lease. A session with no lease (launched by an older build) is never an orphan;
   `ccpool`'s `idle_ttl` reaper bounds it.
3. **The role's own dispatch reconciles orphans**, in `runDispatch` after the closed-bead
   reconcile and before the capacity check, against the role's own pool. It is deliberately not
   run from the default-pool reconcile of the `query` subcommand, which has no role.
   - `idle` or `errored`, and transcript-quiet: apply the role's completion rule. A role that
     claims its bead (close-only, close-or-handback) unclaims it, with a comment, only when the
     bead is `in_progress` and assigned to the role's own actor; a role that never claims (the
     triage roles) writes nothing. Then close the session without purging (reason `handler`) and
     remove the worktree and anchor branch under the dispatch-time cleanup's guards: no live or
     `needs_input` peer on the same directory, `force=false` so a dirty tree is kept and logged.
   - `starting`, `ready` or `working`: measure the time budget from `pgrouter.launched_at`; over
     budget, run the same hard stop as a supervised session (the exported
     `watchdog.HardStop`), then remove the worktree under the same guards. Under budget, or a role
     with no time budget, leave it alone. A bead already closed is not reopened.
   - `needs_input`: unchanged (ADR 0037).
4. **Mutual exclusion is the handler's own flock**, not `ccpool`'s: `ccpool`'s per-session lock has
   no CLI, and `ccpool meta set` is an unconditional upsert, so the lease cannot serve as a
   compare-and-set. The reconcile takes a non-blocking exclusive `flock` on
   `<handler state dir>/locks/<external_id>.lock`, skips a session whose lock is held, and, holding
   it, re-lists and aborts if the lease was refreshed. An absorbing dispatch takes the same lock
   and refreshes the lease before it starts waiting, so the two cannot both act. The alternative of
   a `ccpool meta set --if-value` compare-and-set was not chosen: it needs a `ccpool` change for
   what a local lock does.
5. **Absorbing keeps the launch time.** The watchdog's start is the session's recorded launch time,
   so absorbing an under-budget orphan does not grant a fresh budget. A session reclaimed as an
   orphan is stamped `pgrouter.orphan_reclaimed` before it is closed, so that a redelivered
   dispatch treats the row as abandoned and launches afresh instead of absorbing it (ADR 0082's
   rule that a handler-closed settled row is a duplicate applies to rows the handler closed after
   settling, not to rows it reclaimed).
6. **Observability.** The event log records `orphan_reclaimed` and `orphan_hard_stop` with session,
   bead, role, pool, state, lease expiry and launch time, and each is also an INFO log line.

## Consequences

- A killed handler's idle session frees its slot, and its bead returns to the pool, on the role's
  next dispatch instead of 30 to 35 minutes later; a working orphan past its time budget is
  stopped.
- Known limitation: an orphan is handled only when its own role next dispatches. Until then only
  `idle_ttl` bounds it.
- Known limitation: a role with no time budget leaves a working orphan alone, and only the time
  budget is enforced for an orphan, not tokens or cost. Reading tokens and cost statelessly from the
  transcript is a possible follow-up.
- A live handler that cannot write its lease for longer than the TTL can have its session
  reclaimed or stopped under it; the ERROR log after three consecutive failures exists to make
  that visible.
- The behavior docs no longer claim the pre-shutdown sweep covers dedicated pools.
- Rejected: fixing the pre-shutdown sweep's pool coverage. It would add a per-pool sweep that still
  cannot cover a crash outside shutdown.
- Rejected: a `ccpool`-side compare-and-set. See decision item 4.
