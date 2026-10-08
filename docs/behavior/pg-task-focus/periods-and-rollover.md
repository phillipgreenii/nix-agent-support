# pg-task-focus — periods, profiles, tasks and rollover

A routine is tracked in three kinds of period (a day, a week, a sprint), each holding the task
instances its profile calls for. The operator moves between periods and between profiles by explicit
changes; the product never advances a period by itself. This doc owns those changes, the task states
and their rules, and the refusal of a period change while a cycle is active. Zones are in
[`time-and-zones.md`](time-and-zones.md), the cycle states in
[`cycles-and-timer.md`](cycles-and-timer.md), and undo in [`corrections.md`](corrections.md).

## Stories

- **`STORY-PERIOD-START`** <!-- uuid: 2302aaaa-8a44-4370-a271-f13730d9e055 --> — As the operator on first use, I want to set my day,
  week and sprint in one step, so the routine's tasks appear without any other setup.
  _(→ `JOURNEY-PERIOD-BOOTSTRAP`; `INV-PERIOD-1`, `INV-PERIOD-2`, `INV-PERIOD-3`, `INV-PERIOD-4`.)_
- **`STORY-PERIOD-ROLL`** <!-- uuid: d5956098-bde9-4c7f-b0de-12a13ccf8716 --> — As the operator, I want to roll over to a new day, week
  or sprint after seeing exactly what will be marked missed and what will appear, and to decide which
  open tasks I am skipping on purpose, so nothing is lost or carried silently.
  _(→ `JOURNEY-PERIOD-ROLLOVER`; `INV-PERIOD-5`, `INV-PERIOD-6`, `INV-PERIOD-7`, `INV-PERIOD-8`,
  `INV-PERIOD-9`, `INV-PERIOD-10`, `INV-PERIOD-11`, `INV-PERIOD-12`, `INV-PERIOD-16`.)_
- **`STORY-PERIOD-BACKDATE`** <!-- uuid: 38626aaf-4c3d-4cff-a84b-ca52612bc773 --> — As the operator who forgot to change the day, I want
  to roll over as of yesterday or last week, with no limit on how far back, so the missed times are
  right. _(→ `JOURNEY-PERIOD-FORGOTTEN-DAY`; `INV-PERIOD-13`, `INV-PERIOD-14`, `INV-PERIOD-17`,
  `INV-PERIOD-18`, `INV-PERIOD-19`.)_
- **`STORY-PERIOD-PROFILE`** <!-- uuid: 17111f85-3469-4da4-8e0f-3aedd6013ced --> — As the operator going on call, I want to switch to
  another profile mid-period, with the tasks it adds, withdraws and reinstates shown first, so my
  checklist matches my week. _(→ `JOURNEY-PERIOD-ONCALL`; `INV-PERIOD-15`, `INV-PERIOD-8`.)_
- **`STORY-PERIOD-TASKS`** <!-- uuid: f10293d7-84e0-4f18-aca9-b98ccb090cb5 --> — As the operator, I want to record "done at time T" or
  "not doing it, because R" for each task, to finish a task late that was marked missed, and to be told
  what is next, so the checklist is an honest record. _(→ `JOURNEY-PERIOD-TASK-DAY`; `INV-PERIOD-20`,
  `INV-PERIOD-21`, `INV-PERIOD-22`, `INV-PERIOD-23`, `INV-PERIOD-24`, `INV-PERIOD-25`.)_

## Journeys

### `JOURNEY-PERIOD-BOOTSTRAP` — the first day <!-- uuid: 44848329-e8f2-4f38-8d90-5436b55c6c8b -->

**Primary actor:** `ACTOR-OPERATOR`. **Other actors:** `ACTOR-CLIENT`.
_Requires:_ `INV-PERIOD-1`, `INV-PERIOD-2`, `INV-PERIOD-3`, `INV-PERIOD-4`. _Includes:_ none.

1. The log is empty. The state a client reads is `uninitialized`, and the header says to set the day,
   week and sprint.
2. The operator supplies a day, a week with its end and a sprint with its end, each with the zone in
   force, as one change.
3. One batch is appended, beginning with a profile change that carries the default profile, then each
   period change followed by the tasks it materializes from that profile (`INV-PERIOD-2`).

Extensions:

- 3a. A cycle is running or paused. Bootstrap is a period change like any other and is refused as
  `cycle_active` (`INV-PERIOD-17`).

