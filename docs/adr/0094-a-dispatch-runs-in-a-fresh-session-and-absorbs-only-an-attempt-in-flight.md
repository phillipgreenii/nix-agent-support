# A dispatch runs in a fresh session and absorbs only an attempt still in flight

**Status**: Accepted (supersedes 0082's Decision item 4; amends 0083's Decision item 5 and 0085's answer to question 3; resolves `pg2-yunrp`)
**Date**: 2026-10-13
**Deciders**: Phillip Green II

## Context

A `pg-router-ccpool-handler` dispatch looks for a session the role's pool already holds under the
bead's stable per-bead name, and if it finds one it absorbs it: it waits on that session as its own
instead of launching one. ADR 0082's Decision item 4 made a handler-closed settled row such a
duplicate, so that a crash-window redelivery of an already-settled event would not launch a second
session. Each failure of that rule since then was fixed by narrowing it with one more bound:

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
   earlier dispatch launched, except in item 3. A new event, a re-dispatch at a new head, a
   re-request after a hand-back, release, budget stop or failure, a reopened bead, and any settled
   session (gone, or its turn ended with no handler holding its lease), whoever closed it, all get a
   fresh session.
2. **In flight.** An earlier attempt is in flight while its session is live and a turn is under way
   or paused for a person (`working`, `needs_input`), or a handler holding its lease is still
   launching it or settling it. One bead and role have at most one attempt in flight: no dispatch
   launches a session beside it.
3. **The one exception: absorb the same event's attempt.** A dispatch absorbs the session when its
   attempt is in flight with its turn not yet ended, it records the dispatch's own event id, and
   nobody has closed it or begun to tear it down. This is the case ADR 0085 depends on (the re-offer
   after a daemon restart that left the dispatch running), plus a crash-window redelivery and a
   redelivery that finds a dead handler's session still at work within its budget (ADR 0083). The
   head is not compared: a dispatch re-reads its bead's head when it starts, so a redelivery after a
   head advance carries the new head while the attempt it duplicates still works on the old one, and
   launching beside it would be worse; the source asks again for the new head once the attempt ends.
   The absorbing dispatch sends the session nothing and measures its budget from the original launch,
   which is honest here because it is the same attempt still running: an attempt already past its
   budget has been stopped by its own watchdog, or by the orphan reconcile, before the dispatch looks.
4. **Any other attempt in flight is declined.** An attempt in flight for another event (or for an
   event that cannot be compared), one whose turn has ended while its handler is still settling it,
   and one someone closed while it still runs are neither absorbed nor launched beside: the dispatch
   returns the pre-accept busy decline with reason `session-in-flight`, mutating nothing, and the core
   re-offers it once the attempt is over.
5. **A launch nobody finished is closed and replaced.** A session still `starting` or `ready` whose
   lease has expired never received a prompt and its dispatch is gone. The dispatch closes it
   (without purging) and launches its own session, rather than handing it the new prompt as the
   handler does today (bead `pg2-oq6cy`): the leftover's clock started at the earlier launch, and its
   worktree was prepared for an earlier item.
6. **Settled rows are never absorbed.** This supersedes ADR 0082's Decision item 4 and every bound
   on it. The marks that only kept a settled row from being absorbed (the outcome mark of
   `pg2-tc9c3`, and the event and head comparison on settled rows) are no longer required by the
   behavior docs; the code-alignment bead `pg2-qaeb8` removes what the flipped default makes
   redundant rather than adding another bound, and adds a hermetic test against the real ccpool for
   each case above.
7. **Behavior docs change in the same change.** `INV-CCH-30` is new; `INV-CCH-2`, `INV-CCH-14`,
   `INV-CCH-17`, `INV-CCH-18`, `INV-CCH-20`, `INV-CCH-21`, `INV-CCH-22`, `INV-CCH-26`, `INV-CCH-28`
   and `INV-CCH-29`, the `INTF-CCH-CCPOOL` crossing, the dispatch and release use cases and the
   glossary now cite it, and the set's realization-gap register carries the rows `pg2-qaeb8` closes.

## Consequences

- A re-review after a head advance, a re-request after a hand-back and a reopened bead always get a
  fresh session, so a budget clock never starts at someone else's launch.
- The restart path of ADR 0085 is unchanged while the session's turn is under way. A restart that
  lands while the old handler is still settling a finished turn now declines and is re-offered
  instead of being absorbed, so the absorbing dispatch can no longer hard-stop a finished session
  whose elapsed time passed the cap during its settle step.
- Accepted: a redelivery of an event whose attempt already settled (the narrow crash window, or a
  restart that lands as the attempt settles) launches one fresh session against a closed bead for a
  role without a pre-launch check (`INV-CCH-22`). The dispatch-time reconcile already purged such a
  row before the absorb check in the usual case, so this was already the usual outcome.
- Accepted: a settled session whose subagents are still writing after its handler has gone is not in
  flight, so in a pool with a free slot a fresh session can start in the shared per-bead worktree
  before they stop. A one-slot pool declines at capacity instead (`INV-CCH-6`).
- One extra launch replaces a leftover launch; the cost is seconds.
- Rejected: keeping a settled row absorbable when it served the same event at the same head and its
  dispatch completed the bead. The dispatch-time reconcile purges such a row first in the usual case,
  a pre-launch check answers the redelivery at zero model cost, and keeping it would keep the path
  behind every false budget stop so far.
- Rejected: a fourth bound, such as putting the head into the event id (option (a) of `pg2-afre3`).
  The event id is the core's dedup key (`INV-EVT-3`), and it still could not tell a re-request after
  a hand-back from a redelivery.
- Rejected: absorbing any live session of the bead and role whatever its event. It would make a
  different request adopt another event's work instead of getting a session of its own.
