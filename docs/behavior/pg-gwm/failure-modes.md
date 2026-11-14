# pg-gwm — state, concurrency, failures and operations

This doc owns where the manager keeps its state, how concurrent commands stay consistent, what happens
when an operation dies part-way or a worktree is damaged, how git is bounded, the scheduled reaper, and
the metrics and event log. The command is in [`lease-interface.md`](lease-interface.md); the worktree's
life is in [`worktree-lifecycle.md`](worktree-lifecycle.md).

## Stories

- **`STORY-GWM-CONCURRENT`** <!-- uuid: 2727d3bb-d400-4a54-b019-9b05e4c78d51 --> — As several
  requesters leasing, renewing and releasing in one repository at once, I want every command to see
  a consistent state and no two of us to get the same worktree or name. _(→ `JOURNEY-GWM-RACE`;
  `INV-GWM-28`, `INV-GWM-29`.)_
- **`STORY-GWM-SELF-HEAL`** <!-- uuid: 6ca6ec42-d9c2-449d-86e6-a630a57640dc --> — As the operator, I
  want an operation killed part-way, or a worktree someone deleted by hand, to be found and handled by
  the next reap without my deleting anything. _(→ `JOURNEY-GWM-BROKEN`; `INV-GWM-30`, `INV-GWM-31`.)_
- **`STORY-GWM-UNATTENDED`** <!-- uuid: e0cb4633-047b-4f0e-b3ba-e6d91e8c3dc2 --> — As the operator, I
  want expired and released worktrees reclaimed, and standby worktrees topped up, on a schedule, so
  idle repositories do not accumulate them. _(→ `JOURNEY-GWM-REAP`; `INV-GWM-32`.)_
- **`STORY-GWM-OBSERVE`** <!-- uuid: 30157244-bc0b-4c59-9ec1-66e7255998f8 --> — As the operator, I
  want metrics and an event log that show leases, reclaims, preserved work, worktrees held without an
  expiry, and broken worktrees, so I learn of a problem before a requester does. _(→
  `JOURNEY-GWM-BROKEN`; `INV-GWM-33`, `INV-GWM-34`.)_

## Journeys

### `JOURNEY-GWM-RACE` — two requesters lease at once <!-- uuid: 655c8cd8-bbcd-4271-8f73-9bc1fc2e36f5 -->

**Primary actor:** `ACTOR-REQUESTER`. _Requires:_ `INV-GWM-5`, `INV-GWM-28`, `INV-GWM-29`. _Includes:_ none.

1. Two `pg-gwm lease --name potato` commands start together in one repository.
2. Each takes the repository's lock in turn (`INV-GWM-29`). The first records the lease, status
   `preparing`, which holds the name; the second sees `potato` held and fails with `name-held`
   (`INV-GWM-5`).
3. The first's slow git work (a creation or a switch) runs without holding the repository's
   lock, so a third requester's `renew` is not delayed by it (`INV-GWM-29`).

Extensions:

- 2a. The lock is not taken within `lock-wait`. The command fails with `busy` and changes nothing.
- 3a. A `renew` arrives for a lease whose worktree reap has marked `reclaiming`. It fails with
  `lease-lost` (`INV-GWM-29`, `INV-GWM-7`).

### `JOURNEY-GWM-BROKEN` — an operation died part-way <!-- uuid: 634e5a9a-5ed7-49f4-bcf6-dbacda55fd6b -->

**Primary actor:** `ACTOR-REAPER`. **Other actors:** `ACTOR-OPERATOR`, `ACTOR-HOST`. _Requires:_
`INV-GWM-30`, `INV-GWM-31`, `INV-GWM-33`. _Includes:_ none.

1. A lease was killed while creating its worktree; the worktree is `preparing` and no command is
   working on it (`INV-GWM-30`).
2. Reap marks it `broken`, retries the creation's cleanup once, and on success removes it; on failure
   it stays `broken` with its directory kept, and the broken-worktree metric rises (`INV-GWM-30`,
   `INV-GWM-33`).

Extensions:

- 1a. A person deleted a managed worktree's directory. Reap finds the registration without a
  directory, unlocks and prunes the registration, and drops the record; its branch stays (`INV-GWM-30`).
- 1b. The volume holding `worktree-root` is not mounted. Reap touches nothing in that repository and
  warns (`INV-GWM-30`).
- 2a. The worktree holds unsaved work. It is preserved first (`INV-GWM-24`).

## Invariants

- **`INV-GWM-28`** <!-- uuid: 4f5f5f42-9a50-41fc-9071-b86d8358fcd6 --> — The manager's state for a
  repository MUST live under the repository's common git directory (`<git-common-dir>/pg-gwm/`), so the
  main checkout and every worktree find the same state with no configuration. A test MUST prove that a
  command run from the main checkout, from a managed worktree and from an unmanaged worktree reads and
  writes the same state, and that the state survives `git gc` and `git worktree prune`; if git or
  another tool proves that location unsafe, the location MAY move, and this invariant with it.
  _(Operator ruling, Phillip, 2026-11-14.)_
