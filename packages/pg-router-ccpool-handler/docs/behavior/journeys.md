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
  `INV-CCH-20`, `INV-CCH-21`, `INV-CCH-22`, `INV-CCH-23`, `INV-CCH-24`, `INV-CCH-25`, `INV-CCH-26`, `INV-CCH-27`, `INV-CCH-28`,
  `INV-CCH-29`, `INV-CCH-30`, `INV-CCH-31`.)_
- **`STORY-CCH-QUERY`** <!-- uuid: 661a1b2c-4243-42b3-8fcc-607e8ec7e4af --> — As pg-router's core, I
  want a registered source to query beads for events on my behalf, so pg-router itself never needs
  to know beads' query language. _(→ `USECASE-CCH-QUERY`; `INV-CCH-1`, `INV-CCH-4`, `INV-CCH-5`.)_

## Use cases

### `USECASE-CCH-DISPATCH` — run a dispatched event as a handler session <!-- uuid: 3c471620-e5bc-4a7d-b81a-6100cc862f74 -->

**Primary actor:** `ACTOR-CCH-CORE`.
**Level:** user-goal.
**Preconditions:** this module is registered with a reachable core (`phillipgreenii-nix-agent-support`
ADR 0036 — this module never starts a core).
_Requires:_ `INTF-HANDLER`, `INV-CCH-2`, `INV-CCH-3`, `INV-CCH-6`, `INV-CCH-8`, `INV-CCH-11`, `INV-CCH-12`, `INV-CCH-13`, `INV-CCH-14`, `INV-CCH-15`, `INV-CCH-16`, `INV-CCH-17`, `INV-CCH-18`, `INV-CCH-19`, `INV-CCH-20`, `INV-CCH-21`, `INV-CCH-22`, `INV-CCH-23`, `INV-CCH-24`, `INV-CCH-25`, `INV-CCH-26`, `INV-CCH-27`, `INV-CCH-28`, `INV-CCH-29`, `INV-CCH-30`, `INV-CCH-31`.
_Includes:_ `INTF-CCH-CCPOOL` or a configured command, per the role's own backing kind; `USECASE-CCH-RELEASE-END` for a role whose completion is `close-or-release`; `USECASE-CCH-SPLIT-TRIAGE` for a role whose completion is `close-or-split-triage`.

1. The core dispatches one event under one tracking id to a bound role.
2. This module starts a fresh handler session of its own (`INV-CCH-30`; extension 2k is the one
   case that waits on an existing session instead): a ccpool-backed role drives `ccpool`; a
   command-backed role execs its configured argv.
   2n. A ccpool-backed role starts with the handler-wide tool grants plus its own extra grants, if
   it sets any, and no other role gets them (`INV-CCH-25`).
   2l. A ccpool-backed session that runs under `permissionMode=dontAsk` is sent a code-owned notice
   ahead of the role's own task text: writes to the agent runtime's protected paths (the `.claude/`
   directory, except `.claude/worktrees`) are denied automatically in that mode and no allow-list
   entry can grant them. If the task needs such a write, the session does not attempt or retry it,
   stops at once, and hands the task back through the role's normal hand-back path, stating why and
   carrying the exact intended change (a unified diff, or the full content of a new file). No
   role's prompt template can edit the notice, and it is not sent under any other permission mode
   (`INV-CCH-16`); choosing the mode is a deployment decision.
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
  role's own actor already holds `in_progress` is never declined here, because it is the live
  session of an earlier dispatch, which 2k absorbs while its turn is in progress. It reads only, and
  any failure to read launches as normal.
- 2m. Preparing the per-bead isolation fails because the filesystem has run out of room
  (`INV-CCH-8`): the ccpool-backed handler executor does not count this as a launch failure of
  this bead, because a full disk is a transient, system-wide condition that would hit whichever
  bead dispatched next. This module declines `busy` with reason `low-disk` before any session is
  launched and writes nothing to any bead (no launch-failure stamp, no `human`); the core
  re-offers the event later with backoff, as for 2a.
- 2i. A session dies unexplained before its bead completes (`INV-CCH-7`): this module also logs
  the pool's usage-limit reading once (`INV-CCH-26`), as evidence only; the failure is unchanged.
- 2j. A drain session (completion `close-or-release`) raises a permission dialog nobody can answer
  and sits in `needs_input` (`INV-CCH-29`). After 15 minutes without a write to its transcript, its
  own dispatch sends Escape; if it is still silent one quiet window later, the dispatch closes it
  (not purged) and releases the claim in one call, only when the bead is still held by the role's
  own actor. A bead that stalls twice in a row also gets `human`. Two events record it. A session
  that left `needs_input` in between, or kept writing, is not touched, and a session of any other
  role is never cleared this way. Meanwhile the budget watchdog holds its reminder back, because
  the Enter after a paste would accept the dialog.
- 2d. An earlier dispatch's handler died (it crashed or was killed; a daemon restart alone no
  longer kills it, `INV-CCH-14`) and left its session unsupervised: its lease has expired. Before checking
  capacity this module handles each such orphan of its own role (`INV-CCH-18`). An `idle` or
  `errored` one is closed (not purged), its bead claim released if the role's own actor still
  holds it, and its worktree removed; a `starting`, `ready` or `working` one past its time budget
  is hard-stopped as a supervised session would be (`INV-CCH-11`, `INV-CCH-12`), then its worktree is removed;
  an under-budget one, a `needs_input` one, and one with no lease are left alone (an under-budget
  `starting` or `ready` one is then closed and replaced, 2k). The freed pool slot is then
  available to this very dispatch. If this dispatch is instead a redelivery of the event an orphan
  was launched for, and the orphan's turn is still in progress, it absorbs the orphan (2k): it
  takes over the lease and keeps the orphan's original launch time as the start of its budget.
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
  Until then the record is never absorbed (`INV-CCH-30`) and never reclaimed as an orphan.
