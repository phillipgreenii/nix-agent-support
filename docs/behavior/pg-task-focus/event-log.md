# pg-task-focus — the event log

Everything `pg-task-focus` knows is a line in one durable, append-only log. This doc owns the log's
contract: what an event is, what is written and when, how a crash is survived, how a request is
validated and answered, what a repeated request does, and what the operator sees when the log can
no longer be written. The corrections that fix mistakes are in [`corrections.md`](corrections.md),
the instants and zones in [`time-and-zones.md`](time-and-zones.md).

## Stories

- **`STORY-LOG-TRUST`** <!-- uuid: f02b041d-f1ae-4c7b-b48c-d2b2d59dc351 --> — As the operator, I want everything I record kept as an
  immutable event, so the history I read later is the history that happened.
  _(→ `JOURNEY-LOG-CRASH`; `INV-LOG-1`, `INV-LOG-2`, `INV-LOG-3`, `INV-LOG-4`, `INV-LOG-5`,
  `INV-LOG-6`, `INV-LOG-7`, `INV-LOG-8`, `INV-LOG-29`.)_
- **`STORY-LOG-CRASH`** <!-- uuid: cc4e0d2d-c407-4365-9798-b136bf8b7ea2 --> — As the operator, I want a crash or a power loss to
  lose nothing I was told succeeded and to leave nothing half-written, so I never repair the log by
  hand. _(→ `JOURNEY-LOG-CRASH`; `INV-LOG-9`, `INV-LOG-10`, `INV-LOG-11`, `INV-LOG-30`.)_
- **`STORY-LOG-REJECT`** <!-- uuid: c31ed7de-4f78-4654-86bd-b908136991ab --> — As the operator, I want a request that cannot be
  honored to be refused before anything is written, with a message that says what is wrong, so I
  can fix the request instead of guessing. _(→ `JOURNEY-LOG-REJECTED`; `INV-LOG-12`, `INV-LOG-13`,
  `INV-LOG-14`, `INV-LOG-15`, `INV-LOG-16`, `INV-LOG-27`, `INV-LOG-28`.)_
- **`STORY-LOG-RETRY`** <!-- uuid: 36cd217f-652a-484a-adb4-ba2b13f32acc --> — As a client acting for the operator, I want to repeat a
  request whose outcome I never learned and get the original answer, so a lost response never
  records the same thing twice. _(→ `JOURNEY-LOG-RETRY`; `INV-LOG-17`, `INV-LOG-18`, `INV-LOG-19`,
  `INV-LOG-26`.)_
- **`STORY-LOG-READONLY`** <!-- uuid: 4d729da6-b146-4da5-bae6-62104f86874c --> — As the operator, I want it to be unmistakable, in
  every place I look, when the log can no longer be written, so I never believe a change was saved
  when it was not. _(→ `JOURNEY-LOG-READONLY`; `INV-LOG-21`, `INV-LOG-22`, `INV-LOG-23`,
  `INV-LOG-24`, `INV-LOG-25`.)_
- **`STORY-LOG-ONEWRITER`** <!-- uuid: 403c543b-e069-42d9-ab00-e2ddd9d66ba6 --> — As the operator, I want exactly one running
  instance to be able to write my log, so two instances can never interleave or corrupt it.
  _(→ `JOURNEY-LOG-CRASH`; `INV-LOG-20`.)_

## Journeys

### `JOURNEY-LOG-CRASH` — a crash during a write, then a restart <!-- uuid: 4bcbc81e-8dee-43e5-b0b5-aa6ad87c11d3 -->

**Primary actor:** `ACTOR-OPERATOR`. **Other actors:** `ACTOR-HOST`, `ACTOR-CLIENT`.
_Requires:_ `INV-LOG-9`, `INV-LOG-10`, `INV-LOG-11`, `INV-LOG-20`, `INV-LOG-29`, `INV-LOG-30`.
_Includes:_ none.

1. The operator completes a task and is told it succeeded; the event is durable (`INV-LOG-29`).
2. The next request is being written when the machine loses power; the log now ends in a partial
   line, or in the first events of a batch with no commit marker.
