# A gated dispatcher closes the sessions it settles

**Status**: Accepted (amends 0072); item 4 bounded by an event-id check (2026-10-06, bead `pg2-uprw5`) a pinned-head check (2026-10-07, bead `pg2-afre3`) and an outcome check (2026-10-08, bead `pg2-tc9c3`)
**Date**: 2026-10-05
**Deciders**: Phillip Green II

## Context

ADR 0072 made the pool's cap an admission gate: a programmatic dispatcher does not launch at
`free == 0`, and cap eviction only fires when `counted > max_sessions`. A dispatcher-only pool
that honors the gate never goes over cap, so eviction never runs. ADR 0015 said the dispatcher
purges its session on teardown, but the handler's tail (`finishWait`) only removed the worktree,
and the code comment "nothing auto-closes a settled session" described that gap.

A settled session therefore stayed live and `idle`, counted against `max_sessions`, until
`idle_ttl` (default 30 minutes) plus up to one 5-minute reap tick. The review pool has
`max_sessions = 1`, so each finished review whose bead stayed open (handed back, deferred, human)
blocked every review dispatch for 30 to 35 minutes. Observed 2026-09-24 to 2026-10-04: 30
`close reason=idle_ttl` events on the review pool, 27 of them after a normal handler completion;
on 2026-10-05, 75 "pool at capacity" declines in 35 minutes while an idle session held the slot.
The dispatch-time reconcile does not help: it closes only sessions whose bead is already closed.

## Decision

1. **The handler closes the settled session it launched or absorbed** once the dispatch reaches
   a terminal outcome: success, an applied failure action (add-human, unclaim, handback) or a
   budget stop. The close is `ccpool close --reason handler` WITHOUT `--purge`. This narrows ADR
   0015's purge-on-teardown to a non-purge close: the store row and the resumable Claude
   transcript are kept, and the per-dispatch worktree is already gone, so the live pane gives an
   operator nothing extra.
2. **The close is guarded.** It closes only the dispatch's own `ExternalID`, never another row,
   and only when all hold: neither the context nor the wait error is a cancellation (daemon
   shutdown is owned by the pre-shutdown sweep); the session is quiet for every isolation type
   (the existing worktree quiet window, covering the session's Agent-tool subagents, because
   closing the tmux pane would kill them); and a fresh list shows the row present, with no
   close reason, in state `idle` or `errored`. `needs_input` stays exempt (ADR 0037), as do rows
   that are starting, ready or working, and rows already closed by the budget watchdog, the
   unexplained-death branch or `ccpool` itself.
3. **A close failure is best effort.** It is logged at WARN and never changes the dispatch
   result; the session is left to `idle_ttl`. A deferred close (subagents active) is logged at
   INFO and likewise left to `idle_ttl`.
