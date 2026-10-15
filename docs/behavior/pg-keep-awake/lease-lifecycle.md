# pg-keep-awake — the lease

A lease is one reason to keep the Mac awake. A client or a caller creates one, the keeper reads them, and
the Mac is held awake only while a lease is live and the battery rule allows it. This doc owns what a
lease is, when it is live, how it is created and removed without losing or inventing one, how damaged
or hostile state is tolerated, and what the keeper does about a client that is stuck. The commands that
create and remove leases are in [`cli-contract.md`](cli-contract.md); the `caffeinate` shim that creates
them for callers is in [`caffeinate-shim.md`](caffeinate-shim.md). It traces to design sections 5 and 8
and to ADR 0100 decisions 2 and 5.

## Stories

- **`STORY-LEASE-NO-STRANDED`** <!-- uuid: 2a64c8f1-d0b7-4e93-85a2-f7c19e3b0d56 --> — As the operator, I want a reason to stay awake to end when whatever
  asked for it ends, whether it exits, is killed, becomes a zombie or runs past its time, so a crashed
  client never holds the Mac awake for ever. _(→ `JOURNEY-LEASE-CLIENT-DIES`; `INV-LEASE-1`,
  `INV-LEASE-2`, `INV-LEASE-3`, `INV-LEASE-4`, `INV-LEASE-5`.)_
- **`STORY-LEASE-SURVIVE-RESTART`** <!-- uuid: d91e0b73-5a2c-4f48-b6d9-0c8a4e7f1b32 --> — As the operator, I want a restart or a crash of the keeper to lose no valid
  lease and to drop no assertion it was holding, so applying a new version never lets the Mac sleep
  mid-task. _(→ `JOURNEY-LEASE-KEEPER-RESTART`; `GOAL-SURVIVE-RESTART`, `INV-LEASE-6`, `INV-LEASE-9`.)_
- **`STORY-LEASE-SAFE-STATE`** <!-- uuid: 63f7a2c9-e814-4b05-9d1e-a30b8c5d6f47 --> — As the operator, I want half-written, unreadable, oversized, hostile or
  newer-versioned lease files, concurrent writers and a stuck client to cost nothing worse than a
  skipped lease, so the keeper never wedges, loses a lease or runs anything a file says. _(→
  `JOURNEY-LEASE-CLIENT-DIES`; `INV-LEASE-6`, `INV-LEASE-7`, `INV-LEASE-8`, `INV-LEASE-10`.)_

## Journeys

### `JOURNEY-LEASE-CLIENT-DIES` — a client is killed while holding the Mac awake <!-- uuid: 87b0d4e6-3c1a-4f75-a9e2-5d6c7b18f309 -->

**Primary actor:** `ACTOR-CLIENT`. **Other actors:** `ACTOR-OPERATOR`, `ACTOR-HOST`. _Requires:_
`INV-LEASE-1`, `INV-LEASE-2`, `INV-LEASE-3`, `INV-LEASE-4`, `INV-LEASE-5`, `INV-LEASE-7`,
`INV-LEASE-8`, `INV-LEASE-10`. _Includes:_ none.

1. A session holds a lease bound to its own process (`INV-LEASE-1`, `INV-LEASE-2`).
2. The session is killed without warning. Its process is a zombie for a while because nothing has
   collected it; the lease is no longer live all the same (`INV-LEASE-3`, `INV-LEASE-5`).
3. The keeper's next pass reaps the lease. If no other lease is live, the assertion drops after the
   linger window (`INV-LEASE-4`).
4. While this happens a different client refreshes its own lease. Both operations complete; the
   refreshed lease survives the pass (`INV-LEASE-4`).

Extensions:

- 2a. The process id is reused by an unrelated program. The lease is still not live, because a lease is
  bound to the process that asked and not to its number (`INV-LEASE-3`).
- 2b. The lease was written by a `caffeinate` shim and its client is a stuck, still-running process whose
  lease has expired. The keeper MAY terminate it after checking it is still that shim (`INV-LEASE-10`).
- 3a. A file in the state is damaged, a link, a pipe or enormous. It is skipped; every other lease is
  still counted and nothing is deleted before its grace period (`INV-LEASE-7`, `INV-LEASE-8`).
- 3b. A client is stopped while holding the state's lock. The keeper gives up on that sweep after a short
  bound, still proves it is alive and sweeps on the next pass (`INV-LEASE-7`).

### `JOURNEY-LEASE-KEEPER-RESTART` — the keeper restarts while leases are held <!-- uuid: 4c2e9a05-f8d3-4b61-8e07-b19a6d0c3f74 -->

**Primary actor:** `ACTOR-OPERATOR`. **Other actors:** `ACTOR-HOST`, `ACTOR-CLIENT`. _Requires:_
`INV-LEASE-6`, `INV-LEASE-9`. _Includes:_ `JOURNEY-KEEP-RESTART-AND-APPLY`.