3. The operator restarts the service. It takes the exclusive write claim (`INV-LOG-20`), finds the
   unacknowledged tail, copies it aside, trims the log to the end of the last committed record and
   only then accepts requests (`INV-LOG-9`, `INV-LOG-10`). The recovery is reported, not silent.
4. Everything the operator was told succeeded is present; nothing that was not acknowledged is.

Extensions:

- 2a. The damage is not at the tail (a partial line or an uncommitted batch followed by more
  events). The service refuses to start and says which line is damaged (`INV-LOG-11`); the offline
  check names the same line without taking the write claim (`INV-LOG-30`).
- 2b. The log carries an event version the service does not know. It refuses to start; this is
  never treated as a torn tail (`INV-LOG-2`).

### `JOURNEY-LOG-REJECTED` — a request that cannot be honored <!-- uuid: f863a320-6f74-4918-b29f-9f52b6c0d061 -->

**Primary actor:** `ACTOR-OPERATOR`. **Other actors:** `ACTOR-CLIENT`.
_Requires:_ `INV-LOG-12`, `INV-LOG-13`, `INV-LOG-14`, `INV-LOG-15`, `INV-LOG-16`, `INV-LOG-27`,
`INV-LOG-28`. _Includes:_ none.

1. The operator asks to pause a cycle at a time that would make its running segment empty.
2. The system builds the log the request would produce and replays the whole of it (`INV-LOG-12`).
3. The timeline is impossible, so nothing is written. The answer carries the specific code
   `empty_running_segment` and one sentence naming the cycle, the instants in UTC and in the day
   period's zone, and the stored events involved (`INV-LOG-13`, `INV-LOG-14`, `INV-LOG-15`).

Extensions:

- 1a. The operator names no `effective_at` and the machine clock was stepped back behind the cycle's
  newest event. The answer is `clock_behind_log`, naming both instants and saying to supply an
  `effective_at` (`INV-LOG-28`).
- 1b. The request carries text that is not valid UTF-8, or too large, or a blank reason. The answer
  is `invalid_request`, before anything is built (`INV-LOG-27`).

### `JOURNEY-LOG-RETRY` — an answer that never arrived <!-- uuid: 456e1d4e-0305-4ccf-b9ae-1da217c10c1c -->

**Primary actor:** `ACTOR-CLIENT`. **Other actors:** `ACTOR-OPERATOR`.
_Requires:_ `INV-LOG-17`, `INV-LOG-18`, `INV-LOG-19`, `INV-LOG-24`, `INV-LOG-26`.
_Includes:_ `JOURNEY-LOG-READONLY`.

1. A client sends a request carrying an `id` and receives an "outcome unknown" answer.
2. It sends the same request with the same `id`.
3. If the first attempt had been recorded, the original result comes back, marked as a replay, and
   nothing new is written (`INV-LOG-17`); if it had not, the request is handled as new.

Extensions:

- 2a. The service restarted in between. The result is still found, because the record of request
  ids is rebuilt from the log (`INV-LOG-17`).
- 2b. The first attempt was a no-op. Its answer is remembered only for the life of the process
  (`INV-LOG-18`).
- 2c. The store went read-only after the first attempt was made durable. The retry still returns the
  original result (`INV-LOG-24`).
- 2d. The second request reuses the `id` with different content. It is refused as `id_conflict`
  (`INV-LOG-17`).

### `JOURNEY-LOG-READONLY` — the log can no longer be written <!-- uuid: f1c6a717-35df-4aee-8037-8a9109a674ff -->

**Primary actor:** `ACTOR-OPERATOR`. **Other actors:** `ACTOR-HOST`, `ACTOR-CLIENT`.
_Requires:_ `INV-LOG-21`, `INV-LOG-22`, `INV-LOG-23`, `INV-LOG-24`, `INV-LOG-25`.
_Includes:_ none.

1. The disk refuses to make an append durable, and the file cannot be restored to its earlier end.
2. The store enters read-only mode and remembers why and since when (`INV-LOG-21`).
3. Every client the operator uses shows the READ-ONLY sentence of `INV-LOG-22`, including a client
   that is already open, which is told at once.
4. Reads keep working; every change is refused with the same sentence (`INV-LOG-25`).
5. The operator restarts the service. Recovery runs and the store is writable again; nothing else
   clears the mode (`INV-LOG-23`).