### `JOURNEY-PERIOD-ROLLOVER` — rolling the day <!-- uuid: f2b60797-db19-4e60-b02c-8e4a2a0d6252 -->

**Primary actor:** `ACTOR-OPERATOR`. **Other actors:** `ACTOR-CLIENT`.
_Requires:_ `INV-PERIOD-3`, `INV-PERIOD-5`, `INV-PERIOD-6`, `INV-PERIOD-7`, `INV-PERIOD-8`,
`INV-PERIOD-9`, `INV-PERIOD-10`, `INV-PERIOD-11`, `INV-PERIOD-12`, `INV-PERIOD-16`.
_Includes:_ none.

1. At the end of a day the client shows "New day: roll over" as the next action (`INV-PERIOD-16`).
2. The operator asks for a preview (a dry run). It lists the open tasks of each period being left,
   the tasks the new periods would materialize and any that could not be, with the reason, and the
   state version it was built at (`INV-PERIOD-7`).
3. The operator marks one open task as skipped with a reason and leaves the rest to be marked missed.
4. On confirm, one batch is appended: the rollover of the open tasks, then the new period and its
   tasks (`INV-PERIOD-5`, `INV-PERIOD-9`). The confirm carries the preview's version; if anything
   changed since, it is refused as `stale_preview` (`INV-PERIOD-8`).
5. The skipped task keeps its reason, the others are `missed`, completed tasks are untouched, and
   nothing is carried into the new day (`INV-PERIOD-10`, `INV-PERIOD-11`, `INV-PERIOD-12`).

Extensions:

- 3a. The operator gives one shared reason to skip every open task. A per-task override wins where both
  apply (`INV-PERIOD-9`).
- 3b. A reason is empty or only whitespace. The whole request is refused as `invalid_request`.
- 4a. An override names a task that is unknown or already resolved. The whole request is refused as
  `unknown_task` or `task_already_resolved`.
- 4b. The new `start` is not later than the current start of that kind: `period_unchanged`
  (`INV-PERIOD-4`).

### `JOURNEY-PERIOD-FORGOTTEN-DAY` — the day was never rolled, and a cycle ran overnight <!-- uuid: c2a5fa3a-b40b-47a4-b1c2-6746e5dc3ff9 -->

**Primary actor:** `ACTOR-OPERATOR`. **Other actors:** `ACTOR-CLIENT`.
_Requires:_ `INV-PERIOD-13`, `INV-PERIOD-14`, `INV-PERIOD-17`, `INV-PERIOD-18`, `INV-PERIOD-19`.
_Includes:_ none.

1. It is Wednesday. The operator forgot to roll Tuesday, and a cycle was left running overnight.
2. The operator asks to roll as of Tuesday evening. The request is refused as `cycle_active`, naming
   the running cycle (`INV-PERIOD-17`); the preview lists it as blocking.
3. The operator stops the cycle at the time the work really ended ("End at"), which does not touch the
   period (`INV-PERIOD-19`), and repeats the roll as of Tuesday evening.
4. The change is accepted, because no cycle is active now; the missed times are Tuesday's
   (`INV-PERIOD-13`). A cycle that had run across the backdated instant and is stopped now does not
   block (`INV-PERIOD-18`).

Extensions:

- 4a. The backdated change would make the log impossible, for example sorting before an earlier
  change of the same kind whose start is lower. It is refused with the specific code
  (`period_out_of_order`), not a generic one (`INV-PERIOD-14`).

### `JOURNEY-PERIOD-ONCALL` — switching profile <!-- uuid: b8be6538-c150-463f-8395-537d33f4adf6 -->

**Primary actor:** `ACTOR-OPERATOR`. **Other actors:** `ACTOR-CLIENT`.
_Requires:_ `INV-PERIOD-8`, `INV-PERIOD-15`. _Includes:_ none.

1. The operator previews a switch to the on-call profile. The preview lists the tasks it would add,
   withdraw and reinstate in the current periods and flags any that would be overdue immediately.
2. On confirm, completed, skipped and missed tasks are left alone; added tasks are materialized, open
   tasks the new profile no longer lists are withdrawn, and withdrawn tasks it lists again are
   reinstated, never materialized a second time.
3. The same switch can ride inside a period change, in which case it applies to the periods that remain
   current and the new periods come from the new profile (`INV-PERIOD-5`).