1. The operator applies a new version and the host's service manager stops the keeper (`INV-LEASE-9`).
2. On the way out, the keeper removes the leases that are expired or whose process is gone and leaves
   every valid lease in place (`INV-LEASE-9`).
3. The new keeper starts, reads the valid leases and the assertion it finds still held, and carries on.
   No client was asked to re-acquire (`INV-LEASE-6`).

Extensions:

- 1a. The keeper crashed and did not run its shutdown steps. The new keeper's start does the same
  cleaning (`INV-LEASE-9`).

## Invariants

- **`INV-LEASE-1`** <!-- uuid: b5d8f102-6e47-4a93-9c3b-e7a2d05f8c61 --> — A lease MUST be one reason to stay awake, identified by an owner and a key,
  and MUST carry the flags it asks for, the process it is bound to (if any), its expiry (if any) and a
  watched process (if any). One owner and key name one lease: asking again refreshes it. A lease
  MUST be bound to at least one of a process, an expiry or a watched process, so a lease can never be
  both unbounded and unowned. _(Design section 5.1.)_
- **`INV-LEASE-2`** <!-- uuid: 0e91c7a4-38b2-4d65-a8f0-2b4c9d6e1f83 --> — A client MUST only record and remove leases. It never starts or stops the
  assertion; a client that is killed or that disappears releases its lease with no further protocol.
  _(Design section 3 (R4); ADR 0100 decision 2.)_
- **`INV-LEASE-3`** <!-- uuid: f3a60d89-c1e5-4b27-93d4-7a8e0b5c2f16 --> — A lease MUST be live only while all of these hold: its process, if any, is
  running, is the same process that asked (not another that reused its number) and is not a zombie; its
  expiry, if any, has not passed by the wall clock; and its watched process, if any, is running and is the
  same process. _(Design section 5.1 and 5.5.)_
- **`INV-LEASE-4`** <!-- uuid: 7c14e2b8-a9d0-4f36-85b1-d6e3f90a4c27 --> — A lease that is not live MUST be reaped. The reaper MUST re-read the lease
  under the state's lock and remove it only if it is still dead, so a lease refreshed during a sweep
  survives, and a create racing a reap MUST lose no lease. _(Design section 5.4.)_
- **`INV-LEASE-5`** <!-- uuid: 29b7d0f6-4e83-4a1c-b2a9-5f1c8e73d0b4 --> — A client that was killed but not yet collected by its parent (a zombie)
  MUST NOT hold the assertion. _(Design section 5.5.)_
- **`INV-LEASE-6`** <!-- uuid: e84f1a3b-d607-4c92-8b5e-0a9c7d26f1e3 --> — A lease written by one version of the product MUST be read by every later
  version: unknown fields MUST be ignored, and a lease whose schema version is newer than the reader's
  MUST be skipped, shown by the status view as unreadable and never deleted. _(Design section 5.)_
- **`INV-LEASE-7`** <!-- uuid: 5b02e9c7-f1a4-4d38-96d0-c8e5a7b30d49 --> — No wait for the state's lock may be unbounded. A client that cannot take it
  within a short bound MUST treat the state as unwritable. The keeper that cannot take it MUST skip
  that pass's sweep and still prove it is alive, so one stopped client can wedge neither the keeper's
  loop nor its watchdog. _(Design section 5.4.)_
- **`INV-LEASE-8`** <!-- uuid: a1d6f3e0-8b95-4c27-b4e1-93c0d8a75f62 --> — A damaged file MUST be skipped and MUST NOT be deleted until it has aged
  past a grace period. A file that is not a regular file (a link, a pipe), an oversized file, and the files
  past a bounded number per pass MUST be skipped and counted as rejected. Half-written files MUST NOT be
  mistaken for leases, and ones that are stale MUST be removed by the sweeps. _(Design section 5.4.)_
- **`INV-LEASE-9`** <!-- uuid: 3f8a5c12-d4e7-4b60-a9c3-e1b72d0f8946 --> — When the keeper starts, and again when it is stopped gracefully, it MUST
  remove the leases that are expired or whose process or watched process is gone, and the partial files
  that are stale, and MUST leave every valid lease. _(Design section 8 (sweeps); R8.)_
- **`INV-LEASE-10`** <!-- uuid: c6e0b94d-27a1-4f53-8d8b-a5f3c1e07b29 --> — A lease whose client is still running but which is expired or orphaned MAY
  have its client terminated by the keeper, only for a lease written by the `caffeinate` shim or by `run`,
  and only after re-checking that the process is the same one and is still that tool. The keeper MUST NOT
  signal the process of an `acquire` lease (it may be pa-monitor itself), and MUST NOT execute or
  interpolate anything it reads from a lease. _(Design section 8 (backstop for stuck clients, spawn).)_
