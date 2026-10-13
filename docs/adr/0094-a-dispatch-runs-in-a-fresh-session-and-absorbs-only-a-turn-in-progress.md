# A dispatch runs in a fresh session and absorbs only the same event's turn in progress

**Status**: Accepted (supersedes 0082's Decision item 4; amends 0083's Decision item 5 and 0085's answer to question 3; narrows 0065's `INV-EVT-2` closure; resolves `pg2-yunrp`)
**Date**: 2026-10-13
**Deciders**: Phillip Green II

## Context

A `pg-router-ccpool-handler` dispatch looks for a session the role's pool already holds under the
bead's stable per-role name, and if it finds one it absorbs it: it waits on that session as its own
instead of launching one. ADR 0065 required that memory so a crash-window redelivery would not start
a second session, and ADR 0082's Decision item 4 extended it to a handler-closed settled row, so that
a redelivery of an already-settled event would not launch a second session either. Each failure of
that rule since then was fixed by narrowing it with one more bound:

- by the event id (2026-10-06, bead `pg2-uprw5`), so a later event for the same bead launches fresh;
- by the item's pinned head (2026-10-07, bead `pg2-afre3`), because the event id is the same for
  every event of one type and bead, so a re-review after a head advance carried the same id,
  absorbed the settled row and was hard-stopped at once on a budget measured from the original
  launch (4065 seconds, then 5659, against a 1500 second cap);
- by the outcome (2026-10-08, bead `pg2-tc9c3`), because a hand-back leaves the bead open at the
  same head and the source re-emits the same event, which looped (bead `pg2-92rfu`: one row closed
  268 times).

The verification of the head bound (bead `pg2-pq2ej`) then found the path it guards almost
unobservable live: a settled review row is purged about 20 minutes after it settles, queue latency
is hours, and the shortest gap between a close and the next launch of the same bead in five days was
47 minutes. The bounds had become the only defence against a default that kept producing false
budget stops, and none of them could be watched working.

Operator ruling (Phillip, 2026-10-12, recorded while unblocking `pg2-pq2ej`), verbatim: "in docs, i
think we need to update them such that re-using a ccpool session should not be the default, for this
very reason. so if there is new review, then there should be a new session. re-using a session
should be rare."

## Decision

The rule is `INV-CCH-30` in `packages/pg-router-ccpool-handler/docs/behavior/invariants.md`.

1. **Fresh by default.** Every dispatch of a ccpool-backed role runs in a fresh session it launches
   itself, with its own launch time, budget clock and supervision lease. It never adopts a session an
   earlier dispatch launched, except in item 3. Once no earlier attempt of the bead and role is in
   flight, a new event, a re-dispatch at a new head, a re-request after a hand-back, release, budget
   stop or failure, a reopened bead, and any settled session (its turn ended, or no longer live),
   whoever closed it, all get a fresh session.
2. **In flight.** An earlier attempt is in flight while its turn is in progress (the session is live
   and `working` or `needs_input`), or while a handler still holds its supervision lease, whatever its
   state and whether or not it is live: launching it, waiting on it, settling it or handling its
   death. One bead and role have at most one attempt in flight: no dispatch launches a session beside
   it. Every session of the bead and role counts, and a pool that cannot be read counts as one in
   flight.
3. **The one exception: absorb the same event's turn in progress.** A dispatch absorbs the session
   when its turn is in progress, it records the dispatch's own event id, nobody has closed it or begun
   to tear it down, and its bead is not already closed. This is the case ADR 0085 depends on (the
   re-offer after a daemon restart that left the dispatch running), plus a crash-window redelivery and
   a redelivery that finds a dead handler's session still at work (ADR 0083). The head is not
   compared: a dispatch re-reads its bead's head when it starts, so a redelivery after a head advance
   carries the new head while the turn it duplicates still works on the old one, and launching beside
   it would be worse; the source asks again for the new head once the attempt ends. The absorbing
   dispatch sends the session nothing and measures its budget from the original launch, which is
   honest here because it is the same attempt still running: a stop it triggers is the stop the
   attempt's own watchdog would have made.
4. **Any other attempt in flight is declined.** A session still being launched, one whose turn has
   ended (or whose session has died) while its handler still holds the lease, one for another event or
   an event that cannot be compared, one someone closed while it still runs, one whose bead is already
   closed, and a pool that cannot be read are neither absorbed nor launched beside: the dispatch
   returns the pre-accept busy decline with reason `session-in-flight`, mutating nothing, and the core
   re-offers it later. Like a full pool this is routine backpressure, so the failure-rate alert does
   not count it. A session still being launched is declined rather than absorbed so that an absorbing
   dispatch never waits on a session that may never receive its prompt.
