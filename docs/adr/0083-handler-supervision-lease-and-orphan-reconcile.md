# A handler's supervision lease, and an orphan reconcile that acts on it

**Status**: Accepted (amends 0072 and 0082); amended 2026-10-13 by bead `pg2-zybju`: a drain session stuck in `needs_input` is cleared after a bound (see the Amendment at the end); Decision item 5 amended 2026-10-13 by 0094 (bead `pg2-yunrp`)
**Date**: 2026-10-06
**Deciders**: Phillip Green II

> **Later note (2026-10-13, this ADR's Amendment, bead `pg2-zybju`):** Decision item 3's bullet
> "`needs_input`: unchanged (ADR 0037)" no longer holds for a SUPERVISED drain session. The
> dispatch that supervises it clears a drain session that has sat in `needs_input` past a bound
> (default 15 minutes): Escape, then close and release its claim. The orphan reconcile itself still
> leaves a `needs_input` orphan alone.

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
   - `needs_input`: unchanged (ADR 0037). A SUPERVISED drain session is bounded by the Amendment of
     2026-10-13 below; an orphan is not.
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
   settling, not to rows it reclaimed). **Later note (2026-10-13, ADR 0094, bead `pg2-yunrp`):**
   ADR 0082's settled-row rule is superseded, and a dispatch absorbs only the same event's turn
   still in progress. An orphan is therefore absorbed only while its turn is in progress, and a
   reclaimed orphan, being closed, is never absorbed whether or not it carries the stamp. An
   under-budget `starting` or `ready` orphan that the reconcile leaves alone is then closed and
   replaced by the dispatch as a leftover.
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

## Amendment 2026-10-13: a drain session stuck in needs_input is cleared after a bound

**Deciders**: Phillip Green II (operator ruling, 2026-10-12, bead `pg2-zybju`: the handler clears a
drain session left in `needs_input` beyond N minutes, "send Escape; if still stuck, close the
session and release the bead claim in ONE call"; "N = 15 minutes unless the implementer finds reason
to differ", and the operator accepted that recommendation without choosing another N). This amends
ADR 0037's Decision items 3 and 6 (preservation is unbounded; only an operator moves a
`needs_input` session out of the pool) for one class of session, and this ADR's Decision item 3
(`needs_input`: unchanged). The rule is `INV-CCH-29` in the handler's behavior docs.

### Context

An unattended drain worker (ADR 0089) that raises a permission dialog nobody can answer parks in
`needs_input` until a person attaches. Six such stalls were recorded between 2026-10-10 and
2026-10-13 (five in drain sessions, one in a review session), each holding its bead's claim and its
pool's slot, the longest for about 80 minutes. The triggers differed (a read of a secret-looking path, a `PATH` replacement, a command
that is on the allow list), so a bound on the stall is the one remedy that covers all of them.

Three facts shaped the design:

- The wedged dispatch is the one holding the lane. A drain pool has one slot and one in-flight
  dispatch per tracker, so no later dispatch of the role runs while this one is blocked, and a bound
  that waits for the next dispatch's reconcile can never fire. The bound lives in the SUPERVISING
  dispatch's wait loop, which is alive and polling throughout.
- ccpool leaves `needs_input` only at the next `Stop`: no hook edge fires when a dialog is
  answered. A session whose dialog a person answered, or whose blocked subagent its orchestrator
  stopped, still reads `needs_input` while it works, and a drain orchestrator's turn can last hours.
  A bound keyed on the state alone would Escape, and then kill, a healthy session.
- A budget reminder is a pasted prompt followed by Enter, and Enter on a pending dialog accepts it.
  One was observed approving a pending tool call 0.66 seconds after it was pasted, an approval nobody
  read.

### Decision

1. **Scope.** The bound applies to a drain role only: a ccpool role whose completion is
   `close-or-release` (`INV-CCH-28`). Every other role, and every session of the shared default
   pool, keeps ADR 0037's rule: a `needs_input` session MUST be spared for a human. ccpool's reaper
   is unchanged and still spares every `needs_input` row; the bound is the handler's.
2. **The clock counts QUIET `needs_input`** and runs on the dispatch's own poll (`pollInterval`,
   default 10 seconds) from the injected clock. It starts when the session is first seen in
   `needs_input`, or at the row's `last_activity_at` when that is older and the transcript can be
   observed, so a handler restart that absorbs the session (ADR 0085) does not grant a fresh bound.
   It restarts at the newest write to the session's transcript or its subagents' transcripts,
   because a session blocked on a dialog writes nothing and a healthy one writes on every tool call.
   A poll that sees any other state ends the episode and resets the clock. A transcript that cannot
   be observed is no evidence of life and does not delay the bound.