- **`INV-GWM-29`** <!-- uuid: 40dfbc2c-78ca-4a98-a2a2-3e65aa3bb19c --> — Every change to a
  repository's state MUST happen under one exclusive per-repository lock, taken with a bounded wait
  (`lock-wait`, default 10 seconds; `busy` on timeout). Slow repository operations (creating,
  switching, cleaning, preserving, removing) MUST NOT run while that lock is held: the worktree is first
  marked `preparing`, `creating` or `reclaiming` under the lock, the operation runs outside it, and its
  outcome is recorded under the lock again. A worktree so marked MUST NOT be chosen by any other lease
  or reap. `info` and `list` MUST report it as it is. A `renew` or `release` of a lease whose worktree is
  `reclaiming` MUST fail with `lease-lost`; of a lease that is `preparing`, `renew` MUST succeed and
  `release` MUST fail with `busy`.
- **`INV-GWM-30`** <!-- uuid: a1ee5da0-76bd-4058-8557-4ac6a2e1c8a3 --> — A worktree left `preparing`,
  `creating` or `reclaiming` with no command still working on it, or whose directory is missing while
  git still registers it, MUST be marked `broken`. "No command still working on it" MUST be decided by a
  hold that the working command keeps on that worktree for the operation's duration and that the
  operating system drops when the command dies; this is the manager's own bookkeeping and never a
  lease's liveness (`INV-GWM-11`). Reap MUST retry a `broken` worktree once (preserving unsaved work
  first) and remove it on success, or leave it `broken` with its directory intact. A missing directory
  MUST be handled by unlocking, pruning its registration and dropping its record, never by deleting a
  branch. When `worktree-root` itself is missing, reap MUST change nothing in that repository and warn.
- **`INV-GWM-31`** <!-- uuid: 890a6355-fead-4a95-9821-dbaf85814f3b --> — Every repository operation
  MUST run under a timeout (`git-timeout`, default 2 minutes; `create-timeout`, default 30 minutes, for
  a creation or a clean), in an environment that ignores any inherited variable redirecting git to
  another repository, and MUST be cancelled with SIGTERM and never SIGKILL, because a killed git leaves
  a lock behind. A zero-byte git index lock older than `index-lock-stale-after` (default 10 minutes)
  MAY be removed only while the manager holds that worktree.
- **`INV-GWM-32`** <!-- uuid: 715fb890-c503-4988-9e55-0a239bbdfa41 --> — A scheduled job MUST run
  `pg-gwm reap --all-repos` every `reap-interval` (default 10 minutes), over every repository in the
  user configuration and every repository in which a lease has been made (the manager MUST record each
  such repository in a user-level registry), single-flight per repository and bounded by a deadline,
  and MUST record each run. The lease's own one-candidate reclaim (`INV-GWM-19`) complements it and MUST
  NOT replace it. _(Operator ruling, Phillip, 2026-11-14: "yes".)_
- **`INV-GWM-33`** <!-- uuid: 9d1cadda-27a6-4acc-9c9e-76a1d62a15c9 --> — The manager MUST emit, per
  repository: managed worktrees by status; the age of the oldest `lease-expired` lease; the age of the
  oldest lease with no expiry; worktrees removed and kept by reap; preserved branches; `broken`
  worktrees; refusals by error name; the time a lease takes (split into wait, creation and switch); and
  the age of the last reaper run. It MUST have alerts for `broken` worktrees persisting, a reaper run
  older than three `reap-interval`s, and `max-reached` refusals above a configured rate. Each alert rule
  MUST be registered with the deployment's probe, as the deployment's alert convention requires.
- **`INV-GWM-34`** <!-- uuid: a867aa28-7d57-4838-a7a2-e840e583e823 --> — Every lease, renew, release,
  reclaim, preservation, creation, removal and `broken` transition MUST be recorded in an append-only
  event log under the repository's state, naming the worktree id, lease id, name, owner and outcome.
  The log MUST be rotated to a bounded size, keeping at least `tombstone-retention` of history.

## The reaper and operations interfaces

- **`INTF-GWM-REAP`** <!-- uuid: bfa8dacd-cf57-42d4-b17e-99e82a9d9879 --> — the scheduled pass. What
  crosses in: the registry of repositories and the deadline. What crosses out: per repository, the
  worktrees removed, kept, preserved and marked `broken`, and the run record. What MUST hold:
  `INV-GWM-23`, `INV-GWM-24`, `INV-GWM-30`, `INV-GWM-32`. The counterparty is `ACTOR-REAPER`.
- **`INTF-GWM-OPS`** <!-- uuid: 66e0b90f-5c51-4526-8896-90703917cd55 --> — what the operator reads:
  `list`, `preserved list`, the event log, and the metrics and alerts of `INV-GWM-33`. What MUST
  hold: `INV-GWM-33`, `INV-GWM-34`. The counterparty is `ACTOR-OPERATOR`.