## The event log interface

### `INTF-LOG` — the durable log <!-- uuid: e35017ae-13cf-49f3-aa5b-d2b8834380fb -->

**Counterparty:** `ACTOR-HOST` (kind: actor;
essential participant: without a durable file there is no product). **What crosses:** one line of
JSON per event, appended; the whole file read at startup and by the offline check (which takes the
data directory or the log file). The file is
`events.jsonl` in the service's data directory (the user's data home, in a `pg-task-focus`
directory; the host's data-home convention decides where). **What must hold:** `INV-LOG-1`,
`INV-LOG-2`, `INV-LOG-3`, `INV-LOG-9`, `INV-LOG-10`, `INV-LOG-11`, `INV-LOG-20`, `INV-LOG-29`.

Each line is one event with these fields:

| Field          | Meaning                                                                                                                                                     |
| -------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `v`            | The event schema version. Only version 1 exists.                                                                                                            |
| `id`           | A unique ULID. A client may supply it; the order of the log is append order, never `id` order.                                                              |
| `at`           | When the system recorded the event. Set by the system, never by a client.                                                                                   |
| `effective_at` | When the thing happened. Defaults to `at`; a client may supply it, within the future-skew limit.                                                            |
| `req_hash`     | Present when the request that produced the event carried an `id`: a hash of that request's client-supplied content, so the request can be recognized again. |
| `type`         | One of the closed set of event types below.                                                                                                                 |
| `data`         | The payload of that type.                                                                                                                                   |

The closed set of event types (`INV-LOG-7`):

| Type                | What it records and what its payload names                                                                                           |
| ------------------- | ------------------------------------------------------------------------------------------------------------------------------------ |
| `period.changed`    | A day, week or sprint began: kind, civil start (and end for a week or sprint), the zone in force, a label, its batch                 |
| `profile.changed`   | The active profile changed: the profile, its batch                                                                                   |
| `task.materialized` | A task definition became an instance for a period: id, definition, cadence, period, title, group, link, due instant, due rule, batch |
| `task.completed`    | The task was done: task id                                                                                                           |
| `task.skipped`      | The task was deliberately not done: task id, a non-blank reason, optionally its batch                                                |
| `task.missed`       | The system marked an open task as not acted on at a rollover: task id, batch                                                         |
| `task.withdrawn`    | The task left the active profile: task id, batch                                                                                     |
| `task.reinstated`   | A withdrawn task is back in the active profile: task id, batch                                                                       |
| `cycle.started`     | A cycle began: cycle id, type, title, planned minutes, and the cycle it interrupted if any                                           |
| `cycle.paused`      | A cycle stopped running without ending: cycle id, optionally its batch                                                               |
| `cycle.resumed`     | A paused cycle runs again: cycle id, optionally its batch                                                                            |
| `cycle.boosted`     | Minutes were added to a cycle: cycle id, minutes                                                                                     |
| `cycle.stopped`     | A cycle ended: cycle id                                                                                                              |
| `cycle.annotated`   | The cycle's whole note and key/value list: cycle id, note, pairs                                                                     |
| `event.corrected`   | Replacement values for an earlier event: the target, the replacement fields, an optional reason                                      |
| `event.retracted`   | An earlier event or a whole batch no longer counts: exactly one of `target` or `target_batch`, an optional reason; never a `batch`   |
| `batch.committed`   | The commit marker that closes a batch: the batch id                                                                                  |

## The request interface

### `INTF-REQUEST` — requests in, results and refusals out <!-- uuid: 6b2fe3ca-9a87-47f2-87b2-c5b279968195 -->

What a client sends to change the log and what it gets back. **Counterparty:** `ACTOR-CLIENT` (kind: actor; essential participant: nothing else changes the
log). **What crosses in:** a command (complete or skip a task, change periods or profile, start,
pause, resume, boost, stop, switch or annotate a cycle, back-fill a break, correct, retract), with
an optional request `id`, an optional `effective_at`, and for a preview a dry-run flag and an
expected version. **What crosses out:** on success, whether anything changed, the ids of the events
appended, the batch id, a one-sentence note when nothing changed, and whether this is a replay of
an earlier answer; on refusal, one code from the closed catalog below with a message and
structured details (`INV-LOG-13`, `INV-LOG-14`). **What must hold:** `INV-LOG-12` to `INV-LOG-19`,
`INV-LOG-24`, `INV-LOG-25`, `INV-LOG-26`, `INV-LOG-27`, `INV-LOG-28`, `INV-LOG-29`.

