# pg-task-focus — corrections and retractions

The log never changes, so a mistake is fixed by appending an event that supersedes or retracts an
earlier one, and the corrected view is always derivable from the log. This doc owns those rules, and
the two named fixes the operator reaches for most: back-filling a forgotten break and ending a cycle
at an earlier time. The log's own rules are in [`event-log.md`](event-log.md); the cycle states a fix
must respect are in [`cycles-and-timer.md`](cycles-and-timer.md).

## Stories

- **`STORY-CORR-FIX`** <!-- uuid: a56f10c5-1fab-40a4-96af-2c79a0e5ffa6 --> — As the operator, I want to fix a wrong time, reason or
  value after the fact without losing what was originally recorded, so my history is honest about
  both what happened and what I got wrong. _(→ `JOURNEY-CORR-FIX-TYPE`; `INV-CORR-1`, `INV-CORR-2`,
  `INV-CORR-3`, `INV-CORR-4`, `INV-CORR-5`, `INV-CORR-6`, `INV-CORR-7`, `INV-CORR-16`.)_
- **`STORY-CORR-UNDO`** <!-- uuid: 659a8385-3b21-4b52-9008-2a485e6e01fe --> — As the operator, I want to undo any change, including an
  earlier undo, so no fix is ever a one-way door. _(→ `JOURNEY-CORR-UNDO-ROLLOVER`; `INV-CORR-8`,
  `INV-CORR-9`, `INV-CORR-10`, `INV-CORR-11`, `INV-CORR-12`, `INV-CORR-13`, `INV-CORR-14`.)_
- **`STORY-CORR-BREAK`** <!-- uuid: 4b4d91db-26db-41cb-9e58-de13d3c94cfa --> — As the operator, I want to add a lunch break I forgot to
  pause for, and to end a cycle I forgot to stop at the time I really finished, without editing
  events by hand. _(→ `JOURNEY-CORR-FORGOTTEN-LUNCH`; `INV-CORR-15`, `INV-CORR-17`, `INV-CORR-18`.)_
- **`STORY-CORR-SAFE`** <!-- uuid: 09365cda-71c2-4892-89c4-f5fa53f3b6ad --> — As the operator, I want a fix that would make the log
  impossible to be refused with a reason I can act on, so a correction can never break my record.
  _(→ `JOURNEY-CORR-UNDO-ROLLOVER`; `INV-CORR-14`, `INV-CORR-19`.)_

## Journeys

### `JOURNEY-CORR-FORGOTTEN-LUNCH` — a break nobody paused for <!-- uuid: 0edc64d3-dab8-451f-84e1-b39b4f7e18fb -->

**Primary actor:** `ACTOR-OPERATOR`. **Other actors:** `ACTOR-CLIENT`.
_Requires:_ `INV-CORR-15`, `INV-CORR-17`, `INV-CORR-18`, `INV-CORR-19`. _Includes:_ none.

1. A cycle ran from 09:00; the operator ate lunch from 12:00 to 13:00 without pausing, and stopped the
   cycle at 13:30.
2. The operator back-fills a break from 12:00 to 13:00. The system appends a pause at 12:00 and a
   resume at 13:00 as one batch (`INV-CORR-15`); the cycle's running time drops by an hour.
3. Had the operator forgotten to stop the cycle and discovered it the next morning, the fix is "End
   at": a stop at the time the work really ended (`INV-CORR-17`).

Extensions:

- 2a. The break would overlap a pause the cycle already has. It is refused as
  `cycle_segments_overlap` (`INV-CORR-15`).
- 2b. The break would end at or after the instant the cycle was stopped. It is refused as
  `break_ends_at_stop`, and the message points at "End at" (`INV-CORR-15`).
- 3a. "End at" names a time earlier than the cycle's existing stop. It is refused as `cycle_stopped`;
  the operator corrects that stop's time instead (`INV-CORR-18`).

### `JOURNEY-CORR-UNDO-ROLLOVER` — undoing a rollover <!-- uuid: 63865a90-9dea-46c3-9e83-5f97edc41f23 -->

**Primary actor:** `ACTOR-OPERATOR`. **Other actors:** `ACTOR-CLIENT`.
_Requires:_ `INV-CORR-8`, `INV-CORR-9`, `INV-CORR-10`, `INV-CORR-11`, `INV-CORR-12`, `INV-CORR-13`,
`INV-CORR-14`, `INV-CORR-19`. _Includes:_ none.

1. The operator rolled the day by mistake. The change was one batch.
2. The operator retracts the batch with one retraction that names it as `target_batch`
   (`INV-CORR-10`). The log returns to the previous periods and the missed tasks are open again.
3. The operator retracts that retraction by its `target`, and the rollover is back in force
   (`INV-CORR-8`, `INV-CORR-9`).

Extensions:

- 2a. The operator already completed a task of the new period. The retraction is refused as
  `batch_has_dependents`, naming the completing events (`INV-CORR-11`). A late completion of a task
  the rollover had marked `missed` does not count as a dependent: retracting removes the `missed`
  marker and leaves the completion.
- 2b. The operator tries to retract one event of the batch alone. It is refused as
  `invalid_correction`; a batch member is retracted only through its batch (`INV-CORR-10`).
- 2c. The batch is already retracted. The request succeeds as a no-op with `changed: false` and a note
  naming the retraction (`INV-CORR-13`).

### `JOURNEY-CORR-FIX-TYPE` — a cycle started with the wrong type <!-- uuid: 3a97819d-c912-4201-b2c3-7788288ea0eb -->

**Primary actor:** `ACTOR-OPERATOR`. **Other actors:** `ACTOR-CLIENT`, `ACTOR-CONFIGURER`.
_Requires:_ `INV-CORR-3`, `INV-CORR-4`, `INV-CORR-5`, `INV-CORR-6`, `INV-CORR-7`, `INV-CORR-16`.
_Includes:_ none.

1. The operator started a review cycle that was really deep work.
2. In the corrections editor the operator changes the cycle's type. The system validates the new
   type against the configured cycle types and fills in the cycle's title from the configuration at
   that moment; the operator never supplies a title (`INV-CORR-5`).
3. The original event is still there, shown beside the corrected view (`INV-CORR-1`).

Extensions:

- 2a. The type is not defined in the configuration. The correction is refused as `unknown_cycle_type`.
- 2b. The operator tries to change the event's envelope type, or an identity field. It is refused as
  `invalid_correction` (`INV-CORR-4`).

## Invariants

### Correcting

- **`INV-CORR-1`** <!-- uuid: 7ef71d88-2e95-4efa-ba09-bfdf79212f5f --> — A mistake MUST be fixed by a correction event, never by
  editing or deleting the original. The original is always preserved and the corrected view MUST
  always be derivable from the log. A client MUST be able to read both views: the corrected view
  shows each live event with its corrections applied, the original event's id and the correcting
  events; the original view shows the raw events; both include retracted events, flagged.
- **`INV-CORR-2`** <!-- uuid: e39c3fbc-85e4-4dee-a02f-e8a236b6769d --> — Corrections and retractions MUST apply in log order, and for
  several corrections of one target the last wins. A correction replaces whole fields, and applying
  the same correction twice MUST change nothing further.
- **`INV-CORR-3`** <!-- uuid: 1c97d83d-f407-4973-84a2-5ec20e40e78b --> — A target MUST precede the event that corrects or retracts it
  in the log. A target that does not exist, or a `target_batch` that matches no batch, is refused as
  `unknown_event`. A hand-edited log that breaks this is rejected on replay with the same code.
- **`INV-CORR-4`** <!-- uuid: 97b36b94-9895-4b90-b006-96f59f2499c2 --> — A correction MUST NOT change the event's envelope `type` or
  any identity field: `cycle_id`, `task_id`, `target`, `target_batch`, `batch`, `interrupts`,
  `definition`, `kind`, `start`, `cadence`, `period` and `profile`. It MUST NOT target an
  `event.corrected`, an `event.retracted` or a `batch.committed`. A violation is refused as
  `invalid_correction`, naming the stored target and the field.
- **`INV-CORR-5`** <!-- uuid: 737d0e6a-94f6-4cd6-bf7d-e9eeb7c627a6 --> — A cycle's own type (its `data.type`, not the envelope
  `type`) is not an identity field and MUST be correctable in the corrections editor. The system
  validates the new type against the configured cycle types (`unknown_cycle_type`) and fills the
  cycle's title, and nothing else, from the configuration at correction time. The client MUST NOT
  supply the title, and replay MUST NOT read the configuration. _(Operator ruling, 2026-10-08:
  "fixing a type can be done in the admin.")_
- **`INV-CORR-6`** <!-- uuid: 7eb56482-f21e-46d8-9335-57e4eee325c4 --> — A replacement value that breaks the event's own shape (a
  negative `minutes`, a blank reason, a text that is too long or not valid UTF-8) MUST be refused as
  `invalid_request`, with the field path in the message, and not as `invalid_correction`, which is
  kept for the identity, target-type and lone-retraction rules.
