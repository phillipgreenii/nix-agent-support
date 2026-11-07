# pg-checkout-pool — the lease and its interface

A lease is the temporary, exclusive right of one agent session to one slot. This doc owns who may lease,
what a lease is keyed by, how a lease is acquired, renewed, released and reaped, what a requester gets
back, what happens when no slot is free, and how a session in a pooled slot stays attributable. What a
slot is and how it is reset is in [`slot-reset.md`](slot-reset.md); crashes, damage and alerts are in
[`failure-modes.md`](failure-modes.md). It traces to the Pool report's proposed design (stories, lease
API and invariants) and to the operator ruling of 2026-11-07 described in the README.

## Stories

- **`STORY-POOL-LEASE-WARM`** <!-- uuid: 2cfaced1-7da7-472c-b721-2ff3428ea258 --> — As an agent session, I want a clean checkout of the repository at a given commit that
  is ready at once, and to give it back when I am done, so the next session gets the same slot already
  reset and warm. _(→ `JOURNEY-POOL-LEASE-AND-RELEASE`; `GOAL-POOL-CHEAP-LEASE`, `INV-POOL-2`, `INV-POOL-3`,
  `INV-POOL-14`, `INV-POOL-18`, `INV-POOL-20`.)_
- **`STORY-POOL-EXHAUSTED`** <!-- uuid: 87fe463b-b1fb-40b6-95f9-2b64c3effdde --> — As an agent session, when every slot is leased I want to wait a bounded time and
  then be told the pool is full, never be handed an unmanaged checkout, so the pool stays the admission
  control and the file-system churn never returns. _(→ `JOURNEY-POOL-EXHAUSTED`; `GOAL-POOL-BOUNDED`,
  `INV-POOL-13`.)_
- **`STORY-POOL-ATTRIBUTE`** <!-- uuid: b1c374b8-5bcc-4b76-aa7a-5182af2604a7 --> — As the operator, I want everything that is recorded about a session in a slot to be
  attributable to that session and its work item even though the slot's path is the same for every
  session, so transcripts and guards do not mix sessions up. _(→ `JOURNEY-POOL-LEASE-AND-RELEASE`;
  `INV-POOL-17`, `INV-POOL-20`.)_
- **`STORY-POOL-SCOPE`** <!-- uuid: 0b195229-8c58-4a09-8fea-565821211039 --> — As the operator, I want only automated agent sessions to lease in the first version,
  so an interactive session's checkout is never reset under a human. _(→ `JOURNEY-POOL-LEASE-AND-RELEASE`;
  `INV-POOL-21`.)_

## Journeys

### `JOURNEY-POOL-LEASE-AND-RELEASE` — a session leases a slot, works and releases it <!-- uuid: a15f2e38-e770-41ae-8155-b865743efda3 -->

**Primary actor:** `ACTOR-REQUESTER`. **Other actors:** `ACTOR-OPERATOR`, `ACTOR-HOST`. _Requires:_
`INV-POOL-2`, `INV-POOL-3`, `INV-POOL-5`, `INV-POOL-6`, `INV-POOL-14`, `INV-POOL-17`, `INV-POOL-18`, `INV-POOL-20`, `INV-POOL-21`.
_Includes:_ none.

1. An agent session names a repository, a commit reference, its own session id and, as a hint, its work
   item (`INV-POOL-20`, `INV-POOL-21`).
2. The pool resolves the reference to a full commit once, fetching into the shared object store under a
   pool-wide guard and not once per slot (`INV-POOL-14`).
3. The pool picks a free slot, preferring the one last used for that work item, takes the exclusive
   right to it and writes the lease record (`INV-POOL-2`, `INV-POOL-18`).
4. The slot is reset to the commit before it is handed out and the session receives the lease id, the
   slot path, the branch, the commit, the expiry and whether the slot's watch state is warm (`INV-POOL-5`).
5. The session's session id and the lease id are exported to its environment so what it records is
   attributable (`INV-POOL-17`).
6. The session renews the lease before it expires while it works (`INV-POOL-3`).
7. The session releases the lease. The slot is detached, reset and becomes free; the lease branch and
   its commits stay (`INV-POOL-6`).

Extensions:

- 3a. The session redispatched for the same work item finds its previous slot free and gets it back with
  its earlier commits and warm state (`INV-POOL-18`).
- 3b. The preferred slot is leased or no longer on its lease branch. Any other free slot is used and the
  hint is only recorded (`INV-POOL-18`, `INV-POOL-20`).
- 6a. The owner stops renewing but is still alive. The lease is not reaped while a live process is
  working in the slot; the operator is alerted (`INV-POOL-3`).
- 7a. The session releases with the keep disposition. The slot is left as it is, marked held for the
  operator and alerted (`INV-POOL-3`, `INV-POOL-22`).

### `JOURNEY-POOL-EXHAUSTED` — every slot is leased <!-- uuid: c0881801-d09d-4c0b-bc5d-9fad5cf83513 -->

**Primary actor:** `ACTOR-REQUESTER`. **Other actors:** `ACTOR-OPERATOR`. _Requires:_ `INV-POOL-3`,
`INV-POOL-12`, `INV-POOL-13`. _Includes:_ none.

1. A session asks for a slot while the pool is exhausted (`INV-POOL-13`).
2. The pool waits up to the requested bound for a lease to be released or to expire (`INV-POOL-3`, `INV-POOL-13`).
3. Nothing becomes free. The pool declines with the `pool-exhausted` reason, mapped by the caller to
   its existing re-offer with back-off path (`INV-POOL-13`).

Extensions:

- 2a. A slot frees within the bound. It is leased as in `JOURNEY-POOL-LEASE-AND-RELEASE`.
- 3a. The pool is below its maximum size. It grows by one slot instead of declining, within the disk
  floor (`INV-POOL-12`).

## Invariants

- **`INV-POOL-2`** <!-- uuid: a883a6d7-c1fe-4260-b4bb-d4037f0fb11f --> — A slot MUST be leased to at most one owner at a time. Exclusivity MUST be enforced by an
  exclusive hold on the slot that the owner keeps for the whole lease and that the operating system
  releases when the owner dies, plus a lease record. The lease record alone MUST NOT be trusted for
  liveness. _(Pool report, `INV-POOL-2`.)_
- **`INV-POOL-3`** <!-- uuid: 1919ad26-58cd-468e-9fd1-38f884226f32 --> — A lease MUST have an expiry. An owner that is alive MUST renew it before it expires. A
  lease whose exclusive hold is free and whose expiry has passed is a leak. A lease whose owner has
  stopped renewing but still has a live process working in the slot MUST NOT be reaped; it MUST raise an
  alert and be left to the operator or the stall logic. _(Pool report, `INV-POOL-3` and crash recovery.)_
- **`INV-POOL-13`** <!-- uuid: 16c1bcff-86d6-4dd8-b2b3-3b19fc45462c --> — An exhausted pool MUST decline with the reason `pool-exhausted` after a bounded wait. It
  MUST NOT fall back to an unpooled checkout, because that reopens the churn the pool exists to remove. A
  repository with pooling disabled keeps its existing path. _(Pool report, `INV-POOL-13`.)_
- **`INV-POOL-14`** <!-- uuid: 2fe14e1f-4212-44ab-8216-0c646f266775 --> — The pool MUST fetch into the shared object store once, under a pool-wide guard, not once
  per slot, and MUST resolve the requested reference to a full commit before resetting any slot. A
  requester MUST name a repository and a reference; what the slot is reset to is the resolved commit, not
  a moving reference. _(Pool report, `INV-POOL-14` and `INV-POOL-4`.)_
- **`INV-POOL-17`** <!-- uuid: 54b92c75-5523-4e2f-96b7-684a5b00e785 --> — Slot paths are stable across work items, so anything keyed by working directory (a
  guard that looks for live processes, the transcript project directory, the recorder's attribution) MUST
  also key by the lease id. The lease id MUST be exported to the session so a transcript can be
  attributed to its work item. _(Pool report, `INV-POOL-17`.)_
- **`INV-POOL-18`** <!-- uuid: 21246c52-3406-4435-b06f-1d66a08fc254 --> — A requester that held a slot before (a redispatch of the same work item) SHOULD get the
  same slot back when it is free and its branch is still the lease branch, so a resumed session sees its
  prior commits and warm state. This preference is driven only by the affinity hint; it MUST NOT make any
  slot unavailable to another owner. _(Pool report, `INV-POOL-18`; ruling of 2026-11-07.)_
- **`INV-POOL-20`** <!-- uuid: e39b0459-60cc-44da-bc18-8aab4ebb40a0 --> — **SETTLED.** The lease key MUST be the owner's session id: the lease's owner MUST be the
  same external session id the router handler's session record carries, so orphan reconciliation can
  join a session record to its lease. The work item id MUST be recorded in the lease record as an
  affinity hint only (to prefer the slot last used for that work item) and MUST NOT be the key; two
  concurrent sessions on one work item therefore hold two different slots. _(Operator ruling, Phillip,
  2026-11-07: "i agree with your recommendations", accepting the agent recommendation of 2026-11-06.)_
- **`INV-POOL-21`** <!-- uuid: 7fad615a-33d0-4937-8b6e-8ee8fd2568e2 --> — **SETTLED.** In the first version only agent sessions MUST lease from the pool;
  interactive (human) sessions MUST NOT lease and keep their own checkouts. _(Operator ruling, Phillip,
  2026-11-07: "i agree with your recommendations", accepting the agent recommendation of 2026-11-06.)_

## The lease interface

- **`INTF-POOL-LEASE`** <!-- uuid: 1dc2f641-33fe-4a45-ac1b-5cca101b4835 --> — the interface a requester and an operator use, as an in-process library and as a command
  line; no resident daemon is required, because all coordination is the exclusive hold plus stored
  records. What crosses in: `lease` (repository, commit reference, owner session id, work item hint,
  optional branch name, time to live, bound to wait, affinity), `renew` (lease id, time to live),
  `release` (lease id, disposition `reset`, `keep` or `quarantine`; default `reset`), `status` and `reap`
  (with a dry-run mode). What crosses out: for a lease, the lease id, slot, path, branch, resolved
  commit, expiry, the milliseconds the reset took and whether the slot is warm or cold, as one JSON
  document; for a refusal, a distinct exit code of 2 or more for each of `pool-exhausted`, `low-disk`,
  `ref-unresolvable`, `quarantine-only` and `git-wedged`. The lease record holds the lease id, owner
  session id, owner process and its start time, work item hint, repository, commit, branch, acquired,
  heartbeat and expiry times, and the slot state (`warm-free`, `leased`, `resetting`, `quarantined` or
  `retired`); it is written atomically and is never the source of liveness (`INV-POOL-2`). What MUST hold:
  `INV-POOL-2`, `INV-POOL-3`, `INV-POOL-13`, `INV-POOL-14`, `INV-POOL-17`, `INV-POOL-18`, `INV-POOL-20`, `INV-POOL-21`. The counterparty is
  `ACTOR-REQUESTER`.