The catalog of refusal codes is closed. The categories are the meaning a client maps to its own
transport: **malformed** (the request is malformed or incomplete), **not found**, **conflict** (the
request conflicts with the present state or with other events), **unacceptable** (the request is
well formed but its instant or content cannot be accepted for its own target) and **unavailable**
(the store cannot take the request).

| Category     | Code                                | Condition                                                                                                                                                                                                                          |
| ------------ | ----------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| malformed    | `invalid_request`                   | A malformed or incomplete body; a value out of range; a blank skip or override reason; a bad key; text too long or not valid UTF-8; an event that would be too long; a correction whose replacement values break the event's shape |
| malformed    | `invalid_zone`                      | A zone name the zone rules reject                                                                                                                                                                                                  |
| malformed    | `unknown_profile`                   | The profile is not defined                                                                                                                                                                                                         |
| malformed    | `unknown_cycle_type`                | The cycle type is not defined                                                                                                                                                                                                      |
| malformed    | `reserved_key`                      | The key `cycle_type` was supplied                                                                                                                                                                                                  |
| malformed    | `cycle_ambiguous`                   | No cycle was named and more than one cycle is a candidate; the details list each candidate's id and title                                                                                                                          |
| not found    | `unknown_task`                      | No such task                                                                                                                                                                                                                       |
| not found    | `unknown_cycle`                     | No such cycle                                                                                                                                                                                                                      |
| not found    | `unknown_event`                     | No such event, or a `target_batch` that matches no batch                                                                                                                                                                           |
| conflict     | `no_running_cycle`                  | No cycle was named and none is a candidate, or a switch has no cycle running at its instant                                                                                                                                        |
| conflict     | `cycle_stopped`                     | The change applies to a cycle that is stopped at the relevant instant                                                                                                                                                              |
| conflict     | `another_cycle_running`             | The change would leave two cycles running at once; the details name both                                                                                                                                                           |
| conflict     | `interrupted_cycle_not_running`     | A start that interrupts a cycle that is not running at that instant                                                                                                                                                                |
| conflict     | `cycle_active`                      | A period change while any cycle is running or paused now; the details name the cycles                                                                                                                                              |
| conflict     | `cycle_segments_overlap`            | Two running segments, or a pause and a resume, of one cycle would overlap                                                                                                                                                          |
| conflict     | `break_ends_at_stop`                | A back-filled break would start or end at or after the instant the cycle stopped; the message points at "End at"                                                                                                                   |
| conflict     | `clock_behind_log`                  | No `effective_at` was given and the clock reads earlier than the newest recorded event of the entity                                                                                                                               |
| conflict     | `task_already_resolved`             | The task is already completed or skipped; the message names the resolving events                                                                                                                                                   |
| conflict     | `task_withdrawn`                    | A completion or skip of a withdrawn task, unless its `effective_at` precedes the withdrawal                                                                                                                                        |
| conflict     | `task_not_withdrawn`                | A reinstatement of a task that is not withdrawn                                                                                                                                                                                    |
| conflict     | `task_materialized_twice`           | A second live materialization of one task id                                                                                                                                                                                       |
| conflict     | `task_without_period`               | A change would leave a task whose period has no live `period.changed`                                                                                                                                                              |
| conflict     | `task_before_profile`               | A change would leave a task materialized before the first live `profile.changed`                                                                                                                                                   |
| conflict     | `period_unchanged`                  | The new `start` is not later than the current `start` of that kind                                                                                                                                                                 |
| conflict     | `id_conflict`                       | The request `id` was used with different content                                                                                                                                                                                   |
| conflict     | `stale_preview`                     | The expected version differs from the current version                                                                                                                                                                              |
| conflict     | `batch_has_dependents`              | A later live event references an entity the batch created; the details list the dependents                                                                                                                                         |
| conflict     | `cycle_has_dependents`              | A retraction of a `cycle.started` that has later live events for its cycle, or that another cycle's `interrupts` names; the details list them                                                                                      |
| unacceptable | `invalid_correction`                | A correction or retraction breaks the correction rules (identity field, forbidden target type, lone retraction)                                                                                                                    |
| unacceptable | `future_effective_at`               | The `effective_at` is later than now plus the future-skew limit                                                                                                                                                                    |
| unacceptable | `stop_not_after_start`              | A stop effective at the very instant of the cycle's first start                                                                                                                                                                    |
| unacceptable | `empty_running_segment`             | A pause, interrupt, switch or stop effective at or before the start or resume that opened the running segment                                                                                                                      |
| unacceptable | `cycle_event_before_start`          | A pause, resume, boost, stop or annotation that sorts before its cycle's start, or an event of a cycle that has no live start                                                                                                      |
| unacceptable | `resolution_before_materialization` | A completion or skip effective before the task's materialization                                                                                                                                                                   |
| unacceptable | `period_out_of_order`               | A period change that sorts, by effective instant, before an existing change of the same kind whose `start` is lower than its own                                                                                                   |
| unavailable  | `store_unavailable`                 | The request cannot be recorded; the message says whether the store is read-only and whether the outcome is unknown                                                                                                                 |