5. **A leftover nobody supervises is closed and replaced.** A session still `starting` or `ready`
   whose lease has expired has no handler and no turn: a launch that failed or whose handler died
   before its prompt was taken, or a session a cancel left `ready` after its handler had gone. The
   dispatch closes it (without purging, under the per-session lock of ADR 0083) and launches its own
   session, rather than handing it the new prompt as the handler does today (bead `pg2-oq6cy`): the
   leftover's clock started at the earlier launch, and its worktree was prepared for an earlier item.
6. **Settled rows are never absorbed.** This supersedes ADR 0082's Decision item 4 and every bound on
   it. The marks that only kept a settled row from being absorbed (the outcome mark of `pg2-tc9c3`,
   and the event and head comparison on settled rows) are no longer required by the behavior docs; the
   code-alignment bead `pg2-qaeb8` removes what the flipped default makes redundant rather than adding
   another bound, and adds a hermetic test against the real ccpool for each case above.
7. **Behavior docs change in the same change.** `INV-CCH-30` is new; `INV-CCH-2`, `INV-CCH-14`,
   `INV-CCH-17`, `INV-CCH-18`, `INV-CCH-20`, `INV-CCH-22`, `INV-CCH-28` and `INV-CCH-29`, the
   `INTF-CCH-CCPOOL` crossing, the dispatch and release use cases and the glossary now cite it;
   `INV-CCH-21` and `INV-CCH-26` drop their mentions of an absorbed duplicate and of the incomplete
   marker; and the set's realization-gap register carries the rows `pg2-qaeb8` closes.

## Consequences

- A re-review after a head advance, a re-request after a hand-back and a reopened bead always get a
  fresh session, so a budget clock never starts at someone else's launch.
- The restart path of ADR 0085 is unchanged while the session's turn is in progress. A restart that
  lands while the old handler is still launching the session, or still settling a finished turn, now
  declines and is re-offered instead of being absorbed, so the absorbing dispatch can no longer
  hard-stop a finished session whose elapsed time passed the cap during its settle step, or wait on a
  session that never receives its prompt.
- ADR 0065's `INV-EVT-2` closure required that a crash-window redelivery not start a second session.
  That still holds while the earlier turn is in progress. For an attempt that has already settled, a
  redelivery now runs a fresh session, which ADR 0065 counted as the defect; the ruling accepts it
  because the settled-row path is what produced every false budget stop.
- Accepted: a role without a pre-launch check (`INV-CCH-22`) launches one fresh session for a dispatch
  whose bead is already closed (a redelivered event, or an event that was declined while an earlier
  attempt ran and closed the bead). The dispatch-time reconcile already purged such a row before the
  absorb check in the usual case, so this was already the usual outcome.
- Accepted: a declined event is re-offered only until it expires. A born-expired event (a drain
  event) is dropped and re-derived on its source's next query, and an event of another type can expire
  while an attempt of the same bead and role stays in flight.
- Accepted: a lease outlives a handler that returned early by whatever is left of it (for a failed
  launch, up to one launch wait plus one lease TTL), so a later dispatch declines for that long before
  it closes and replaces the leftover. Requiring a returning handler to end its own lease would shorten
  that window; it is left as an option for `pg2-qaeb8`, because a handler whose session another
  dispatch absorbed must not end the lease that dispatch now refreshes.
- Accepted: a settled session whose subagents are still writing after its handler has gone is not in
  flight, so in a pool with a free slot a fresh session can start in the shared per-bead worktree
  before they stop. A one-slot pool declines at capacity instead (`INV-CCH-6`).
- Accepted, unchanged from ADR 0085: a redelivery that arrives while the earlier dispatch is still
  creating its session, before the pool lists it, cannot see it, and a pool with a free slot can then
  run two sessions for the bead and role.
- One extra launch replaces a leftover launch; the cost is seconds.
- Rejected: keeping a settled row absorbable when it served the same event at the same head and its
  dispatch completed the bead. The dispatch-time reconcile purges such a row first in the usual case,
  a pre-launch check answers the redelivery at zero model cost, and keeping it would keep the path
  behind every false budget stop so far. In the handler as it stands, such a false stop on a bead the
  attempt had already closed also releases the claim, which reopens the bead, unless that stop
  reaches the escalation threshold.
- Rejected: a fourth bound, such as putting the head into the event id (option (a) of `pg2-afre3`).
  The event id is the core's dedup key (`INV-EVT-3`), and it still could not tell a re-request after
  a hand-back from a redelivery.
- Rejected: absorbing any live session of the bead and role whatever its event. It would make a
  different request adopt another event's work instead of getting a session of its own.
