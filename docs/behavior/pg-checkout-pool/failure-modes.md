# pg-checkout-pool — failure modes and operations

This doc owns what the pool does when something goes wrong or needs watching: a crashed owner, an
interrupted reset, a damaged slot, a wedged git process, a full disk, the pool's own growth and size, the
guards the existing checkout code already gives and the pool MUST keep, and the metrics and alerts that
show the pool working. How a lease works is in [`lease-api.md`](lease-api.md); how a slot is reset is in
[`slot-reset.md`](slot-reset.md). It traces to the Pool report's crash recovery, failure modes and
existing safety properties.

## Stories

- **`STORY-POOL-CRASH-RECOVERY`** <!-- uuid: f5f50908-2e12-4995-90e4-dfc598e08635 --> — As the operator, I want a handler or session that dies mid-lease to leave nothing
  stuck, so the next janitor pass saves the work, resets the slot and frees it without anyone deleting a
  directory. _(→ `JOURNEY-POOL-LEAK-RECLAIMED`; `GOAL-POOL-SELF-HEALING`, `INV-POOL-8`, `INV-POOL-9`, `INV-POOL-10`.)_
- **`STORY-POOL-QUARANTINE`** <!-- uuid: 22fe4b3d-9dfb-4f4a-91fd-1b0700f515df --> — As the operator, I want a damaged slot taken out of rotation, kept intact and
  alerted, and a replacement grown, so one bad slot never blocks work or destroys evidence. _(→
  `JOURNEY-POOL-QUARANTINE`; `INV-POOL-12`, `INV-POOL-16`.)_
- **`STORY-POOL-BOUNDED-GROWTH`** <!-- uuid: eab52938-51da-43e4-a7a6-d754575c4c15 --> — As the operator, I want the pool to grow one slot at a time, never past its
  maximum or the free-space floor, so a burst of requests cannot fill the disk or flood the file-system
  event service. _(→ `JOURNEY-POOL-QUARANTINE`; `GOAL-POOL-BOUNDED`, `INV-POOL-12`.)_
- **`STORY-POOL-OBSERVE`** <!-- uuid: 36a43808-e962-45be-aae2-9b320645f95f --> — As the operator, I want metrics and alerts for leaks, quarantines, waiting requesters
  and slow resets, so I learn of a problem before a session does. _(→ `JOURNEY-POOL-LEAK-RECLAIMED`,
  `JOURNEY-POOL-QUARANTINE`; `INV-POOL-22`.)_

## Journeys

### `JOURNEY-POOL-LEAK-RECLAIMED` — a handler dies and the monitor reclaims its slot <!-- uuid: 26d92dc0-b392-41ca-83d5-92bad5cf9f34 -->

**Primary actor:** `ACTOR-MONITOR`. **Other actors:** `ACTOR-OPERATOR`, `ACTOR-HOST`. _Requires:_
`INV-POOL-8`, `INV-POOL-9`, `INV-POOL-10`, `INV-POOL-22`. _Includes:_ `JOURNEY-POOL-DEAD-OWNER`.

1. A handler is killed mid-session. Its exclusive hold on the slot is released by the operating system
   (`INV-POOL-8`).
2. The monitor finds a lease past its expiry with a free hold, and asks the existing guards whether the
   slot may be touched: nothing live works in it and the session record agrees (`INV-POOL-8`).
3. Uncommitted work is rescued, the slot is reset and marked free, under bounded, hermetic repository
   commands (`INV-POOL-10`; see `JOURNEY-POOL-DEAD-OWNER`).
4. The leak metric returns to zero and no alert fires, or fires and clears (`INV-POOL-22`).

Extensions:

- 1a. The owner is alive but hung. The lease is not reaped; an alert is raised for the operator (`INV-POOL-8`,
  `INV-POOL-22`).
- 3a. A reset was interrupted earlier. The slot is in the resetting state with a free hold; the reset is
  re-run, and a second failure quarantines the slot (`INV-POOL-16`).
- 3b. The pool's audit and sweep tools run meanwhile. They skip the slot because it is registered as a
  pool slot (`INV-POOL-9`).

### `JOURNEY-POOL-QUARANTINE` — a slot is found damaged <!-- uuid: 7a5348ef-2595-4899-91a3-989cca21e004 -->

**Primary actor:** `ACTOR-MONITOR`. **Other actors:** `ACTOR-OPERATOR`, `ACTOR-HOST`. _Requires:_
`INV-POOL-10`, `INV-POOL-12`, `INV-POOL-16`, `INV-POOL-22`. _Includes:_ none.

1. A reset fails twice, or a fresh or non-empty lock is found, or the tree is not clean after reset, or
   the watch cannot be made to answer after one stop and respawn, or the slot's directory is not a
   checkout (`INV-POOL-16`).
2. The slot is taken out of rotation with its directory kept, and an alert is raised (`INV-POOL-16`,
   `INV-POOL-22`).