A client's own readiness check (the service has not finished starting) is a further code owned by
the service that hosts the library, not by the log.

## Invariants

### The record

- **`INV-LOG-1`** <!-- uuid: 213b819e-082b-489c-9910-886bed8ebb8d --> — The log MUST be append-only: no component rewrites or deletes
  a line. The only truncations are crash recovery (`INV-LOG-9`, `INV-LOG-10`) and the rollback of a
  failed append (`INV-LOG-21`), each back to the end of the last committed record.
- **`INV-LOG-2`** <!-- uuid: fd98b33c-01f4-4e0c-9a0f-090cc7dc9289 --> — The service MUST refuse to start on an event version it does
  not know, and MUST NOT treat such a line as a torn tail. A `v` that is not a JSON number, or is
  absent, is not a recognised version: that line is an ordinary decode failure. A line whose
  top-level `v` is any JSON number other than the literal `1` (`1.0` and `1e0` included) is an
  unknown version, even if `v` appears more than once, so `"v":2` beside `"v":1` is an unknown
  version whichever comes last. Any other duplicate key is a decode error. An event with a field
  the version does not define is not a valid event.
- **`INV-LOG-3`** <!-- uuid: f5d2ba85-c500-41f0-85f5-7dd9fb7af843 --> — Every stored instant MUST be an RFC 3339 timestamp in UTC with
  millisecond precision (`2026-10-07T13:30:00.000Z`). An instant a client supplies is truncated to
  milliseconds before it is validated or stored.
- **`INV-LOG-4`** <!-- uuid: f69b8b04-0581-4552-a04a-f002a60380e4 --> — `at` MUST be set by the system and never by a client.
  `effective_at` MUST default to `at`, and a client MAY supply it, but it MUST NOT be later than the
  present plus the future-skew limit (default 60 seconds, configurable); otherwise the request is
  refused as `future_effective_at`.
- **`INV-LOG-5`** <!-- uuid: 6e225da1-b160-4dab-8125-4045981d9e51 --> — An entity's events (a cycle's by cycle id, a task's by task
  id) MUST be ordered by `effective_at`, then by log position, never by event `id`. Corrections and
  retractions apply by log position. A `task.missed` marker is exempt from the ordering checks,
  because a late completion supersedes it whatever the order. A result that would need a different
  order for events at one instant MUST be refused with the code of that condition.
- **`INV-LOG-6`** <!-- uuid: 62c858d6-953d-44e7-b430-3a232750fb10 --> — A task instance and a cycle MUST keep the snapshot taken when
  it was created (a task's title, group, link, due instant and due rule; a cycle's type title and
  planned minutes). Replay MUST NOT consult the configuration, so a configuration edit never
  rewrites history.
- **`INV-LOG-7`** <!-- uuid: 6020b37f-8cde-459e-b3e0-75bb75cda0fe --> — The log MUST contain only the seventeen event types of the
  closed set above. On every type that carries one, `batch` MUST mean membership of a batch group
  and nothing else; `event.retracted` never carries one, and `batch.committed` carries the id of the
  batch it closes.
