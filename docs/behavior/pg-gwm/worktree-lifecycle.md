# pg-gwm — the managed worktree's life

This doc owns what happens to a managed worktree between creation and removal: how it is created and
named, which branch and commit a lease puts it on, how a reclaimed worktree is reused and cleaned, what
reap does under a minimum and a maximum, how unsaved work is preserved, and how managed worktrees are
protected from other tools. The command and its record are in [`lease-interface.md`](lease-interface.md);
concurrency, crashes and the scheduled reaper are in [`failure-modes.md`](failure-modes.md).

## Stories

- **`STORY-GWM-POOL`** <!-- uuid: f7d81f0b-39f4-409a-8ae0-5bdea11ba3d7 --> — As the operator of a
  repository whose worktrees are expensive to create, I want released worktrees reused instead of
  deleted and recreated, so a lease costs a branch switch and not a full checkout. _(→
  `JOURNEY-GWM-REUSE`; `GOAL-GWM-CHEAP-REUSE`, `INV-GWM-18`, `INV-GWM-20`.)_
- **`STORY-GWM-STANDBY`** <!-- uuid: a888fced-b63b-488d-98ee-5cfb18cf8ce5 --> — As the operator, I
  want a minimum number of worktrees kept ready, even when reuse is off, so a requester rarely waits for
  a creation. _(→ `JOURNEY-GWM-REAP`; `INV-GWM-20`, `INV-GWM-23`.)_
- **`STORY-GWM-SAVE-WORK`** <!-- uuid: 29b6492c-167d-4ea2-8e03-098cd8d1acdd --> — As a requester
  whose lease expired with unsaved changes or with commits no branch holds, I want them committed to a
  branch I can find before the worktree is reclaimed, so expiring never costs work. _(→
  `JOURNEY-GWM-DIRTY-EXPIRED`; `GOAL-GWM-NEVER-LOSE-WORK`, `INV-GWM-24`.)_