### `JOURNEY-PERIOD-TASK-DAY` — a day of tasks <!-- uuid: 140a07a9-3ffd-4c13-b2c2-5a777b26c4e9 -->

**Primary actor:** `ACTOR-OPERATOR`. **Other actors:** `ACTOR-CLIENT`.
_Requires:_ `INV-PERIOD-20`, `INV-PERIOD-21`, `INV-PERIOD-22`, `INV-PERIOD-23`, `INV-PERIOD-24`,
`INV-PERIOD-25`. _Includes:_ none.

1. The state names the next task: the open task with the earliest due time (`INV-PERIOD-25`).
2. The operator completes it, and skips another with a reason (`INV-PERIOD-21`).
3. After the next rollover the operator remembers a task from yesterday that is now `missed`, and
   completes it with an `effective_at` of yesterday afternoon; the completion supersedes the `missed`
   marker (`INV-PERIOD-22`).

Extensions:

- 2a. The task was already completed or skipped: `task_already_resolved`, naming the resolving event
  (`INV-PERIOD-21`).
- 2b. The task is withdrawn: `task_withdrawn`, unless the `effective_at` precedes the withdrawal
  (`INV-PERIOD-23`).
- 3a. The completion is earlier than the task's materialization: `resolution_before_materialization`
  (`INV-PERIOD-24`).

## The state interface

### `INTF-STATE` — the state a client reads <!-- uuid: fb7e3c88-c9d9-4d9d-83ad-4fdecd24e9f6 -->

**Counterparty:** `ACTOR-CLIENT` (kind: actor; essential participant: the operator sees the product
only through clients). **What crosses out:** the header (the current day, week and sprint with their
dates, labels and zones, and the active profile), the task instances of each period with status, due
instant and zone, `next`, the running cycle (the focus) with elapsed and remaining time computed at
read time, the dimmed cycles, the interrupt stack, the resume offer, the store health, the state
version, and the "the date has moved on" banner when a period's last civil date has passed in its
zone. Nothing is ticked into the log: every value that depends on the clock is computed when read.
**What must hold:** `INV-PERIOD-1`, `INV-PERIOD-16`, `INV-PERIOD-24`, `INV-PERIOD-25`,
`INV-CYCLE-3`, `INV-CYCLE-12`, `INV-LOG-19`, `INV-LOG-22`.

## Invariants

### Periods and rollover

- **`INV-PERIOD-1`** <!-- uuid: efcc6bf3-1f68-460a-a02b-93ab1c7da649 --> — With an empty log the product MUST serve the state
  `uninitialized`, and a client says "set your day, week and sprint". The header showing the current
  day, week, sprint and profile is read-only; a change is made only through an explicit change request.
- **`INV-PERIOD-2`** <!-- uuid: 0bfffc6a-26bf-45c3-aa54-18e816c3ee77 --> — The bootstrap batch MUST begin with a `profile.changed`
  carrying the default profile, so the active profile is always the latest live `profile.changed` in
  the log and replay never consults the configuration. The first `period.changed` of each kind has no
  tasks to roll over and materializes its tasks.
- **`INV-PERIOD-3`** <!-- uuid: 04c73c09-a090-4884-96d9-c6ef96da9140 --> — One change request MUST take one or more period changes (a
  kind, a civil `start`, an `end` for a week or sprint, the zone in force, an optional label) as one
  atomic batch, plus an optional `effective_at`, profile, overrides, shared skip reason, expected
  version, dry-run flag and request `id` (used as the batch id). `end` is REQUIRED for a week and a
  sprint and MUST be absent for a day (`invalid_request` otherwise). Each kind changes independently:
  changing the day leaves weekly tasks alone.
- **`INV-PERIOD-4`** <!-- uuid: 6b83730c-9d23-4c45-951a-c711c1d934f2 --> — A new `start` MUST be later than the current `start` of
  that kind (`period_unchanged`, found against the present state). A period change that sorts, by
  effective instant, before an existing change of the same kind whose `start` is lower than its own is
  `period_out_of_order`. Replay classifies from the events alone, so a stored log and a candidate
  agree on the code.
- **`INV-PERIOD-5`** <!-- uuid: 78e4ab99-9523-4024-8824-96244eeaf163 --> — A period change batch MUST be ordered: the rollover of open
  tasks in the periods being left; then a `profile.changed` if the request carries a profile (it
  applies to the periods that remain current, never to a period being left); then each new
  `period.changed` and its tasks, materialized from the now-active profile. There is no profile in a
  `period.changed`.