3. A stale, zero-byte lock older than the stale bound with no git process in the slot is removed under
   the slot's exclusive hold and the reset retried once before quarantine is chosen (`INV-POOL-10`, `INV-POOL-16`).
4. The pool grows a replacement, one slot at a time, within the maximum size and the free-space floor
   (`INV-POOL-12`).
5. The operator, or a bounded rate-limited reaper after a retention period, removes the quarantined slot
   (`INV-POOL-16`).

Extensions:

- 4a. Free space is below the floor. The replacement is not created and requests decline with `low-disk`
  (`INV-POOL-12`).
- 5a. The operator wants the state. It is still there (`INV-POOL-16`).

## Invariants

- **`INV-POOL-8`** <!-- uuid: 6ae3eade-d447-4504-be9f-747fcff218dd --> — Every guard the existing checkout code applies to a per-session checkout MUST apply to a slot,
  with "remove" replaced by "reset": a slot is reclaimed only if it is linked, on its own lease branch,
  unlocked by an operator, older than its grace period, named by no open or live session record, not the
  working directory of any live process, clean, without unique commits, and not held by a live dispatch.
  The two-phase ordering MUST hold: a session's pool record MUST NOT be purged until its slot lease is
  released or the slot is quarantined, and an interrupted release MUST be retried by the next reconcile.
  _(Pool report, `INV-POOL-8`.)_
- **`INV-POOL-9`** <!-- uuid: c0c52f7b-1c55-4693-aa89-4803e718794c --> — Slots MUST be registered as locked pool slots and MUST live under a directory that the
  worktree-keyed sweep and the checkout audit and disk-reclaim tools exclude, so no existing janitor
  removes them. Those tools MUST treat a locked pool slot as in use, never as an abandoned checkout.
  _(Pool report, `INV-POOL-9`.)_
- **`INV-POOL-10`** <!-- uuid: e6244299-0409-4fe8-b524-6c35625fa3cd --> — Every repository operation MUST be bounded by a timeout, run hermetically, and be cancelled
  with a polite termination and never a forced kill, because a killed git process leaves a lock behind.
  A stale lock (zero bytes and older than the stale bound) MUST be removed only under the slot's
  exclusive hold and only while no git process has the slot as its working directory. _(Pool report,
  `INV-POOL-10` and "Failure modes and operations".)_
- **`INV-POOL-12`** <!-- uuid: 8272b0e1-08dc-4e78-bc2e-a2b4d9db0cc5 --> — The pool MUST be bounded by a configured maximum slot count and by the free-space floor.
  Creating a slot MUST be serialized (one at a time), rate limited, and subject to the existing
  free-space floor and low-disk classification. Steady-state leasing MUST create nothing. The maximum
  number of slots MUST NOT exceed the free space above the floor divided by the size of one checkout. A
  cold pool provisions its first slot on demand and MAY prewarm further slots one at a time with a delay
  between. _(Pool report, `INV-POOL-12`, "Pool sizing against disk" and "Cold start".)_
- **`INV-POOL-16`** <!-- uuid: dfbce3b4-7f8a-4a8f-9b34-84a1f08c03e9 --> — A slot MUST be quarantined (taken out of rotation, directory kept, alert raised) when: a
  reset fails twice, a non-empty or fresh lock exists, the working tree is not clean after reset, the
  watch cannot be made to answer after one stop and respawn, or the directory is not the checkout's
  top level. Quarantine MUST NOT delete anything. A quarantined slot MAY be replaced by a new slot
  (`INV-POOL-12`) and is removed only by the operator or by a bounded, rate-limited reaper after a retention
  period. _(Pool report, `INV-POOL-16`.)_
- **`INV-POOL-22`** <!-- uuid: 30074ad2-05e2-450d-bf6d-8a415184963a --> — The pool MUST emit metrics for slots by state, the age of the oldest lease, leaked slots,
  the time requesters wait, the time a reset takes, reset failures, quarantined slots, free bytes, the
  count of file-system watch daemons against the count of slots, and rescued work. It MUST have alerts
  for leaked slots persisting, quarantined slots persisting, every slot leased with requesters waiting,
  a reset slower than a threshold derived from the monorepo measurement, and watch daemons exceeding
  slots by more than one. Each alert rule MUST also be registered with the deployment's probe, as the
  deployment's alert convention requires. _(Pool report, "Leaks and alerts".)_

## The operations interface

- **`INTF-POOL-OPS`** <!-- uuid: 5abae38c-cfce-4a95-a41d-eeea3097a98f --> — what the operator reads: the status view (slots, states, ages, owners, free space) and the
  metrics and alerts named in `INV-POOL-22`. What crosses out: one gauge or counter per metric of `INV-POOL-22`
  and an alert per rule. What MUST hold: `INV-POOL-12`, `INV-POOL-16`, `INV-POOL-22`. The counterparty is
  `ACTOR-OPERATOR`.
