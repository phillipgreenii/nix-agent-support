# pg-checkout-pool — the slot and its reset

A slot is a checkout that lives for the whole of its life. This doc owns what a slot is, how it is
brought to a requested commit without being deleted, which ignored files survive, when the repository's
post-checkout hook runs, how a dead owner's uncommitted work is preserved, why the file-system watch of a
monorepo is never switched off, and why the pool assumes nothing about what a slot is made of. Who may
lease a slot is in [`lease-api.md`](lease-api.md); what happens when a reset or a slot goes wrong is in
[`failure-modes.md`](failure-modes.md). It traces to the Pool report's measured reset against create and
proposed design, to the Clone report's mitigations, and to the operator ruling of 2026-11-07 described in
the README.

## Stories

- **`STORY-POOL-RESET-IN-PLACE`** <!-- uuid: cc34464c-1061-4ce3-803b-fa0e6a079147 --> — As the operator, I want a released slot reset in place to the next requested commit,
  so a lease costs a reset sized by what the last session changed and the file-system event service sees
  thousands of events instead of hundreds of thousands. _(→ `JOURNEY-POOL-RESET`; `GOAL-POOL-CHEAP-LEASE`,
  `INV-POOL-1`, `INV-POOL-5`, `INV-POOL-6`, `INV-POOL-11`.)_
- **`STORY-POOL-KEEP-WARM-STATE`** <!-- uuid: f0e2b7fa-b33b-4334-bbe8-97d3b9a16187 --> — As an agent session in the monorepo, I want the developer-environment state the
  repository needs to survive a reset while every other ignored file is cleaned, so my tools start warm
  and no stale build output leaks from the previous session. _(→ `JOURNEY-POOL-RESET`; `INV-POOL-4`,
  `INV-POOL-15`.)_
- **`STORY-POOL-RESCUE`** <!-- uuid: 6c8aa612-d8c4-4707-a8e6-8933f43f7e15 --> — As the operator, I want the uncommitted work of a session that died to be saved to a ref
  before its slot is reset, and the slot left alone if that save fails, so a crash never costs work.
  _(→ `JOURNEY-POOL-DEAD-OWNER`; `GOAL-POOL-NEVER-LOSE-WORK`, `INV-POOL-6`, `INV-POOL-7`.)_
- **`STORY-POOL-SWAPPABLE-STORE`** <!-- uuid: dcb1d792-1e18-46bc-aa1b-49d02fcfaf02 --> — As the operator, I want a different backing store, such as a copy-on-write clone,
  to be usable later without changing how leases and resets are specified, so the pool outlives the
  first implementation. _(→ `JOURNEY-POOL-RESET`; `INV-POOL-19`.)_

## Journeys

### `JOURNEY-POOL-RESET` — a free slot is reset for the next lease <!-- uuid: 8d203d19-5845-445a-8c15-44099489af8c -->

**Primary actor:** `ACTOR-REQUESTER`. **Other actors:** `ACTOR-HOST`, `ACTOR-OPERATOR`. _Requires:_
`INV-POOL-1`, `INV-POOL-4`, `INV-POOL-5`, `INV-POOL-6`, `INV-POOL-11`, `INV-POOL-15`, `INV-POOL-19`. _Includes:_
`JOURNEY-POOL-LEASE-AND-RELEASE`.

1. The previous owner released the slot. The slot detached from its lease branch, which and its
   commits stay (`INV-POOL-6`).
2. A new lease request resolves to a commit. Before the slot is handed out, it is reset to that commit
   on a lease-specific branch, with no tracked modification, no untracked file and no ignored file
   outside the repository's keep list (`INV-POOL-4`, `INV-POOL-5`).
3. The repository's post-checkout hook is skipped for this reset only if the repository's policy says
   so and a measurement showed the tree clean and the keep list intact (`INV-POOL-15`).
4. The slot's file-system watch is still enabled and still answering, so the first status after the
   reset is cheap (`INV-POOL-11`).
5. The slot is handed out. Nothing about the slot's directory was deleted (`INV-POOL-1`).

Extensions:

- 2a. The slot was created just now. The full hook runs once, at creation (`INV-POOL-15`).
- 3a. The repository's policy says its hook output depends on the lease. The hook runs on the reset too
  (`INV-POOL-15`).
- 4a. The watch does not answer. It is stopped and respawned once, never disabled; if it still does not
  answer the slot is quarantined (`INV-POOL-11`; `INV-POOL-16` in [`failure-modes.md`](failure-modes.md)).
- 5a. The slot is backed by something other than a linked checkout. The same sequence applies through the
  provisioner (`INV-POOL-19`).

### `JOURNEY-POOL-DEAD-OWNER` — a session died holding uncommitted work <!-- uuid: 585b9574-82c1-489b-8f2d-94850f06c1c2 -->

**Primary actor:** `ACTOR-MONITOR`. **Other actors:** `ACTOR-OPERATOR`, `ACTOR-HOST`. _Requires:_
`INV-POOL-6`, `INV-POOL-7`. _Includes:_ none.

