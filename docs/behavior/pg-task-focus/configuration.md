# pg-task-focus — configuration

`pg-task-focus` is generic: everything specific to one routine (the task list, the cycle types, the
links, the group names, the sounds) is configuration supplied by whoever deploys it. This doc owns
the configuration's contract: what it contains, what is rejected and how, what a reload may and may
not do, and how a configuration edit relates to history that already exists. The zone rules a
configuration's `tz` follows are in [`time-and-zones.md`](time-and-zones.md); the sounds are used in
[`cycles-and-timer.md`](cycles-and-timer.md).

## Stories

- **`STORY-CONF-DEFINE`** <!-- uuid: 04996b75-2e8f-4d69-85ef-04feb04ec3dc --> — As the configurer, I want to describe my routine once
  (tasks with due rules, cycle types with durations and alert sounds, profiles that select among them)
  and have the product enforce it, so the operator never retypes the routine.
  _(→ `JOURNEY-CONF-EDIT`; `INV-CONF-1`, `INV-CONF-2`, `INV-CONF-3`, `INV-CONF-4`, `INV-CONF-5`,
  `INV-CONF-6`, `INV-CONF-7`, `INV-CONF-9`, `INV-CONF-18`.)_
- **`STORY-CONF-SAFE`** <!-- uuid: 1b8ae101-7a9d-4abb-9a76-c0cb21fda464 --> — As the configurer, I want every mistake in an edited
  configuration reported at once and a bad edit to leave the running service untouched, so a typo
  never takes my timer down. _(→ `JOURNEY-CONF-EDIT`; `INV-CONF-8`, `INV-CONF-10`, `INV-CONF-11`,
  `INV-CONF-12`, `INV-CONF-13`, `INV-CONF-14`, `INV-CONF-17`.)_
- **`STORY-CONF-HISTORY`** <!-- uuid: 289a65b7-f588-491e-80ab-3820b360df92 --> — As the operator, I want to edit the configuration
  while my history exists, and never see that history change, so removing a task or a cycle type from
  the routine does not rewrite what I did. _(→ `JOURNEY-CONF-REMOVE-TASK`; `INV-CONF-15`,
  `INV-CONF-16`.)_

## Journeys

### `JOURNEY-CONF-EDIT` — changing the routine <!-- uuid: 3f93297b-1c18-4607-a98a-0e2d88fb7000 -->

**Primary actor:** `ACTOR-CONFIGURER`. **Other actors:** `ACTOR-CLIENT`.
_Requires:_ `INV-CONF-1`, `INV-CONF-2`, `INV-CONF-3`, `INV-CONF-4`, `INV-CONF-5`, `INV-CONF-6`,
`INV-CONF-7`, `INV-CONF-8`, `INV-CONF-9`, `INV-CONF-10`, `INV-CONF-11`, `INV-CONF-12`, `INV-CONF-13`,
`INV-CONF-14`, `INV-CONF-17`, `INV-CONF-18`. _Includes:_ none.

1. The configurer adds a task, lists it in a profile and asks the running service to reload.
2. The service validates the new file against the schema and then the semantic rules, and reports
   every problem it finds at once (`INV-CONF-10`).
3. With no problems, the new configuration is swapped in and the configuration generation advances.
   A task added mid-period appears at the next period change or profile change, not retroactively.

Extensions:

- 2a. The file has a problem (a task listed under the wrong cadence, a `tz` of `PST`, a `minutes` of
  0). The reload is rejected, the previous configuration keeps running and the problem is reported
  through the service's health (`INV-CONF-12`).
- 2b. The edit removes the active profile. That is a bad reload (`INV-CONF-12`).
- 2c. The edit changes the listening port or the public URL. It takes effect only on restart, and the
  reload reports that it saw the change (`INV-CONF-13`).
- 2d. The configurer adds a `snooze_minutes` or `carry_over` key. The unknown key is rejected
  (`INV-CONF-8`).