- **`STORY-GWM-LEFT-ALONE`** <!-- uuid: ffdf4822-b137-4c4f-90d2-5b9eb79fa704 --> — As the operator, I
  want other worktree tools (git's own prune and remove, audit and cleanup tools, disk reclaimers) to
  recognize managed worktrees and leave them alone. _(→ `JOURNEY-GWM-REAP`; `GOAL-GWM-ONE-OWNER`,
  `INV-GWM-26`.)_
- **`STORY-GWM-BRANCH`** <!-- uuid: 3f8ac914-4251-466b-8048-4fafa4f56cb1 --> — As a requester, I want
  to name the branch and base I need, or get a sensible generated branch at the default base, and be
  told plainly when the branch is taken. _(→ `JOURNEY-GWM-LEASE-AND-RELEASE`; `INV-GWM-16`, `INV-GWM-17`,
  `INV-GWM-22`.)_

## Journeys

### `JOURNEY-GWM-REAP` — the reaper reclaims and tops up <!-- uuid: a728c252-e7bc-4f0d-8131-aceaa5d3a47a -->

**Primary actor:** `ACTOR-REAPER`. **Other actors:** `ACTOR-HOST`, `ACTOR-OPERATOR`. _Requires:_
`INV-GWM-20`, `INV-GWM-23`, `INV-GWM-24`, `INV-GWM-26`, `INV-GWM-27`, `INV-GWM-32`. _Includes:_
`JOURNEY-GWM-DIRTY-EXPIRED`.

1. The scheduled reaper runs `pg-gwm reap` for a repository with `min = 2`, three released worktrees
   and one expired one (`INV-GWM-32`, `INV-GWM-23`).
2. Each candidate is reclaimed in turn: its lease, if any, ends; unsaved work is preserved first
   (`INV-GWM-24`); a worktree beyond what `min` keeps is unlocked, its file-system monitor stopped, and
   removed, its branch kept; the rest are kept, freed of their branch, and become `available`
   (`INV-GWM-23`, `INV-GWM-26`, `INV-GWM-27`).
3. Two worktrees remain, both `available`; nothing needed creating (`INV-GWM-20`).

Extensions:

- 3a. Fewer than `min` remain (for example a fresh repository with `min = 2`). Reap creates standby
  worktrees, one at a time, until `min` is met or the free-space floor stops it (`INV-GWM-20`,
  `INV-GWM-15`).
- 2a. A candidate's preservation commit fails. It is marked `broken` and nothing about it is removed
  (`INV-GWM-24`, `INV-GWM-30`).
- 2b. `max` was lowered below the count. Reap also removes `available` worktrees until the count is at
  `max` (`INV-GWM-23`).

### `JOURNEY-GWM-REUSE` — a lease takes a reclaimed worktree <!-- uuid: 85c29cd8-f1bb-44ab-b311-1a671607c642 -->

**Primary actor:** `ACTOR-REQUESTER`. **Other actors:** `ACTOR-HOST`. _Requires:_ `INV-GWM-18`,
`INV-GWM-19`, `INV-GWM-20`, `INV-GWM-24`, `INV-GWM-25`. _Includes:_ none.

1. A repository has `reuse-reclaimed-worktrees = true`. A lease arrives.
2. The manager takes the first match of `INV-GWM-20`'s order and marks it `preparing` (`INV-GWM-20`).
3. If anything was written into the worktree since its lease ended, it is preserved first
   (`INV-GWM-24`). With `clean-reused-worktree = true`, the worktree is then cleaned to a pristine tree
   except the keep list (`INV-GWM-18`).
4. It is switched to the requested branch and commit, with the post-checkout hook run or skipped per
   the repository's policy (`INV-GWM-16`, `INV-GWM-25`), and handed out with its path unchanged.

Extensions:

- 2a. A `released` worktree already has the requested branch checked out. It is taken before every
  other candidate, with reuse on or off (`INV-GWM-20`, row 12).

### `JOURNEY-GWM-DIRTY-EXPIRED` — an expired lease left changes <!-- uuid: d96792b8-be6a-4461-a9fe-d5e9865bbd9a -->

**Primary actor:** `ACTOR-REAPER`. _Requires:_ `INV-GWM-24`. _Includes:_ none.

1. A lease named `potato` on branch `feature/x` expired with a changed file and a new untracked file.
2. Reap creates branch `pg-gwm/dirty-reaped-<timestamp>-feature-x-potato` at the worktree's current
   commit, adds every change (tracked and untracked, never ignored), and commits it with the message
   `adding dirty WIP` (`INV-GWM-24`).
3. `feature/x` is untouched. The worktree is now clean and is reclaimed as in `JOURNEY-GWM-REAP`.
4. Later the requester runs `pg-gwm preserved list`, finds the branch, runs
   `pg-gwm lease <that branch>`, and then `git reset --soft HEAD~1`: the commit is gone and its changes
   are staged, ready for the correct commit (the commit message says exactly this, `INV-GWM-24`).

## Branch and commit on lease

`lease` takes the same branch arguments as `git worktree add`, with the lease's name standing where git
uses the last component of the path: `pg-gwm lease [--name N] [-b B | -B B | --detach] [<commit-ish>]`.
`<commit-ish>` defaults to `HEAD` of the checkout the command runs in. The manager never fetches, as
`git worktree add` does not: everything resolves against local refs, remote-tracking ones included, so a
requester that needs a fresher base fetches first (ruled 2026-11-14).

| #   | Input                                                                                      | Behavior (as `git worktree add`, unless noted)                                              |
| --- | ------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------- |
| 1   | `<branch>`, a local branch not checked out anywhere                                        | that branch checked out                                                                     |
| 2   | `<branch>` with no local branch but exactly one remote-tracking `<remote>/<branch>`        | a local branch created tracking it                                                          |
| 3   | `<commit-ish>` that is not a branch name (a tag, a commit, `origin/main`)                  | detached at it                                                                              |
| 4   | `-b B [<commit-ish>]`, B does not exist                                                    | B created at the commit-ish                                                                 |
| 5   | `-b B ...`, B exists                                                                       | `usage` (B already exists; ruled 2026-11-14)                                                |
| 6   | `-B B [<commit-ish>]`                                                                      | B created, or reset to the commit-ish if it exists and no worktree holds it (git's `-B`)    |
| 7   | `--detach [<commit-ish>]`                                                                  | detached at the commit-ish                                                                  |
| 8   | nothing but `--name N`                                                                     | a branch named N: checked out if it exists and no worktree holds it, else created at `HEAD` |
| 9   | nothing, and no name                                                                       | `usage`, with a hint to pass `--name`, `-b` or `--detach` (ruled 2026-11-14)                |
| 10  | the branch is checked out in an unmanaged worktree or the main checkout                    | `branch-in-use`                                                                             |
| 11  | the branch is checked out in a managed worktree whose lease is preparing, valid or expired | `branch-in-use`, naming that lease; never taken over (ruled 2026-11-14)                     |
| 12  | the branch is checked out in a `released` worktree not yet reclaimed                       | that worktree is leased (same branch, warm files), with reuse on or off (pg-gwm only)       |
| 13  | a new branch (rows 4, 6, 8) under `pg-gwm/`                                                | `usage`; the namespace holds only preserved-work branches (pg-gwm only)                     |

## Invariants

- **`INV-GWM-15`** <!-- uuid: 8b7c2be7-25f8-4e09-975a-bfda5441395e --> — A new managed worktree MUST
  be created under the repository's `worktree-root`, in the directory `worktree-dir-template` names,
  one creation at a time per repository, and never when the free space would fall below
  `free-space-floor` (`low-disk`). The repository's post-checkout hook MUST run in full at creation.
  Every provisioner (the plain linked worktree, and a copy-on-write store when one exists) MUST pass one
  shared conformance suite for create, switch, clean, commit-to-branch, lock, remove and the dirty
  check, so the rest of the manager depends on no one store.
- **`INV-GWM-16`** <!-- uuid: d110b6ac-7b83-40c2-a35f-a5366486f454 --> — A lease MUST put its worktree
  on the branch and commit the table above gives, accepting the same branch arguments as
  `git worktree add` and behaving as it does for each, and MUST refuse rows 5, 10, 11 and 13 with the
  error named there, and row 9 with `usage`. Row 8 MUST fail with `usage` when the name is not a valid branch name. A lease
  MUST NOT move or take over a branch held by another worktree, whether that worktree's lease is
  preparing, valid or expired; `-B` MUST NOT reset a branch any worktree holds.
- **`INV-GWM-17`** <!-- uuid: c136f8b1-8005-498d-b3d1-9518953398c6 --> — A lease MUST NOT fetch. It
  MUST resolve its commit-ish against local refs, remote-tracking ones included, as `git worktree add`
  does, and MUST resolve it to a full commit under the repository's lock before any worktree is touched;
  one that does not resolve MUST fail with `ref-unresolvable` and change nothing. _(Operator ruling,
  Phillip, 2026-11-14: "i would expect lease to work similar to how git worktree add would do.")_
- **`INV-GWM-18`** <!-- uuid: 1b454ccc-403c-4436-845a-0cf327c3190c --> — With `clean-reused-worktree`
  on, a reused worktree MUST be cleaned when it is handed out, never at release: no changed tracked
  file, no untracked file, and no ignored file outside the repository's keep list. A worktree that is
  never reused is therefore never cleaned. With it off, a reused worktree keeps its ignored files and
  only switches branch and commit, and `config check` MUST warn that ignored files carry over to the
  next requester. A fresh worktree is not cleaned. _(Operator ruling, Phillip, 2026-11-14: "hand-out;
  not need to run it if never needed.")_
- **`INV-GWM-19`** <!-- uuid: fc273c9e-274c-4134-971e-18248a89d071 --> — A lease MUST NOT run a full
  reap. When it needs a worktree that only a reclaim can give (at `max`, or reuse on with nothing
  `available` or `released`), it MUST reclaim exactly one candidate, a `released` one before any
  `lease-expired` one and the oldest expiry first, under the same preservation rule as reap
  (`INV-GWM-24`), and MUST NOT top up standby worktrees. _(Operator ruling, Phillip, 2026-11-14: "the
  reap inside lease, it isn't doing a full reap.")_
- **`INV-GWM-20`** <!-- uuid: d5b6e2f2-903d-4616-8062-5940337e41a3 --> — A lease MUST take, in order: a
  `released` worktree that holds the requested branch (row 12); an `available` worktree; then, only
  with `reuse-reclaimed-worktrees` on, a `released` worktree, then a `lease-expired` one reclaimed per
  `INV-GWM-19`; and only then create a new one. Before handing out a `released` or `available`
  worktree it MUST check it for unsaved work and preserve any (`INV-GWM-24`). `min` MUST count
  `available` worktrees only: after a reap the repository MUST hold at least `min` of them. With reuse
  off, reap MUST remove every reclaimed worktree and create fresh standbys to reach `min`, so a standby
  is always one that was never used; with reuse on, reap MUST keep reclaimed worktrees as standbys up to
  `min` and remove the rest. _(Operator ruling, Phillip, 2026-11-14, on `min` counting ready worktrees:
  "yes".)_
- **`INV-GWM-21`** <!-- uuid: bbca662c-b90f-44eb-83fd-4cdd4e7e2110 --> — `max` MUST count every managed
  worktree in every status. A lease at `max` MUST reclaim one candidate per `INV-GWM-19` if one exists,
  with reuse on or off, and otherwise fail with `max-reached` (after any `--wait`). With reuse off, a
  lease at `max` reclaims by removing and then creates fresh, so `max` alone is an upper limit, not a
  pool. `config check` MUST reject `min > max` and warn when the current count exceeds `max`.
- **`INV-GWM-22`** <!-- uuid: 8fcdec47-2cf9-44b9-9d61-56457c652d31 --> — The manager MUST keep a slot
  index per managed worktree, and a new worktree MUST take the lowest index no managed worktree holds
  whose rendered directory does not already exist; an occupied directory MUST move it to the next index,
  never fail the lease. The default `worktree-dir-template` MUST include the repository's directory
  name, and `config check` MUST reject two configured repositories whose templates can render the same
  path under one `worktree-root`. `worktree-dir-template` and `standby-branch-template` MUST be given: the slot index, the repository name, the name and lease id
  (when a lease is being made), the owner, the metadata, `min`, `max`, and the count of managed
  worktrees including the one being created. A template whose output is not a valid directory or branch
  name MUST fail with `usage` and change nothing; `config check` MUST render each template once with
  sample values. A standby branch that already exists MUST be reused if no worktree holds it, and
  otherwise suffixed. (A template such as `slot-{{index | letter}}` yields `slot-a`, `slot-b`, ….)
- **`INV-GWM-23`** <!-- uuid: 313b9d8d-8cb5-4212-8d2d-581e2cdc9296 --> — `reap` MUST reclaim every
  `released` and `lease-expired` worktree of the repository, and MUST check every `available` one for
  unsaved work. For each reclaimed worktree it MUST first end any lease (status `reclaiming`), then
  preserve unsaved work (`INV-GWM-24`), then either unlock and remove it or keep it, detached or on a
  new branch from `standby-base` named by `standby-branch-template`, so its lease branch is free, and
  mark it `available`; which it keeps is governed by `min` and reuse (`INV-GWM-20`). It MUST also remove
  `available` worktrees while the count exceeds `max`. Reap MUST NOT delete any branch a lease used.
  `reap --dry-run` MUST report what it would do and change nothing.
- **`INV-GWM-24`** <!-- uuid: 403aa816-cafe-426f-8d03-b5147605f076 --> — Before any worktree is
  reclaimed, and before a `released` or `available` worktree is handed out, a worktree that is dirty, or
  whose head is a commit no branch or tag reaches, MUST have its work preserved: a new branch
  `pg-gwm/dirty-reaped-<UTC timestamp>-<branch>-<name or lease id>` is created at the worktree's
  current commit (`detached` stands in for an empty branch; each component is made safe for a branch
  name), every change except ignored files is added, untracked files included, and committed with
  signing off, the repository's commit hooks skipped, and a fallback author when git resolves none (for
  a clean tree with unreachable commits the branch alone suffices). The commit's subject MUST be
  `adding dirty WIP`, and its body MUST name the lease (name, lease id, branch), say that commit hooks
  were skipped, and give the commands that take the commit back off while keeping its changes staged
  so the correct commit can be made: `pg-gwm lease <this branch>`, then `git reset --soft HEAD~1`. The
  lease's own branch MUST NOT move. If the work cannot be preserved (a merge or rebase in progress,
  changes inside a submodule), the worktree MUST be marked `broken` and nothing about it removed,
  switched or handed out. _(Operator rulings, Phillip, 2026-11-14: hooks skipped, "yes; the comment
  should mention that", and "instruction on how to take the WIP commit and remove it but leave the WIP
  changes on the index".)_
- **`INV-GWM-25`** <!-- uuid: c1d962b8-756a-42d6-ae3a-9d12bf5623e4 --> — When a reused worktree is
  switched, the post-checkout hook MUST run unless the repository's `reuse-hook-policy` is `skip`; a skip
  MUST apply to that one command and MUST NEVER be written into the worktree's or repository's
  configuration. _(Carried from `INV-POOL-15`, Phillip, 2026-11-07.)_
- **`INV-GWM-26`** <!-- uuid: 3427b251-9242-4a5c-8906-0920ab2f7c94 --> — Every managed worktree MUST
  carry a git worktree lock whose reason is `pg-gwm <worktree id>`. The lock MUST be removed only by the
  manager, immediately before its own removal or prune of that worktree. Other tools in this product
  family (audit, cleanup, disk-reclaim, sweeps) MUST treat a worktree locked with a reason starting
  `pg-gwm` as in use and MUST NOT remove it. The lock does not restrict work inside the worktree.
- **`INV-GWM-27`** <!-- uuid: 82f5ef54-c248-4ae4-bea6-0e327661f540 --> — The manager MUST NOT write
  `core.fsmonitor`. Before removing a worktree it MUST stop that worktree's file-system monitor daemon,
  if one runs, so the removal does not feed a daemon about to disappear. _(Operator, 2026-10-05:
  fsmonitor stays on for the large monorepo.)_

## The host interface

- **`INTF-GWM-HOST`** <!-- uuid: 1ff7bfce-c1a9-4a37-b7f6-a8d42e0065a8 --> — the repository operations
  the manager relies on. What crosses out: create a worktree on a branch or detached at a commit,
  switch it, clean it except a keep list, commit its changes to a new branch, lock and unlock it,
  remove and prune it, stop a worktree's file-system monitor, read free space. What
  crosses in: each operation's outcome, the worktree's dirty state, head and reachability, free bytes.
  What MUST hold: `INV-GWM-15` to `INV-GWM-27`. The counterparty is `ACTOR-HOST`.