- **`INV-LOG-8`** <!-- uuid: fb512c0c-44f6-409e-b7ea-628141295088 --> — A task id MUST be derived, never random: its cadence prefix
  (`day`, `week` or `sprint`), the civil start date of its period and its definition name, for
  example `day:2026-10-07:post-plan`. The same period and definition therefore always name the same
  task, and a second live materialization of one id is refused as `task_materialized_twice`; a
  withdrawn task returns by `task.reinstated`, never by a second materialization.
- **`INV-LOG-29`** <!-- uuid: f2a0a738-0b62-4fb3-9c42-69f432153f0c --> — A change MUST be acknowledged only after its events are
  durable, and the in-memory state MUST never be ahead of the log: the order is validate, append and
  make durable, adopt the validated state, publish the new version, answer.

### Batches and crash recovery

- **`INV-LOG-9`** <!-- uuid: 8de58791-2e9b-45a5-9ba8-893815660108 --> — A mutation that appends more than one event (a period or
  profile change, a rollover, a break back-fill, a cycle switch) MUST give the events one batch id
  and end them with a `batch.committed` event, all written and made durable together. A final line
  that is torn (no terminating newline) or that ends in a newline but does not parse as a valid
  event (a complete line of a known version that fails its shape included), and the trailing events
  of a batch with no `batch.committed`, were never acknowledged to any client and are recovered at
  the next start (`INV-LOG-10`). _(Operator ruling, 2026-10-08: a final line that ends in a newline
  but does not parse is a torn tail, not a refusal.)_
- **`INV-LOG-10`** <!-- uuid: 6e39c826-d8e0-4f8a-ae1c-6d8be9137ae9 --> — Recovery MUST copy the unacknowledged bytes to a sidecar
  file and make it durable, then truncate the log to the end of the last committed record and make
  that durable, and only then accept requests. The sidecar is named
  `events.jsonl.recovered-<UTC yyyymmddThhmmssZ>-<n>`, MUST NOT overwrite an earlier sidecar, and
  MUST keep the bytes, so nothing is lost. The sidecar's directory entry is made durable by a
  directory fsync before the log is truncated. A recovery MUST be reported (logged and counted), never
  silent, and a crash in the middle of recovery MUST be survivable by recovering again.
- **`INV-LOG-11`** <!-- uuid: 335e87a5-ee00-46cb-8f42-40502d2a4626 --> — A torn or unparsable line, or an uncommitted batch, anywhere
  but the tail MUST be treated as corruption: the service refuses to start and names the cause and
  the 1-based line number. Corruption also includes an event line longer than the size limit
  (`INV-LOG-27`), an interleaved batch, a `batch.committed` with no open batch, a reused batch id,
  a repeated event id (ids are unique) and a blank line before the final line; a blank final line
  is a torn tail. The size limit is checked before the version, so an over-limit line is corruption
  even when it carries an unknown `v`. CRLF line endings are accepted on read, because a trailing
  carriage return is JSON white space; the library never writes them.

### Validation and the expressive error

- **`INV-LOG-12`** <!-- uuid: 67088f81-5f68-4d16-ac7c-4f6b4e08eb8b --> — Every mutation, every correction and retraction and every
  client-supplied `effective_at` MUST be validated by replaying the whole log with the change
  applied, before anything is appended. An impossible timeline (a stop at the instant of a start, a
  cycle event that sorts before its start, a running segment of no length, overlapping segments, two
  running cycles, a resume of a stopped cycle, a completion earlier than its task's
  materialization, a task completed or skipped twice) MUST be refused, and a refused request MUST
  append nothing.