### `JOURNEY-CONF-REMOVE-TASK` — dropping a task that has history <!-- uuid: 80398d20-4109-4300-906a-fefcb491ed09 -->

**Primary actor:** `ACTOR-CONFIGURER`. **Other actors:** `ACTOR-OPERATOR`, `ACTOR-CLIENT`.
_Requires:_ `INV-CONF-15`, `INV-CONF-16`. _Includes:_ none.

1. A task `post-plan` has instances in the current day and in past periods. The configurer removes
   its definition and a cycle type from the configuration and reloads.
2. Replay, the state a client reads and the alert schedule keep working from the snapshots in the
   log: past and current instances keep their titles, groups, links and due instants (`INV-CONF-15`).
3. The next profile change withdraws the current day's open `post-plan` instance, because its
   definition is gone; a completed one is untouched. A running cycle of the removed type keeps its
   snapshot title and falls back to the default alert settings (`INV-CONF-16`).

## The configuration interface

### `INTF-CONFIG` — the configuration file <!-- uuid: 8f82ed63-43af-483a-aa56-b76b2084468d -->

**Counterparty:** `ACTOR-CONFIGURER` (kind: actor; essential participant: without a configuration
there is no routine to track). **What crosses in:** one JSON document. It has `defaults` (cycle
minutes, boost buttons, the starting profile, the future-skew limit, the alert and attention
settings), `listen_port`, an optional `public_url`, an ordered `group_order`, named `profiles` (each
listing its task and cycle ids by cadence), `tasks` (title, cadence, optional group and link, a due
rule) and `cycles` (title, minutes, pre-filled key names, optional alert override). **What crosses
out:** acceptance, or a list of every problem with its path. **What must hold:** `INV-CONF-1` to
`INV-CONF-18`.

A generic example, itself valid under the rules below:

```yaml
defaults:
  cycle_minutes: 25
  boost_minutes: [5, 10, 25]
  profile: normal
  max_future_skew_seconds: 60
  alert: { sound: Glass, reminder_sound: Tink, repeat_minutes: 5 }
  attention:
    { due_soon_minutes: 30, overtime_high_minutes: 15, stale_pause_minutes: 45 }
listen_port: 49210
public_url: https://focus.example.test
group_order: [Start of day, During the day, End of day]
profiles:
  normal:
    daily: [plan-day, end-of-day-summary]
    weekly: [weekly-update]
    sprint: [capacity-check]
    cycles: [review, deep-work]
tasks:
  plan-day:
    {
      title: Plan the day,
      cadence: daily,
      group: Start of day,
      due: { at: "09:00", tz: America/New_York },
    }
  end-of-day-summary:
    {
      title: Post the summary,
      cadence: daily,
      group: End of day,
      due: { at: "17:30", tz: America/New_York },
    }
  weekly-update:
    {
      title: Write the weekly update,
      cadence: weekly,
      due: { weekday: thu, at: "09:00", tz: America/New_York },
    }
  capacity-check:
    {
      title: Do the capacity check,
      cadence: sprint,
      due: { day: 1, at: "09:00", tz: America/New_York },
    }
cycles:
  review: { title: Review cycle, minutes: 25, keys: [pr] }
  deep-work:
    {
      title: Deep work cycle,
      minutes: 50,
      keys: [ticket, pr],
      alert: { sound: Hero, repeat_minutes: 10 },
    }
```

## Invariants

- **`INV-CONF-1`** <!-- uuid: ff59ead6-de85-4b7c-a3c8-bc46fe8ad07a --> — The product MUST be generic: everything specific to one
  routine or one employer is configuration, and the product, its docs, its fixtures and its examples
  MUST carry no such detail. Examples and fixtures use only generic text and the `example.test` host.
- **`INV-CONF-2`** <!-- uuid: a0a1a2a4-3b52-4032-86e1-6112877e8892 --> — A profile MUST list task ids and cycle ids; the definitions
  live once under `tasks` and `cycles`. Exactly one profile is active, and the configuration's
  `defaults.profile` MUST name a defined profile.