- 2g. A role with worktree isolation is configured to pre-fetch (`INV-CCH-21`), as a review of a
  pull request is: after the per-bead worktree exists and before the session launches, this module
  makes the item's commit local (fetching it only when it is not), checks the worktree out at it,
  and writes the change's file statistics and diff to files in the worktree. The prompt is told the
  commit and the file paths, so the session starts already on the commit with the diff on disk,
  and neither the time nor a pool slot is spent on it. If any step fails (an origin that needs a
  credential the handler lacks, a commit that cannot be found), the dispatch still proceeds and the
  prompt is told the pre-fetch did not succeed, so it does those steps itself.
- 2k. The role's pool already holds a session for the same bead and role (`INV-CCH-30`). If that
  session's turn is in progress (working, or paused for a person) for this same event, nobody has
  closed it or begun to tear it down, and its bead is not already closed, this module absorbs it:
  it waits on that session as its own, sends it nothing, and keeps its original launch time as the
  start of its budget. This is the redelivery a daemon restart or a crash window produces. Any
  other attempt still in flight (one being launched; one whose turn has ended or whose session has
  died while its handler still holds the lease; one for another event or with no event id to
  compare; one closed while it still runs; or one whose bead is already closed) makes this module
  decline `busy` with reason `session-in-flight` and launch nothing; a pool that cannot be read is
  treated as holding one in flight and declines `busy` the same way but with its own reason,
  `pool-unreadable`, which the failure-rate alert counts (bead `pg2-s3pjt`). The core re-offers the
  event later, and a dispatch that finds no attempt in flight launches a fresh session. A leftover nobody supervises (still `starting` or `ready`, its lease expired) is closed
  and replaced by this dispatch's own session. Any other session is never reused, whoever closed
  it: a re-review after a head advance, a re-request after a hand-back and a reopened bead each
  get a fresh session.
- 3d. The role's completion is `close-or-release` and its session has ended (`INV-CCH-28`): this
  module settles it as described in `USECASE-CCH-RELEASE-END`, which includes the case of a
  session that ended without ever claiming its bead.
- 3e. The role's completion is `close-or-split-triage` (`INV-CCH-13`): its session works a bead
  parked for split review, and this module settles the dispatch as described in
  `USECASE-CCH-SPLIT-TRIAGE`.
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
_Requires:_ `INV-CCH-28`, `INV-CCH-7`, `INV-CCH-15`, `INV-CCH-17`, `INV-CCH-23`, `INV-CCH-30`.
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
  an error; like every settled session, this one is never absorbed by a later dispatch
  (`INV-CCH-30`).
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

### `USECASE-CCH-SPLIT-TRIAGE` — settle a bead parked for split review, for the split-triage role <!-- uuid: e0b4ef0a-189d-4297-b11e-e6454c930cbc -->

**Primary actor:** `ACTOR-CCH-CORE`.
**Level:** subfunction (the work and settle steps of `USECASE-CCH-DISPATCH`, for a role whose completion is `close-or-split-triage`).
**Preconditions:** a dispatch has started a session of the split-triage role for a bead labelled `needs-split-review`, the label the escalation triager gives a budget-stopped bead (labelled `escalated` by `INV-CCH-12`) that it judges splittable, or that an operator adds. The deployment wires the role, its discovery query and its tool grant for the `split` subcommand; this module's own beads-backed source never discovers a bead that carries the label (`INV-CCH-12`).
_Requires:_ `INV-CCH-13`, `INV-CCH-11`, `INV-CCH-12`.
_Includes:_ `INTF-CCH-CCPOOL`, `INTF-CCH-BEADS`.

1. The session reads the bead and its stop history (the stopped sessions recorded on it,
   `INV-CCH-11`) and decides whether it can be split.
2. The session records exactly one outcome through this module's own `split` subcommand, as its
   role's prompt template directs, never by hand-built `bd` calls. All bead text reaches `bd` as
   discrete arguments taken from the session's input, never through a shell (`INV-CCH-13`).
3. Splittable (`split apply`): the session supplies a plan of at least two children, each with a
   title, a description and acceptance criteria. This module creates each child with the parent's
   labels, except its `budget-stop:*` records, `needs-split-review`, `was-split`, `human`,
   `escalated` and any `split-from:*`, plus a `split-from:` label naming the parent, so no child
   inherits a stop count. It then makes the parent blocked by each child and checks that those
   edges exist, marks the parent `was-split`, comments the decomposition, releases a claim the
   parent still holds (open and unassigned, in one update), and removes `needs-split-review` last.
4. Not splittable (`split unsplittable`): the session supplies the reason. This module adds
   `human`, comments the reason together with the stop history, releases a claim the bead still
   holds, and removes `needs-split-review` last.
5. The dispatch is done when the bead is closed or `needs-split-review` is gone. The label's
   removal is the completion signal, so both outcomes write it last.

Extensions:

- 3a. The parent is closed, already `was-split`, or itself a split child (it carries a
  `split-from:` label): `split apply` refuses and writes nothing, so a bead is split at most once
  and a child never starts a second round.
- 3b. A split fails after its children exist: the parent fails safe to `human` (`needs-split-review`
  removed, any held claim released, a comment naming the children created) and is not retried.
- 5a. The session hands the bead back or only comments: neither is an outcome, so the dispatch is
  not done; only a close or the removal of `needs-split-review` completes it.
- 5b. The split-triage session itself hits its budget: the bead is routed to `human` by the budget
  stop's escalation (`INV-CCH-12`) and also has `needs-split-review` removed, and the role never
  re-enters the split path. No part of this role changes a budget.

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
