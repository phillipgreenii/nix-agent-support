# Journeys — pg-router-ccpool-handler

Stories, use cases, and journeys, typed and leveled per the behavior-docs method's vocabulary
rules: **user-goal** and **subfunction** level elements are `USECASE-`; **summary**-level
multi-actor arcs stay `JOURNEY-`. Each element carries, on its own definition, what it requires and
what it includes (`INV-22`). None of the code below exists yet — see [README](README.md)'s
Realization gaps; these are the stories Task 5.2 onward realizes.

## Stories

- **`STORY-CCH-RUN`** <!-- uuid: dfd8305b-a52a-441c-a1d8-218337d7c81f --> — As pg-router's core, I
  want a registered handler participant to run an agent session on my behalf through `ccpool` (or
  a bare configured command), so pg-router itself never needs to know how to drive an agent.
  _(→ `USECASE-CCH-DISPATCH`; `INV-CCH-2`, `INV-CCH-3`, `INV-CCH-4`, `INV-CCH-5`,
  `INV-CCH-9`, `INV-CCH-10`, `INV-CCH-14`, `INV-CCH-15`, `INV-CCH-17`, `INV-CCH-18`, `INV-CCH-19`,
  `INV-CCH-20`, `INV-CCH-21`, `INV-CCH-22`, `INV-CCH-23`, `INV-CCH-24`, `INV-CCH-25`.)_
- **`STORY-CCH-QUERY`** <!-- uuid: 661a1b2c-4243-42b3-8fcc-607e8ec7e4af --> — As pg-router's core, I
  want a registered source to query beads for events on my behalf, so pg-router itself never needs
  to know beads' query language. _(→ `USECASE-CCH-QUERY`; `INV-CCH-1`, `INV-CCH-4`, `INV-CCH-5`.)_

## Use cases

### `USECASE-CCH-DISPATCH` — run a dispatched event as a handler session <!-- uuid: 3c471620-e5bc-4a7d-b81a-6100cc862f74 -->

**Primary actor:** `ACTOR-CCH-CORE`.
**Level:** user-goal.
**Preconditions:** this module is registered with a reachable core (`phillipgreenii-nix-agent-support`
ADR 0036 — this module never starts a core).
_Requires:_ `INTF-HANDLER`, `INV-CCH-2`, `INV-CCH-3`, `INV-CCH-14`, `INV-CCH-15`, `INV-CCH-17`, `INV-CCH-18`, `INV-CCH-19`, `INV-CCH-20`, `INV-CCH-21`, `INV-CCH-22`, `INV-CCH-23`, `INV-CCH-24`, `INV-CCH-25`.
_Includes:_ `INTF-CCH-CCPOOL` or a configured command, per the role's own backing kind.

1. The core dispatches one event under one tracking id to a bound role.
2. This module starts (or continues) a handler session: a ccpool-backed role drives `ccpool`; a
   command-backed role execs its configured argv.
   2d. A ccpool-backed role starts with the handler-wide tool grants plus its own extra grants, if
   it sets any, and no other role gets them (`INV-CCH-25`).
   2c. While the session runs, this module keeps a supervision lease on it fresh (`INV-CCH-18`),
   so a later dispatch can tell it from an orphan.
3. This module replies inline with a completion outcome, or defers with an ack and finishes the
   session on its own, later.
   3b. The session's turn has ended: this module closes it, non-purge (`INV-CCH-17`), once it is
   quiet and no one else has closed it, so the pool's slot is free again before `ccpool`'s idle
   timeout. How long it waits for quiet is
   the handler-wide window unless the role overrides it (`INV-CCH-23`).

Extensions:

- 2a. The role is already at capacity: this module declines `busy` before starting a session; the
  core re-offers per `INTF-HANDLER`'s pre-accept decline rule.
- 2b. The git origin the dispatch runs against is unavailable (`INV-CCH-10`): this module declines
  `busy` with reason `origin-unavailable` before touching any bead; dispatches into other origins
  are unaffected, and dispatch resumes on the first successful probe.
- 2h. A REVIEW dispatch whose bead is already closed, whose PR is merged, or whose pending review
  already holds content for the head (`INV-CCH-22`): this module declines `busy` before any
  capacity, isolation or launch step, with reason `skipped-bead-closed`, `skipped-pr-merged` or
  `skipped-pending-review`. It reads only, and any failure to read launches as normal.