- **`INV-CORR-7`** <!-- uuid: f0fec602-a5ed-477f-90fb-1dd3c1f09756 --> — A correction MAY change an event's `effective_at` and its
  non-identity fields, and is judged by the timeline rules only: no lower bound applies to a
  correction either. A correction that would make the timeline impossible (for example a pause
  moved before its cycle's start) MUST be refused with the specific code of the condition found, the
  same code the equivalent command would get; and a correction whose `effective_at` is in the future
  is refused as `future_effective_at`.

### Retracting

- **`INV-CORR-8`** <!-- uuid: 95816d45-daca-425a-be09-948ad7535d86 --> — A retraction MAY target any event except a `batch.committed`,
  including a correction or another retraction, so every correction is undoable. Retracting a
  correction restores the earlier value; retracting a retraction restores the retracted event (an
  undo of an undo).
- **`INV-CORR-9`** <!-- uuid: 7ecdbbef-c307-47cc-9e35-9edd51dd4273 --> — A retraction names exactly one of `target` (an event id) or
  `target_batch` (a batch id), and `batch` MUST mean membership of a batch group and nothing else. A
  retraction event carries no `batch`, so it can itself be retracted alone. _(Operator ruling,
  2026-10-08: "d7, yes rename.")_
- **`INV-CORR-10`** <!-- uuid: fb62287a-6f3d-4bee-b8fa-4eb7251acdde --> — A whole batch MUST be retracted by one `event.retracted`
  naming it as `target_batch`; that retraction is not itself a batch. An event that is a member of a
  batch MUST be retracted only through its batch, never alone, and the structural refusal
  (`invalid_correction`) holds even when its batch is already retracted. A lone `task.materialized`
  or `period.changed` MUST NOT be retracted, and neither MUST any retraction that would leave a task
  whose period has no live `period.changed` (`task_without_period`).
- **`INV-CORR-11`** <!-- uuid: 6b4ae098-8396-45e6-b333-0e89207bb01d --> — A batch retraction MUST be refused as `batch_has_dependents`
  when a later live event references an entity the batch created (for example a task completed in the
  new period), and the refusal MUST name the dependent events, so a client can link them in the
  editor and state the limit next to every undo. A late completion or skip of a task the batch marked
  `missed` is not a dependent. A switch or a break creates no entity and has none.
- **`INV-CORR-12`** <!-- uuid: 43be12ec-7a2a-4213-8de6-29815ee7517c --> — A retraction of a `cycle.started` that has later live events
  for its cycle, or that a later `cycle.started` names in `interrupts`, MUST be refused as
  `cycle_has_dependents`, listing the dependent events.
- **`INV-CORR-13`** <!-- uuid: 335c72af-52e4-41fd-9f8b-05717c792750 --> — A retraction of a target or a batch that is already
  retracted MUST be a no-op success (`changed: false`, a note naming the retraction event). Once that
  retraction is itself retracted the target is live again and a new retraction applies.
- **`INV-CORR-14`** <!-- uuid: 1a19be11-cd92-4bac-a252-f8e116050aef --> — Retracting a switch batch MUST return the focus to the first
  cycle; where later events make that impossible, validation refuses with the specific code of the
  condition found. An undo is always available for a correction.

### Named fixes

- **`INV-CORR-15`** <!-- uuid: 61c13383-6e1e-4152-9e82-87daf187e15d --> — Back-filling a break (a cycle id, a `from` and a `to`) MUST
  append a pause at `from` and a resume at `to` as one batch. `from` MUST be before `to`
  (`invalid_request`) and neither MAY be in the future. `to` MUST be strictly earlier than the
  cycle's stop if it has one, otherwise the request is `break_ends_at_stop`, whose message points at
  "End at"; a break that overlaps an existing pause of the cycle is `cycle_segments_overlap`. A
  break that a stored log carries on a stopped cycle replays as `cycle_stopped`, naming the batch.
- **`INV-CORR-16`** <!-- uuid: 989218f6-4d66-43bf-92bf-d588f1ff68fd --> — Fixing a cycle's type, a skip reason or an `effective_at` is
  offered in a corrections editor that shows the original and the corrected view, and a client SHOULD
  show a before-and-after timeline, with the zone and date, before the operator confirms a
  timeline-changing fix.
- **`INV-CORR-17`** <!-- uuid: 4c3ccc4b-ba34-47b5-bd35-fa2488a5484f --> — "End at" MUST stop a cycle at an earlier time the operator
  names. It is an ordinary stop with an `effective_at`, validated by replaying the whole log.
- **`INV-CORR-18`** <!-- uuid: c4598665-5128-40cc-bfb3-b8a3610d814e --> — "End at" on an already stopped cycle MUST succeed as a no-op
  when its instant is later than the existing stop, and MUST be refused as `cycle_stopped` when it is
  earlier (the cycle was already stopped at the later instant; the message says to correct that
  stop's time). "End at" at the instant of the cycle's start is `stop_not_after_start`, at the
  instant of its last resume `empty_running_segment`, and before its start `cycle_event_before_start`.
- **`INV-CORR-19`** <!-- uuid: e79f13e7-eb88-4f1e-ada7-939772f3847d --> — A correction, a retraction, a break or an "End at" MUST be
  validated by candidate replay before anything is appended, so none can leave the log impossible; the
  refusal carries the specific code, a message naming the entity, the instants and the stored events,
  and nothing is written.
