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
  `INV-CCH-20`, `INV-CCH-21`, `INV-CCH-22`, `INV-CCH-23`, `INV-CCH-24`, `INV-CCH-25`, `INV-CCH-26`, `INV-CCH-27`, `INV-CCH-28`.)_
- **`STORY-CCH-QUERY`** <!-- uuid: 661a1b2c-4243-42b3-8fcc-607e8ec7e4af --> — As pg-router's core, I
  want a registered source to query beads for events on my behalf, so pg-router itself never needs
  to know beads' query language. _(→ `USECASE-CCH-QUERY`; `INV-CCH-1`, `INV-CCH-4`, `INV-CCH-5`.)_

## Use cases

### `USECASE-CCH-DISPATCH` — run a dispatched event as a handler session <!-- uuid: 3c471620-e5bc-4a7d-b81a-6100cc862f74 -->

**Primary actor:** `ACTOR-CCH-CORE`.
**Level:** user-goal.
**Preconditions:** this module is registered with a reachable core (`phillipgreenii-nix-agent-support`
ADR 0036 — this module never starts a core).
_Requires:_ `INTF-HANDLER`, `INV-CCH-2`, `INV-CCH-3`, `INV-CCH-14`, `INV-CCH-15`, `INV-CCH-17`, `INV-CCH-18`, `INV-CCH-19`, `INV-CCH-20`, `INV-CCH-21`, `INV-CCH-22`, `INV-CCH-23`, `INV-CCH-24`, `INV-CCH-25`, `INV-CCH-26`, `INV-CCH-27`, `INV-CCH-28`.
_Includes:_ `INTF-CCH-CCPOOL` or a configured command, per the role's own backing kind; `USECASE-CCH-RELEASE-END` for a role whose completion is `close-or-release`.

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
- 2h. A dispatch whose role opts in to the pre-launch check and whose check says the dispatch is
  not worth running (`INV-CCH-22`): this module declines `busy` before any capacity, isolation or
  launch step, with a reason that starts `skipped-`. For a REVIEW role that is a bead already
  closed, a PR already merged, or a pending review that already holds content for the head
  (`skipped-bead-closed`, `skipped-pr-merged`, `skipped-pending-review`). For a role that works a
  bead it claims itself (`ready`) it is a bead that is no longer open, is held by another actor,
  is now marked for a human, is no longer groomed, is deferred or is blocked
  (`skipped-bead-not-open`, `skipped-bead-claimed`, `skipped-bead-human`,
  `skipped-bead-not-groomed`, `skipped-bead-deferred`, `skipped-bead-blocked`); a bead that the
  role's own actor already holds `in_progress` is never declined, because it is the live session a
  restarted daemon is about to absorb. It reads only, and any failure to read launches as
  normal.
- 2i. A session dies unexplained before its bead completes (`INV-CCH-7`): this module also logs
  the pool's usage-limit reading once (`INV-CCH-26`), as evidence only; the failure is unchanged.
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
- 3d. The role's completion is `close-or-release` and its session has ended (`INV-CCH-28`): this
  module settles it as described in `USECASE-CCH-RELEASE-END`, which includes the case of a
  session that ended without ever claiming its bead.
- 3a. The session hits a post-accept failure (`retryable`, `resource-limit`, `critical`): this
  module surfaces it on its own logs/metrics or as a new event, never as anything but the opaque
  completion outcome the core already stores (`INV-CCH-3`).
- 3c. The session is still working when the role's wait for its bead runs out (`INV-CCH-27`): this
  module applies the role's `on_failure`, as for any other not-complete outcome. The wait is the
  longer of the handler-wide maximum and the role's time budget plus ten minutes, so a role with a
  long time budget is not failed while its budget still has time left; the failure names the wait
  that applied.

### `USECASE-CCH-RELEASE-END` — settle a session that ended, for a role that may give its bead back <!-- uuid: e6c78a04-ae89-4104-9ee8-bf4ee1939436 -->

**Primary actor:** `ACTOR-CCH-CORE`.
**Level:** subfunction (the settle step of `USECASE-CCH-DISPATCH`, for a role whose completion is `close-or-release`).
**Preconditions:** a dispatch has started a session for a bead and is waiting for it.
_Requires:_ `INV-CCH-28`, `INV-CCH-7`, `INV-CCH-15`, `INV-CCH-17`, `INV-CCH-23`.
_Includes:_ `INTF-CCH-CCPOOL`, `INTF-CCH-BEADS`.

1. The handler keeps reading the bead while the session runs, and remembers the first time it
   sees the bead in progress under the role's own actor (the claim latch).
2. The bead is closed: the dispatch is done.
3. The session stops being active and goes quiet, including its subagents: the session has
   ended. (A session that is merely idle while a subagent still writes is not ended; the handler
   keeps waiting.)
4. The bead is unassigned and open or deferred, and the worker gave it back (the latch is set, or
   the bead carries `human`, a future deferral date or an open blocker, which a restarted handler
   reads as the same thing): the dispatch is done.

Extensions:

- 4a. The bead is held by an actor other than the role's own (a peer won the claim): the handler
  writes nothing to the bead, counts no strike and reports no verb. The dispatch returns without
  an error, and the settled session's row is marked incomplete so it is not absorbed again.
- 4b. The session ended and the bead is still `open`, unassigned, not `human`, not deferred and
  without blocker, and the latch was never set (the worker never claimed it): the handler counts
  a strike through the label `drain-unclaimed-end`. The first strike leaves the bead as it is and
  reports the dispatch as unclaimed; the second adds `human` and reports it as escalated. Neither
  is recorded as a session failure.
- 4c. The bead is still held by the role's own actor, or in a status the mode does not
  recognise: the session died holding its claim, so the role's `on_failure` applies, as for
  every other mode (`INV-CCH-7`'s unexplained death, or its external close).
- 4d. The session was closed by ccpool before the bead was handed back, and the bead is not held
  by a peer: `INV-CCH-7` applies (release, comment, strike on `pool-evicted`), ahead of 4b.
- 4e. The handler restarts while the session runs: the latch is lost with it. A bead that the
  worker parked, deferred or converted still reads as handed back (step 4); one it handed back
  inside a single poll interval, with none of those marks, reads as 4b and earns a strike.

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