- 2d. An earlier dispatch's handler died (it crashed or was killed; a daemon restart alone no
  longer kills it, `INV-CCH-14`) and left its session unsupervised: its lease has expired. Before checking
  capacity this module handles each such orphan of its own role (`INV-CCH-18`). An `idle` or
  `errored` one is closed (not purged), its bead claim released if the role's own actor still
  holds it, and its worktree removed; a `starting`, `ready` or `working` one past its time budget
  is hard-stopped as a supervised session would be (`INV-CCH-11`), then its worktree is removed;
  an under-budget one, a `needs_input` one, and one with no lease are left alone. The freed pool
  slot is then available to this very dispatch. If this dispatch is instead a redelivery that
  absorbs an orphan still within its budget, it takes over the lease and keeps the orphan's
  original launch time as the start of its budget.
- 2e. An earlier dispatch's handler died after it created the per-bead worktree but before the
  session existed (or a session was closed by `ccpool` before anyone reclaimed its worktree), so no
  session row leads to the worktree. Before checking capacity this module scans the worktree
  directory (`INV-CCH-19`) and removes each worktree, with its anchor branch, that is old enough,
  clean, holds no commit the canonical clone lacks, is not locked by git, is named by no open or
  live session row, is not the working directory of any live process (so a spared session of
  another role's pool is left alone), and is not held by a live dispatch. Anything else is kept and logged. Every
  dispatch of a worktree-isolation role holds a shared lock on its bead's worktree from before it
  creates the worktree until it returns, which is how a live dispatch is told from a dead one.
  The same pass removes lock files nothing has acquired for over seven days and nobody holds
  (`INV-CCH-24`), so the lock directory stays bounded.
- 2f. An earlier teardown of a session in a per-bead worktree was interrupted (a restart, a crash,
  a slow removal) after the session was closed but before its worktree was gone, so its pool
  record was kept (`INV-CCH-20`). Before checking capacity this module retries one such record: it
  removes the worktree and its anchor branch from the repository root (or, if the directory is
  already gone or git no longer knows it, deletes the husk and prunes), and then purges the
  record. If another live session still uses the worktree (`INV-CCH-15`), the record is purged and
  the worktree is left for that session. A failed retry leaves the record for the next dispatch.
  Until then the record is neither absorbed as a settled duplicate nor reclaimed as an orphan.
- 2g. A role with worktree isolation is configured to pre-fetch (`INV-CCH-21`), as a review of a
  pull request is: after the per-bead worktree exists and before the session launches, this module
  makes the item's commit local (fetching it only when it is not), checks the worktree out at it,
  and writes the change's file statistics and diff to files in the worktree. The prompt is told the
  commit and the file paths, so the session starts already on the commit with the diff on disk,
  and neither the time nor a pool slot is spent on it. If any step fails (an origin that needs a
  credential the handler lacks, a commit that cannot be found), the dispatch still proceeds and the
  prompt is told the pre-fetch did not succeed, so it does those steps itself.
- 3a. The session hits a post-accept failure (`retryable`, `resource-limit`, `critical`): this
  module surfaces it on its own logs/metrics or as a new event, never as anything but the opaque
  completion outcome the core already stores (`INV-CCH-3`).

### `USECASE-CCH-QUERY` — answer a source query from beads <!-- uuid: c73f0cd4-5c9b-4862-84da-1b80716ad937 -->

**Primary actor:** `ACTOR-CCH-CORE`.
**Level:** user-goal.
**Preconditions:** this module's beads-backed source is registered and `bd` is reachable.
_Requires:_ `INTF-SOURCE`.
_Includes:_ `INTF-CCH-BEADS`.

1. The core queries this module's beads-backed source, on its own query trigger.
2. This module runs its configured `bd` query and answers inline with events, or defers and
   delivers them later on the callback.

Extensions:

- 2a. The query itself fails (e.g. `bd` unreachable): reported as the source's own failure, not a
  core-side condition.

## Open questions

- **`OQ-CCH-ROLEMODEL`** <!-- uuid: 0ec48c8a-1d82-45b9-b94b-589056323ada --> — whether this
  module's internal role model keeps a `kind` field distinguishing ccpool-backed from
  command-backed roles (mirroring the enum pg-router's core drops per
  `phillipgreenii-nix-agent-support` ADR 0065's "Open question resolved" section), or dispatches by
  some other mechanism (e.g. one registered executor per configured role name). _Gap_: ADR 0065
  explicitly leaves this module's own internal layout to the implementer — "that distinction is
  now the handler's concern" — and no packet has settled it yet. _Owner_: whichever task folds the
  command executor into this module's own handler-dispatch entrypoint. _Path_: settle when that
  entrypoint is built (Task 5.3), as an internal implementation choice, not a behavior-docs
  decision. _Blocks_: nothing today — this module carries no code yet.