- **`INV-CONF-3`** <!-- uuid: 635bfe81-eb9b-4d43-9a23-68b9e0572ddd --> — A due rule is REQUIRED on every task. A daily task takes `at`
  and `tz`; a weekly task takes `weekday`, `at` and `tz`; a sprint task takes `day`, `at` and `tz`.
  `at` is a 24-hour `HH:MM` time, `weekday` is one of `mon` to `sun`, `day` is at least 1 and counts
  calendar days with 1 the period's first civil date, and `tz` is REQUIRED and MUST be a valid zone
  name (`INV-TIME-3`). A rule whose fields do not match its cadence is rejected.
- **`INV-CONF-4`** <!-- uuid: b793102e-1639-4184-a1f2-d9b2e2a281c6 --> — A due rule MUST resolve against the civil dates of a period,
  in the rule's own zone, when the period starts, and the resolved instant MUST be stored in the task
  instance. A `weekday` that occurs more than once in a period resolves to its first occurrence. A
  rule that matches no civil date in its period (a `day` beyond the sprint's end) MUST NOT be
  materialized, and the response to the period change MUST report it with the reason.
- **`INV-CONF-5`** <!-- uuid: 3bd81ab8-feba-4aeb-8e5c-990c221491e2 --> — A cycle's planned duration MUST resolve by precedence: the
  start-time override, then the cycle type's `minutes`, then `defaults.cycle_minutes`. A cycle started
  of a type outside the active profile is allowed and is flagged `not_in_profile`.
- **`INV-CONF-6`** <!-- uuid: bd02f7ba-6097-4636-b2ec-0c2c95ce3e03 --> — `keys` are only the pre-filled key names in a cycle's form;
  the operator MAY add any key at any time, and values are free text. A key MUST match `[a-z0-9_-]+`
  (`invalid_request`). The key `cycle_type` is reserved for the connector and MUST be rejected as
  request input (`reserved_key`) and as a configured `keys` entry.
- **`INV-CONF-7`** <!-- uuid: 604b4820-ab3f-457f-a320-738fb8dbc71d --> — Alert settings (`sound`, `reminder_sound`, `repeat_minutes`)
  MUST resolve per cycle type, then `defaults.alert`. `sound` is the one played when the cycle's time is
  up and `reminder_sound` the one played on each overtime repeat; each MUST be settable, per cycle type
  and in `defaults.alert`, to a sound name or to an explicit "no sound" (`null`). A `null` is a setting
  like a name, distinct from leaving the key out: a key that is left out falls back, a `null` does not.
  `sound` is the type's, else `defaults.alert`'s. `reminder_sound` is the first one set of the type's
  `reminder_sound`, the type's `sound`, `defaults.alert`'s `reminder_sound`, `defaults.alert`'s `sound`;
  so a type that sets only its `sound` reminds with it, not with the default reminder. A sound that is
  "no sound" plays nothing and its notification is still sent (`INV-CYCLE-16`). `defaults.alert.sound`
  is required and MAY be `null`; an empty string is not a sound and MUST be rejected. Both sounds SHOULD
  be under two seconds long, and a cycle type that routinely runs long SHOULD raise its
  `repeat_minutes`. _(Operator rulings, 2026-10-10: a static "no sound" per cycle type is permitted; the
  reminders keep repeating, with the notification only, when `reminder_sound` is "no sound".)_
- **`INV-CONF-8`** <!-- uuid: e02c9b46-b6a4-4c24-8bfa-56077b3ff37d --> — An unknown configuration key MUST be rejected. In particular
  no option can exist to acknowledge, mute, snooze or cap the overtime sound while it is playing or
  repeating, or to carry an unfinished task over into the next period: a typo such as `snooze_minutes`
  is an error, not an ignored setting. A configured "no sound" (`INV-CONF-7`) is a standing choice made
  per cycle type before the cycle runs, not a way to quiet a sound at run time, and is permitted.
  _(Operator rulings, 2026-10-07: no mute, acknowledge or snooze; no carry-over. 2026-10-10: a static
  per-cycle-type "no sound" is compatible with that ruling.)_
- **`INV-CONF-9`** <!-- uuid: 3097195e-6261-4f05-a407-0900dba65427 --> — Tasks MUST sort within a period by `group_order` (a group not
  listed, and a task with no group, sort last and equal to each other), then by due time.
- **`INV-CONF-10`** <!-- uuid: 5fd56650-a4ee-4d13-adb3-6c0c54ed017d --> — A configuration MUST be rejected as a whole, with every
  problem reported at once and each carrying its path, when: a profile names an unknown task or cycle
  or lists a task under a cadence other than its own; a due rule's fields do not match its cadence or
  are missing; a `weekday` is not `mon` to `sun`; a `day` is less than 1; an `at` is not a valid
  `HH:MM`; a `tz` is not a valid zone name; a duration or interval is out of range (`INV-CONF-11`);
  `defaults.profile` is not defined; a key name is not `[a-z0-9_-]+` or is `cycle_type`; a required
  key is missing; or a key is unknown.
- **`INV-CONF-11`** <!-- uuid: 9b966746-f8eb-4d0e-aecb-dd2916c73016 --> — Every minutes or interval value (`planned_minutes`, a boost,
  a `minutes` override, `cycle_minutes`, `repeat_minutes`) MUST be an integer from 1 to 525600 (one
  year), so duration arithmetic stays far from overflow.
- **`INV-CONF-12`** <!-- uuid: b93f4c5c-b332-4164-b3e6-8ca30dd7e183 --> — A bad configuration at first start MUST be a hard failure.
  A bad reload MUST keep the previous configuration and report the problem through the service's
  health; a reload that removes the active profile is a bad reload.
- **`INV-CONF-13`** <!-- uuid: 2004bb00-d7f8-4460-b370-a3937a8581a5 --> — `listen_port` is required, `public_url` is optional and MUST
  be an `http` or `https` URL, and every deep link is built from `public_url`. A change to either
  takes effect only on restart, and a reload that sees one MUST report it.
- **`INV-CONF-14`** <!-- uuid: 8c7deaca-206f-4150-90d4-3df7747438c8 --> — The configuration MUST have a JSON Schema (2020-12, declaring
  `$schema`) checked in with the product; the loader validates against it and then applies the
  semantic checks.
- **`INV-CONF-15`** <!-- uuid: 712b0d84-6af2-4990-a5ba-a29cce5ed5f7 --> — Replay MUST NOT consult the configuration. After a reload
  that removes a task definition, a cycle type or a non-active profile, replay, the state a client
  reads and the alert schedule MUST keep working from the snapshots in the log, so a configuration
  edit never rewrites history.
- **`INV-CONF-16`** <!-- uuid: d51f87e6-6912-4e83-9c88-b2937a908404 --> — Only runtime behavior reads the current configuration: the
  alert sound and repeat interval, the attention thresholds, `not_in_profile`, group ordering and the
  tie-break of the next task. A cycle type that no longer exists falls back to `defaults.alert` and its
  snapshot title. A profile change MUST withdraw the open tasks whose definition has vanished from the
  configuration, as it withdraws any open task the profile no longer lists.
- **`INV-CONF-17`** <!-- uuid: 999aa26d-fe46-46e5-99e1-9500298c1714 --> — The configuration generation MUST advance on each successful
  reload and be seeded at each start so that a (log lines, generation) pair never recurs across a
  restart (`INV-LOG-19`).
- **`INV-CONF-18`** <!-- uuid: 957fc794-d733-497b-8c1d-5f19a736155e --> — The maximum a request's `effective_at` may lie in the future
  MUST default to 60 seconds and MAY be set by `defaults.max_future_skew_seconds`.