- **`INV-PERIOD-6`** <!-- uuid: 5ddf91e7-c960-4cf9-8c7a-7f5878246613 --> — A task instance MUST be materialized with its due instant
  always present, and a snapshot of its title, group, link and resolved due rule (`INV-LOG-6`). Tasks
  of a period sort by group order, then due time (`INV-CONF-9`).
- **`INV-PERIOD-7`** <!-- uuid: 5de256a8-9dd8-4039-8e94-48e1bdfbd015 --> — A dry run of a period change MUST return, without
  appending, the open tasks of each period being left, the tasks the new periods would materialize and
  any that could not be with the reason, the tasks a requested profile change would add, withdraw and
  reinstate in the periods that remain current, the cycles that block the change, and the state
  version. It MUST return exactly the events the real request would append, except that when cycles
  block the real request is refused.
- **`INV-PERIOD-8`** <!-- uuid: fe093b3c-73c3-479b-bc6b-6ba3c160e76c --> — A confirm that carries the preview's expected version MUST
  be refused as `stale_preview` if either member of the version differs, so what the operator
  confirmed is what happens. The web UI and the period-roll command MUST carry the version back; other
  clients MAY.
- **`INV-PERIOD-9`** <!-- uuid: 451319e7-bdfd-4e46-8582-94765763b21b --> — On rollover each open task MUST get a `task.missed` by
  default. The operator MAY override any single task to `task.skipped` with a reason, or mark every
  open task skipped with one shared reason; where both apply, the override wins. An override that names
  an unknown or an already-resolved task MUST refuse the whole request. Every reason MUST be non-blank
  (empty or only whitespace is `invalid_request`).
- **`INV-PERIOD-10`** <!-- uuid: 1c3a60fc-c16c-4ddc-9ee9-c91354914233 --> — A rollover MUST touch only the tasks that are open when it
  is validated: completed, skipped, missed and withdrawn tasks are left alone.
- **`INV-PERIOD-11`** <!-- uuid: 94848220-04f3-41f2-b0f9-5551df36cbf8 --> — There MUST be no carry-over. A task still open at rollover
  is `missed` or `skipped`, never carried into the next period, no option requests carrying, and the
  new period's tasks have no link to the old ones. _(Operator ruling, 2026-10-07.)_
- **`INV-PERIOD-12`** <!-- uuid: 30acb89b-e762-4ed3-ab68-9ee2bedb2717 --> — `missed` and `skipped` MUST be distinct on purpose.
  `missed` means the operator did not act; `skipped` is a decision with a stated reason. Reporting and
  attention treat them differently.