3. **The bound is 15 minutes.** The handler config key is `needsInputStall` (a nanosecond integer
   like its other durations; `0` disables the bound everywhere; a value under six polls is a config
   error naming the key). A role MAY override it for its own dispatches with `needsInputStall` in
   its role file (a duration string, greater than zero).
4. **Escape, then close.** At the bound the handler sends Escape through `ccpool cancel` (an Escape
   burst and a confirmed-static pane, which also resets the row to `ready`). If the transcript then
   stays quiet for a GRACE, one worktree quiet window (default 2 minutes, never under two polls),
   the session is stuck for good: the handler closes it (non-purge, reason `handler`, so the row and
   the transcript stay) and releases the claim. The bounded gap is the grace, and from the first
   `needs_input` poll to the close the worst case is the bound plus the grace plus one poll: 17
   minutes 10 seconds by default. A write after the Escape means the session reacted to it, and ends
   the episode instead: nothing is closed.
5. **The release is guarded and is ONE bd call.** The handler reads the bead first; if it cannot,
   it changes nothing and retries on the next poll, because closing the session over an unreadable
   claim would strand the claim. It then closes the session and only then releases, so no other
   worker takes a bead whose session is still live. It MUST release only a bead that is
   `in_progress` and assigned to the role's own actor, with the single call
   `bd update <id> --status=open --assignee=` (status and assignee together, never two calls). A
   bead that is closed is left to the completion check. A bead held by anyone else, or already open
   and unassigned, is never written
   to (the stalled session is still closed, to free its slot), and a peer-held bead ends the
   dispatch with no verb, like any ended session of a peer's bead (`INV-CCH-28`). A close that fails
   leaves the claim alone and ends the dispatch with an error.
6. **Two strikes.** The first stall of a bead adds the label `drain-needs-input-stall` and a comment
   saying why the claim was released. The second consecutive stall of the same bead adds `human` in
   the SAME call as the release, so a bead that wedges every worker stops being re-offered, as
   `pool-evicted` and `drain-unclaimed-end` already do for their causes. A completed dispatch removes
   the label.
7. **Surfacing.** The handler event log records `needs_input_escape` and `needs_input_cleared`
   (session, bead, role, pool, seconds quiet, the bound, and for the clear whether the claim was
   released and whether `human` was added), beside the existing `needs_input` alert. The handler
   files no escalation bead of its own for a stuck session: the one `ccpool-probe` files is left to
   its normal triage, and a cleared session is no longer stuck. Nothing here creates a second one.
8. **The budget watchdog never pastes into a `needs_input` session.** Its reminder is held back
   while the row reads `needs_input`, or cannot be read: the level is not latched, so the reminder is
   sent on the first poll after the session leaves `needs_input`, and the hold is logged once as
   `reminder-suppressed` with reason `needs_input`. The wrap-up message that follows the 90% cancel
   is held the same way when the cancel left the row in `needs_input`. Skipping was chosen over
   sending Escape first because dismissing a dialog is a decision that belongs to one place, the
   bound above and only for a drain role. A reminder that dismissed dialogs would do it at an
   arbitrary budget percentage and for every role, which would contradict ADR 0037 for the sessions
   that still have a human to wait for.

### Consequences

- A drain session blocked on a dialog is cleared without a person in 17 minutes 10 seconds at the
  longest by default, and its bead returns to the pool unclaimed, so the lane works the next bead.
  A bead that stalls twice in a row stops being offered.
- The bound is a bound on silence, not proof of a dialog. A session that sits silent in a foreground
  command longer than the bound while its row reads `needs_input` is Escaped, and later closed. The
  quiet-transcript rule narrows that and cannot close it; the remedy for a role that runs long
  foreground commands is a longer role-level `needsInputStall`.
- Known limitation: the orphan reconcile still leaves a `needs_input` orphan alone (its handler died
  and its lease expired), and it runs only when its role next dispatches. Such a session is bounded
  by an operator, or by ccpool's `dead_needs_input_ttl` once its pane is gone (ADR 0086). A daemon
  restart does not make a session an orphan (ADR 0085), so this is the rare case.
- The cleared row keeps its last observed state (ADR 0015), so the dispatch-time worktree cleanup
  treats it as still parked and leaves its per-bead worktree for the bead's next dispatch to reuse.
- Rejected: running the bound from a next-dispatch hook or the orphan reconcile. The wedged dispatch
  holds the lane, so neither runs while the session is stuck.
- Rejected: Escape-first for the budget watchdog, for the reason in item 8.
- Rejected: re-prompting the session after the Escape. The bound exists to free the lane and the
  claim, not to coax a worker that stopped on a dialog nobody can read.
- Rejected: a bound in ccpool's reaper. ccpool tracks session facts and knows nothing of bead claims
  (ADR 0015), so it could close the session but not release the claim in the same breath.
- Not changed: ADR 0037's rule for every session that is not a drain session, and the dead
  `needs_input` row backstop of ADR 0086.