- **`INV-LOG-13`** <!-- uuid: 66e3c379-75d4-4b8d-8651-2619407e75ac --> — Errors MUST be expressive. Every rejection carries one
  specific code from the closed catalog, named for its own problem; there is no generic catch-all
  code, and in particular no "invalid timeline". The same condition MUST get the same code whether
  the command finds it from the present state or candidate replay finds it by replaying a changed
  log. _(Operator ruling, 2026-10-08: "the error should be expressive of the problem . invalid_timeline
  is not helpful to understanding the problem.")_
- **`INV-LOG-14`** <!-- uuid: 48e52615-01ee-43f4-addc-30ea81a29e8f --> — Every rejection MUST carry a plain-sentence message naming
  the entity, the instants and the ids of the stored events involved, and structured details holding
  the same facts (`entity`, `events`, `instants`; the cycles for `cycle_active`, `cycle_ambiguous`
  and `another_cycle_running`; the store health for `store_unavailable`). A message MUST cite only
  the ids of events that are stored: the event a request would add is "the new event" and a cycle it
  would create is "the new cycle".
- **`INV-LOG-15`** <!-- uuid: 8db9ea3c-6d7c-4b8b-b30b-5735b2b05241 --> — Every instant in a message MUST be given in UTC (RFC 3339
  with `Z`) followed in parentheses by the same instant in the active day period's zone with its
  identifier once a day period exists (`2026-10-07T14:00:00Z (10:00 America/New_York)`), so no zone
  is ever implicit.
- **`INV-LOG-16`** <!-- uuid: 891f6f47-6f28-455a-8fa0-82f44d727904 --> — A rejection's category MUST keep its ordinary meaning:
  malformed or incomplete (including an ambiguous cycle), not found, a conflict with the present
  state or with other events, unacceptable instant or content for its own target, and an unavailable
  store. A stop earlier than an existing stop is a conflict (`cycle_stopped`) because it conflicts
  with an existing event, not because its own content is wrong.
- **`INV-LOG-27`** <!-- uuid: 3b1a6d7e-8bf2-4c95-bc91-e72d0d494e5e --> — Free text and every key or value a client supplies MUST be
  valid UTF-8 and within the size limit, and an event whose encoded form exceeds 256 KiB (262144
  bytes) MUST be refused as `invalid_request` before it is written, never as a store failure. Text
  that is not valid UTF-8 MUST be rejected, never rewritten: a stored note is exactly the text the
  operator wrote. A skip or override reason MUST be non-blank (empty or only whitespace is
  `invalid_request`), and surrounding whitespace is trimmed. A 100 KiB line already in a log MUST
  still replay. _(Operator ruling, 2026-10-08: a blank reason includes `""` and `"  "`.)_
- **`INV-LOG-28`** <!-- uuid: 8182a677-bc64-4cf8-b174-e41ad2b089be --> — When no `effective_at` is given and the clock reads earlier
  than the newest recorded event of the entity acted on, the request MUST be refused as
  `clock_behind_log`, with a message naming both instants and saying to supply an `effective_at`; it
  MUST NOT corrupt the log.

### Repeated requests

- **`INV-LOG-17`** <!-- uuid: 6eac84be-94d0-4cd2-93a2-9eaa2cf6d061 --> — A request that carries an `id` MUST be recognized again: the
  lookup runs before validation and compares only the request's content hash. The same `id` with the
  same content returns the original result, marked as a replay, and appends nothing; the same `id`
  with different content is refused as `id_conflict`; an `id` that collides with a stored event that
  carries no hash is also `id_conflict`. For a batch the request `id` is the batch id. The record of
  request ids MUST be rebuilt from the log after a restart. A request with no `id` has no
  idempotency and is given generated ids. A defaulted `effective_at` stays out of the content hash,
  so a retry made later hashes the same.
- **`INV-LOG-18`** <!-- uuid: 6077c2ac-0d6e-414a-afa3-bdadcdf6518b --> — A no-op (see `INV-CYCLE-9`) appends nothing, so it stores no
  hash. The service MUST remember the result of a no-op in a bounded, least-recently-used cache for
  the life of the process: a retry with the same `id` and content returns the original
  `changed: false` result, different content under that `id` is `id_conflict`, and after a restart
  the request is evaluated again against the present state.
- **`INV-LOG-19`** <!-- uuid: 867ce803-0d31-4112-a1e3-af56e3804037 --> — The state version MUST be a pair of the number of lines in
  the log and a configuration generation that never recurs across a restart. Clients compare
  versions only for inequality and refetch on any change; an expected version and `stale_preview`
  compare both members.
- **`INV-LOG-26`** <!-- uuid: fcbbd270-b808-4043-98c8-1da4380f8e2c --> — A dry run MUST return exactly what the real request would
  affect at the returned state version and MUST NOT append anything. It MUST neither read nor write
  the record of request ids or the no-op cache, and it is not refused while the store is read-only,
  so a dry run carrying the `id` of a durable request returns a preview, not that request's result.

### One writer, and the log that cannot be written

- **`INV-LOG-20`** <!-- uuid: cdee6c3e-98e6-489b-ae81-92caaadfdfdf --> — Exactly one service MUST be able to write a given data
  directory: it takes an exclusive claim on the directory at startup, held for the life of the
  process, and refuses to start without it, and a claim left behind by a process that is gone does
  not block. The offline check reads the log without the lock
  and without modifying it.
- **`INV-LOG-21`** <!-- uuid: 54de231d-8aff-48ac-83ca-07be925c3911 --> — On any error from a write or a sync, the system MUST truncate
  the log back to the offset before the failed append and make that durable, so no partial or
  unacknowledged bytes remain. If the truncation fails, or the sync of the append itself failed (the
  file's state is then unknown), or the rollback could not be synced, the store MUST enter read-only
  mode. A failed write whose rollback succeeds MUST NOT enter read-only mode, and its answer says the
  outcome is unknown and to retry with the same `id`.
- **`INV-LOG-22`** <!-- uuid: ee5242f7-d97e-41d2-b56f-2bb5c734a402 --> — Read-only mode MUST be obvious in every client, present and
  future: the web UI, the CLI status, the menu bar, the connector's attention feed and any terminal
  UI added later. The store keeps the cause and the instant it began; the state a client reads
  carries them (`state` `ok` or `read_only`, `reason`, `since`); the reason is one of a closed set
  of plain sentences naming the failed step (the append write failed and the rollback could not
  truncate the log; the append write failed and the rollback could not be synced; the append fsync
  failed; the new state could not be adopted after a durable append), never free text; and the
  sentence every client shows is `READ-ONLY: <reason>. Restart pg-task-focus to recover`. A client
  that is already open MUST be told when the mode begins, not at its next refresh. _(Operator
  ruling, 2026-10-08: the restart-only recovery is accepted, and the mode must be visible in the web
  UI, a terminal UI, the status line and the menu bar, among others.)_
- **`INV-LOG-23`** <!-- uuid: ae8f0cac-9709-41a4-afaa-f29f17e6d8a3 --> — In read-only mode reads MUST keep working, every mutation
  MUST be refused, and only a restart that has run crash recovery MUST clear the mode; nothing else,
  retries included, does. The first cause and its instant are kept; a later failure never changes
  them. After an adoption failure (the change is durable but the validated state could not be
  adopted) the in-memory model MUST be treated as behind the log until the restart.
- **`INV-LOG-24`** <!-- uuid: e5ba927f-1c5c-4047-99ff-0079bd5deb1c --> — The record of request ids is consulted before the read-only
  gate, and a request is recorded as soon as its events are durable and before the new state is
  adopted. A retry of a request whose events are durable MUST therefore return its original result,
  marked as a replay, even while the store is read-only and even after an adoption failure.
- **`INV-LOG-25`** <!-- uuid: eaba9ca1-12b3-4757-bfe1-a0d6339fbe57 --> — A `store_unavailable` refusal MUST say which of four things
  happened. (1) The store was already read-only: the message is the READ-ONLY sentence, nothing was
  attempted, and even a new request that would have been a no-op is refused. (2) An append failed
  and was rolled back, so the store stayed writable: "the outcome is unknown: retry with the same
  id". (3) The request's own append made the store read-only: the READ-ONLY sentence, then "The
  outcome is unknown; if the request carried an id, retry with it after the restart". (4) The new
  state could not be adopted after a durable append: the READ-ONLY sentence, then "The change is
  stored; if the request carried an id, retry with it after the restart to receive the result".
  Only shapes (2) and (3) mean the outcome is unknown.
- **`INV-LOG-30`** <!-- uuid: 7479ece7-0bb6-4b3c-968c-2035fe6c13ba --> — An offline check MUST report a log's line count, batches, the
  recoveries it would perform, the line of any corruption and, after replay, any impossible timeline
  with its specific code and message, without modifying the log and without taking the write lock,
  even while a service has the log open. The check takes the data directory or the log file itself.