- **`INV-PERIOD-13`** <!-- uuid: 23810531-b9bd-4961-b240-6726539b6058 --> — `effective_at` MAY backdate a period change by any amount,
  so the missed times are correct: there is no lower bound and no setting for one. Only the
  future-skew rule and the timeline apply. _(Operator ruling, 2026-10-08: "no restrictions, let me do
  what is necessary to fix the date")_
- **`INV-PERIOD-14`** <!-- uuid: 87bd0dca-8548-48b4-bb7d-cc4cff19fdc8 --> — A backdated change that candidate replay finds impossible
  MUST be refused with the specific code of the condition found (for example `period_out_of_order`),
  never a generic code. A late completion of a task that was marked `missed` is unaffected.
- **`INV-PERIOD-15`** <!-- uuid: 51238d5e-531d-45f2-8407-793183929875 --> — A profile change MUST leave completed, skipped and missed
  tasks alone. For each current period it materializes the tasks the new profile adds, gives
  `task.withdrawn` to open tasks the new profile no longer lists (and to those whose definition has
  vanished from the configuration), and gives `task.reinstated` to withdrawn tasks the new profile
  lists again. Its dry run lists the added, withdrawn and reinstated tasks and flags any that would be
  overdue immediately. An unknown profile is `unknown_profile`.
- **`INV-PERIOD-16`** <!-- uuid: cdf12d93-6f1a-4f99-8d2d-0ebea49a48b0 --> — The product MUST NOT advance a period by itself. When a
  period's last civil date has passed in its zone, a client shows "New day: roll over", "New week: roll
  over" or "New sprint: roll over", using exactly these strings everywhere, as the next action; tasks
  of the ended period stay completable until it is rolled over. If several kinds have ended, one
  confirmation rolls them together; a client pre-fills `start` as today in the zone and, for a week or
  sprint, `end` as `start` plus the length of the previous period of that kind, and the operator MAY
  edit it.

### Period change and active cycles

- **`INV-PERIOD-17`** <!-- uuid: 2b76b0cd-ad86-4d2c-9445-8aa9729c3e43 --> — A period change (a rollover, bootstrap included) MUST be
  refused as `cycle_active`, with a message and details naming the cycles, while ANY cycle is not
  stopped in the present state (running, or paused and dimmed), whatever the change's `effective_at`.
  The operator stops them first. There is no "End at" prefill on rollover. _(Operator ruling,
  2026-10-08: "lets keep this simple and disallow a rollover with an active cycle"; that the check
  reads the present state, whatever the change's instant, is the operator's decision of the same
  day.)_
- **`INV-PERIOD-18`** <!-- uuid: 0e5e6ce9-0ac6-4903-a5b9-c781b53eb83f --> — A cycle that ran across a backdated instant but is stopped
  now MUST NOT block the change, and a stopped cycle never blocks: a cycle is not tied to a period, so
  a cycle that spans a rollover is harmless. The check is on the present state of the cycles.
- **`INV-PERIOD-19`** <!-- uuid: 4f441044-5d7a-4ee7-a5b6-595f8e5669f3 --> — A period change MUST NEVER stop a cycle by itself, emit any
  cycle event or prefill a stop time. A cycle forgotten overnight is stopped with "End at" at the time
  the operator names, and the change is then repeated. The dry run MUST list the blocking cycles (id,
  title, status and start) and still return the full preview, and a client MUST then block
  confirmation and show each blocking cycle with Stop and End at actions; a period-roll command prints
  the blocking cycles and exits unsuccessfully.

### Tasks

- **`INV-PERIOD-20`** <!-- uuid: cf3d5e89-3819-4eed-909c-c56a7be6ae00 --> — A task is `open` (materialized with no live resolution),
  `completed`, `skipped`, `missed` or `withdrawn`, derived from its live events by this precedence: a
  live `completed` or `skipped` wins; it supersedes `missed` regardless of `effective_at` order, and
  supersedes `withdrawn` only when its `effective_at` precedes the withdrawal; otherwise the end of the
  withdraw-and-reinstate chain decides `withdrawn`; otherwise a live `missed` gives `missed`;
  otherwise `open`.
- **`INV-PERIOD-21`** <!-- uuid: 4f95639d-28b7-44a4-828e-8f67e3fc3633 --> — A task MAY be completed or skipped from `open` or `missed`;
  a second completion or skip of a resolved task MUST be refused as `task_already_resolved`, naming the
  resolving event, whether found from the present state or because a correction or the retraction of a
  retraction would leave two live resolutions. A skip MUST carry a non-blank reason.
- **`INV-PERIOD-22`** <!-- uuid: e5224b50-f981-4baf-a5b8-451564cf219e --> — A late completion or skip of a task that was marked
  `missed` MUST be allowed and MUST supersede the `missed`, so a task that rolled over at 00:05 can
  still be completed with an `effective_at` of 16:00 the day before. `task.missed` and `task.withdrawn`
  are markers, not resolutions, and `missed` is exempt from the ordering checks.
- **`INV-PERIOD-23`** <!-- uuid: af8a663c-9d26-48eb-98af-c56b124b5735 --> — A withdrawn task MUST be reinstated before it can be
  completed or skipped (`task_withdrawn`), unless the completion or skip has an `effective_at` that
  precedes the withdrawal, in which case it is accepted and supersedes the withdrawal. A reinstatement
  of a task that is not withdrawn is `task_not_withdrawn`.
- **`INV-PERIOD-24`** <!-- uuid: ce0d5886-b3eb-4e90-a4ef-b0b4851d9554 --> — A completion or skip effective before the task's
  materialization MUST be refused as `resolution_before_materialization`, and a task of a period the
  log no longer holds a change for as `task_without_period`, and a task materialized before the first
  live profile change as `task_before_profile`.
- **`INV-PERIOD-25`** <!-- uuid: 9dd14057-9109-45e5-bbd1-391b1fb248db --> — The state MUST expose `next`: the open task with the
  earliest due time across all kinds, ties broken by group order. Withdrawn and resolved tasks are
  never next. A client shows an overdue task as overdue; the wording is the client's.
