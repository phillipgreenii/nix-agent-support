# A gated dispatcher closes the sessions it settles

**Status**: Accepted (amends 0072)
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
   `cap_eviction` or `operator`, and non-terminal dead rows, stay absent.

## Consequences

- A finished session frees its counted slot as soon as it is quiet, so the `max_sessions = 1`
  review pool is no longer blocked for 30 to 35 minutes by a finished review.
- The row stays resumable only while its bead is open: once the bead is closed, the existing
  dispatch-time reconcile purges it at the next dispatch.
- Known limitation: a handler-closed `idle` row for a bead that is later re-dispatched (for
  example a review bead reopened after a head advance) matches the redelivery check by name and
  is absorbed rather than relaunched, for as long as the row exists. Bounding that (for example
  to rows whose bead is already complete) is a follow-up, not part of this decision.
- Orphaned sessions whose handler died (daemon restart, crash) are not covered here; they need a
  supervision step in the reconcile.
- Rejected: leaving the slot to `idle_ttl` and shortening it. It also shortens the life of
  sessions that are legitimately idle between turns, and adds no signal that the dispatch is
  finished.
- Rejected: purging on teardown (ADR 0015 as written). It discards the resumable transcript of a
  session whose bead is still open.