4. **A handler-closed settled row is still a duplicate.** Because the first dispatch now stamps
   `handler` on its own settled row, duplicate absorption (`INV-EVT-2`) treats a row with close
   reason `handler` in state `idle` or `errored` as a settled duplicate to absorb, instead of
   launching a second session for an already-settled bead. Rows closed with `idle_ttl`,
   `cap_eviction` or `operator`, and non-terminal dead rows, stay absent. **Bounded by event
   (2026-10-06, bead `pg2-uprw5`):** the launching dispatch stamps the id of the pg-router event it
   serves on the session (`pgrouter.event_id`, written by `ccpool new --meta`). A handler-closed
   settled row is absorbed only by a dispatch carrying that same event id, which is what a
   crash-window redelivery of the accepted event does. A later dispatch for the same bead and role
   is a new event with a different id (for example a review bead reopened after a head advance),
   and launches a fresh session instead. A handler-closed row with no recorded event id (launched
   by an older build) cannot be proven a redelivery and is not absorbed; a dispatch with no event
   id applies no bound. Rows that are still open are judged as before. **Refined by head
   (2026-10-07, bead `pg2-afre3`):** the event id is deterministic per event type and bead
   (`review.ready:<bead>`), so a re-review after a head advance carries the SAME id and the check
   above never fired for it; the re-dispatch absorbed the settled row and measured its time budget
   from the original launch, tripping an instant false "session budget exceeded". The launching
   dispatch therefore also stamps the item's pinned head (`pgrouter.head_sha`), and a handler-closed
   row is absorbed only when the head also matches. A dispatch with no `head_sha` (a worker or
   feedback bead) applies no head bound; a row with no recorded head (older build) is not absorbed.
   The event id is deliberately left unchanged: it is the core's dedup key (INV-EVT-3).
   **Bounded by outcome (2026-10-08, bead `pg2-tc9c3`):** event id and head together still cannot
   tell a crash-window redelivery from a re-request after a HAND-BACK. The review session unclaims
   its bead and goes idle, the handler closes the row, and the bead is open again at the same
   head, so the review source re-emits the same event; absorbing the row started the budget at its
   original launch and hard-stopped instantly about every minute, and because the escalation count
   is per distinct session it never reached the human threshold. The handler now stamps
   `pgrouter.incomplete` on a row whose dispatch ended with its bead still open: a hand-back of a
   close-or-handback role or a failure (stamped in `closeSettledSession`, or, when the wait's
   unexplained-death branch closes the row itself, by that branch), and a budget hard stop (stamped
   by the watchdog before its own close). It is always stamped BEFORE the close, and an incomplete
   row is absent to every later dispatch. Only a row whose bead was closed stays a duplicate.
   Chosen over purging the row on settle (option (c) of `pg2-afre3`) because a non-purge close
   keeps the resumable transcript of a session whose bead is still open, which this ADR's item 1
   exists to preserve. Rows the deployed handler wrote before this change carry no marker; absorbing
   one ends in a failed dispatch, either the watchdog hard stop (finite budget) or the wait's
   session-exited-before-completing branch (unlimited budget, or whichever wins the race), and both
   stamp the row, so the next dispatch launches fresh. A bead that was closed in the meantime is a
   success and stays absorbable. Residual: the stamp is best effort (a failure is logged at
   error level), so a failed stamp leaves the row absorbable until the next failed absorb stamps
   it, and a stamped row whose close then failed stays live, and absorbable, until `idle_ttl`.

## Consequences

- A finished session frees its counted slot as soon as it is quiet, so the `max_sessions = 1`
  review pool is no longer blocked for 30 to 35 minutes by a finished review.
- The row stays resumable only while its bead is open: once the bead is closed, the existing
  dispatch-time reconcile purges it at the next dispatch.
- Former limitation, resolved by bead `pg2-uprw5` (2026-10-06): a handler-closed `idle` row for a
  bead that was later re-dispatched (for example a review bead reopened after a head advance)
  matched the redelivery check by name and was absorbed rather than relaunched, for as long as the
  row existed. Item 4 is now bounded by the event id, so only a redelivery of the same event is
  absorbed. An unclosed settled row (a failed or deferred close) is still matched by name alone,
  as before.
- Orphaned sessions whose handler died (daemon restart, crash) are not covered here; they need a
  supervision step in the reconcile. Later note (2026-10-06): ADR 0083 adds that step, a
  supervision lease the handler keeps fresh and a role-scoped orphan reconcile that acts on an
  expired one.
- Later note (2026-10-07, bead `pg2-uyahp`): the slot stays counted from the last `working` to
  `idle` transition until the close, which waits for the quiet window and the worktree removal.
  Measured over 23 review dispatches that gap was a median of 206 seconds (about 23 percent of a
  session's slot hold). A role may therefore override the quiet window for its own dispatches
  (`INV-CCH-23`); the default and every role that does not set it stay at 2 minutes. The
  trade-off is the straggler-subagent risk (a subagent silent for longer than the window is
  mistaken for finished), which is why the override is per role and opt-in rather than a shorter
  default.
- Rejected: leaving the slot to `idle_ttl` and shortening it. It also shortens the life of
  sessions that are legitimately idle between turns, and adds no signal that the dispatch is
  finished.
- Rejected: purging on teardown (ADR 0015 as written). It discards the resumable transcript of a
  session whose bead is still open.