1. The monitor finds a lease whose owner is gone past its grace window (`INV-POOL-7`).
2. Uncommitted tracked changes are first captured to a rescue ref (`INV-POOL-7`).
3. The slot is reset and freed; commits made on the lease branch are untouched (`INV-POOL-6`, `INV-POOL-7`).

Extensions:

- 2a. The capture fails. The slot is quarantined and not reset (`INV-POOL-7`).
- 1a. The owner is not proven dead. Nothing is touched (`INV-POOL-7`).

## Invariants

- **`INV-POOL-1`** <!-- uuid: 5336e19f-a79a-4ed1-913f-30694373fcfa --> — A pooled checkout MUST be a linked checkout (or other provisioner product) that exists for
  the whole life of the slot. Releasing a lease MUST NOT delete the directory; it MUST reset it. _(Pool
  report, `INV-POOL-1`.)_
- **`INV-POOL-4`** <!-- uuid: 2b9f2105-61f9-477e-9d1e-25acc6413dac --> — **SETTLED (keep list).** A slot handed out MUST be at the requested full commit, on a
  lease-specific branch (by default named for the owner), with a clean tree: no tracked modification, no
  untracked file, and no ignored file outside the repository's keep list. The reset MUST clean every
  ignored file except the keep list (the equivalent of a forced clean of directories and ignored files).
  The keep list MUST be configured per repository. For the monorepo the default keep list MUST be exactly
  two paths, `.flox` and `.envrc.local`; a measurement of the monorepo MAY extend it for that repository,
  and nothing else extends it. _(Operator ruling, Phillip, 2026-11-07: "i agree with your
  recommendations", accepting the agent recommendation of 2026-11-06; Pool report, `INV-POOL-4`.)_
- **`INV-POOL-5`** <!-- uuid: ebe63109-48b0-4bdc-a29e-67a04e2e36c0 --> — A slot MUST be reset BEFORE it is handed out (verified on acquire), not only after release,
  so a release that crashed before its reset cannot leak state to the next lease. _(Pool report,
  `INV-POOL-5`.)_
- **`INV-POOL-6`** <!-- uuid: 9d68ab4d-10d6-4039-9fa8-2dac7b6b520d --> — On release the manager MUST detach the slot from its lease branch (so no idle slot holds a
  branch checked out) and MUST NOT delete the lease branch. Commits made in a lease live on a ref and
  survive the reset. _(Pool report, `INV-POOL-6`.)_
- **`INV-POOL-7`** <!-- uuid: 0fc1fa9a-87c1-4dfc-a589-51cc6a95eeeb --> — The manager MUST NOT destroy the uncommitted edits of an owner that is not proven dead. For a
  dead owner past its grace window, uncommitted tracked changes MUST first be captured to a rescue ref,
  and then the slot reset. If the capture fails the slot MUST be quarantined, not reset. _(Pool report,
  `INV-POOL-7`.)_
- **`INV-POOL-11`** <!-- uuid: 7b47d3bb-7308-45b9-9334-4ddb66863e08 --> — The file-system watch (`core.fsmonitor`) MUST remain enabled in a pooled monorepo slot. The
  manager MUST NOT write that setting. Recovery of a watch that does not answer MUST be stop and respawn
  (removing that slot's stale socket), never disabling the feature. _(Operator ruling, 2026-10-05; Pool
  report, `INV-POOL-11`.)_
- **`INV-POOL-15`** <!-- uuid: 6a9d560e-649a-4491-89c2-dcbf5b92b7eb --> — **SETTLED (hook policy).** The post-checkout hook policy MUST be per repository. For the
  monorepo the full hook MUST run once at slot creation, and MUST be skipped on a reset in place: the
  hook path setting is unset for that one command only and MUST NEVER be persisted in the slot's
  configuration. The default for the monorepo MUST remain skip only if a measurement shows the tree clean
  and the keep list intact after a skipped-hook reset; a repository whose policy says its hook output is
  lease-dependent MUST run the hook on reset. The policy MUST be measured before it is made a default.
  _(Operator ruling, Phillip, 2026-11-07: "i agree with your recommendations", accepting the agent
  recommendation of 2026-11-06; Pool report, `INV-POOL-15`.)_
- **`INV-POOL-19`** <!-- uuid: 8f917b11-269b-4254-ad7f-7b33bd9a2183 --> — The provisioner interface MUST NOT assume the backing store is a linked checkout. A slot is a
  path, a repository and a store kind; creation, reset and destroy are the provisioner's operations. A
  copy-on-write clone store MAY implement the same interface. _(Pool report, `INV-POOL-19`.)_

## The host interface

- **`INTF-POOL-HOST`** <!-- uuid: 775f0f4b-c640-4a8e-807b-0148acd825e7 --> — the repository operations, disk readings and daemon state the pool relies on. What
  crosses out of the pool: create a slot, reset it to a commit, detach it, capture a rescue ref, query
  and stop and respawn the slot's file-system watch, read free space. What crosses in: the outcome of each
  operation, the slot's watch answer, free bytes and a low-disk or wedged classification. What MUST hold:
  `INV-POOL-1`, `INV-POOL-4`, `INV-POOL-5`, `INV-POOL-7`, `INV-POOL-11`, `INV-POOL-15`, `INV-POOL-19`. The counterparty is
  `ACTOR-HOST`.
