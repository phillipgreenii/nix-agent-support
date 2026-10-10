# pg-task-focus — the command line

The command line is the first client of the service and the one that works with nothing else running:
a terminal verb for every action the operator takes, a status view, an editor for the log and an
offline check. It is a thin client: every rule is the service's, and every verb is one or two requests
of `INTF-API`. This doc owns what a verb prints, how it names things, what it does when the service is
not there or cannot write, and how it tells a script what happened. The service is in
[`service.md`](service.md); the rules a refusal comes from are in the docs it cites.

## Stories

- **`STORY-CLI-DRIVE`** <!-- uuid: 12f65482-fd01-4da1-b7c1-f8e2df2aaf81 --> — As the operator at a terminal, I want a short verb for each thing I do
  (complete or skip a task, start, pause, resume, boost, stop, switch, back-fill and annotate a cycle,
  roll the periods, change the profile, undo) and a status view of what is due, what is next and what
  is running, so the routine costs seconds. _(→ `JOURNEY-CLI-DAY`; `INV-CLI-1`, `INV-CLI-6`,
  `INV-CLI-7`, `INV-CLI-8`, `INV-CLI-9`, `INV-CLI-10`.)_
- **`STORY-CLI-SCRIPT`** <!-- uuid: aa3164b4-1098-4e56-bbd9-c4c700879bb2 --> — As a script or a menu-bar plugin, I want machine output that matches a
  published schema, a stream of whole state objects, and exit codes that say what kind of failure
  happened, so I never parse prose. _(→ `JOURNEY-CLI-DAY`, `JOURNEY-CLI-FAILURES`; `INV-CLI-2`,
  `INV-CLI-3`, `INV-CLI-4`.)_
- **`STORY-CLI-RETRY-SAFELY`** <!-- uuid: dd38d929-4839-4fc4-89af-dd29017c6b24 --> — As the operator whose request got no answer, I want to repeat it and
  be sure it happens once, and to be told plainly when the service is read-only, so a flaky disk never
  costs me a duplicated or a lost action. _(→ `JOURNEY-CLI-FAILURES`; `INV-CLI-3`, `INV-CLI-4`,
  `INV-CLI-5`.)_
- **`STORY-CLI-CHECK-OFFLINE`** <!-- uuid: e45a9d70-d1e3-40fc-a92d-f856cea84685 --> — As the operator whose service will not start, I want to check the log
  and the configuration without the service, and be told the line or the path at fault, so I can fix
  it. _(→ `JOURNEY-CLI-FAILURES`; `INV-CLI-11`.)_

## Journeys

### `JOURNEY-CLI-DAY` — a day at the terminal <!-- uuid: 22fbbefc-2cef-4e4a-be37-2efb14041d9e -->

**Primary actor:** `ACTOR-OPERATOR`. **Other actors:** `ACTOR-CLIENT`. _Requires:_ `INV-CLI-1`,
`INV-CLI-2`, `INV-CLI-6`, `INV-CLI-7`, `INV-CLI-8`, `INV-CLI-9`, `INV-CLI-10`, `INV-CLI-12`.
_Includes:_ none.

1. The operator runs the status view: the periods, the checklist with short task names, the next task
   ("Next: Post the plan, due 09:30 America/New_York, in 22m") and any running or dimmed cycle
   (`INV-CLI-1`).
2. The operator completes a task by a unique prefix of its name, with a time of 8:55 meaning today in
   the machine's zone (`INV-CLI-7`, `INV-CLI-8`). The request carries an id (`INV-CLI-6`).
3. The operator starts a cycle, is interrupted, starts another, and switches back by a unique prefix
   of the paused cycle's title (`INV-CLI-7`).
4. Two cycles are paused and the operator pauses or resumes without naming one: the service refuses,
   the candidates are printed, and the operator names one (`INV-CLI-7`).
5. The next morning the operator asks to roll the periods. The preview is shown; a cycle is still
   paused, so the blockers are printed and the command exits non-zero. The operator stops it and rolls
   again; the version of the preview is carried back so what was shown is what happens (`INV-CLI-9`).
6. The operator undoes the last change (`INV-CLI-10`).

Extensions:

- 2a. A skip with no reason, or a blank one, is refused before anything is sent (`INV-CLI-12`).
- 2b. The name matches several tasks. The operator is told which and asked to be more specific
  (`INV-CLI-7`).
- 5a. The operator wants the new period's data as JSON for a script: every verb prints the service's
  own document, which validates against the published schema (`INV-CLI-2`).

### `JOURNEY-CLI-FAILURES` — when it does not work <!-- uuid: 89861007-43c6-4849-b253-d21c3995e6d4 -->

**Primary actor:** `ACTOR-OPERATOR`. **Other actors:** `ACTOR-CLIENT`, `ACTOR-HOST`. _Requires:_ `INV-CLI-2`, `INV-CLI-3`, `INV-CLI-4`, `INV-CLI-5`, `INV-CLI-11`. _Includes:_ none.

1. The service is not running. A verb prints one line on standard error saying so and exits with the
   code for "not reachable" (`INV-CLI-3`, `INV-CLI-4`).
2. The service answers a change with `store_unavailable` and no event is known to have been written.
   The verb says the outcome is unknown, prints the id it used, and exits with the store code; the
   operator repeats the verb with that id and it is applied once (`INV-CLI-5`).
3. The service is read-only. Status prints the READ-ONLY sentence as its first line, and every change
   prints the same sentence and exits with the store code (`INV-CLI-5`).
4. The service will not start. The operator checks the log offline and is told the line number of the
   corruption, and checks the configuration and is told each problem by path (`INV-CLI-11`).

## Invariants

- **`INV-CLI-1`** <!-- uuid: c8eff03d-3abe-46de-bd1d-befbcc1d3018 --> — The command line MUST be a client of the service's API and MUST
  NOT read the log or the configuration to answer a client verb. One binary runs the service and
  serves as its client. The verbs cover: the status view; a watch that stays connected; a period change
  and a roll; a profile change; task done and skip; cycle start, pause, resume, boost, stop, switch,
  back-fill and note; events list, correct and retract; undo; and the offline checks of `INV-CLI-11`.
  The status view MUST print short task names, the next task, a running cycle as time elapsed and time
  left or overtime, then each dimmed cycle and the resume offer, and show every time with its zone.
- **`INV-CLI-2`** <!-- uuid: 01a98318-49b3-462e-8cfd-fc9658715d1a --> — Every client verb MUST have a machine form whose output is the
  service's own JSON document, and that output MUST validate against a checked-in JSON Schema; the
  shared definitions are derived from the API contract and a test fails when they drift. The watch MUST
  print one full state object per line, each validated, whenever the state version or the store
  changes and at least once per refresh interval, so a menu-bar plugin needs no polling process per
  second; interrupting it ends it cleanly.
- **`INV-CLI-3`** <!-- uuid: a035279f-be7d-4057-9377-835dab24e9e0 --> — When the service is not reachable the command line MUST say
  so in one line on standard error and exit non-zero with its own code. It MUST NOT start the service.
- **`INV-CLI-4`** <!-- uuid: 7356a646-f6b0-437a-b44e-cc2eee8ac859 --> — Exit codes follow the repository's convention: 1 is the
  generic error and carries no branchable meaning, and each specific condition has its own value of 2 or
  more: a bad argument, the service not reachable, a request the service refused, a store that did not
  take the change (read-only or an unknown outcome), a service that is starting and not ready, a service
  that could not start, and a check that found a problem. A refusal prints its reason code and sentence,
  and lists the candidates or the blocking cycles it carries.
- **`INV-CLI-5`** <!-- uuid: c64600b3-c782-400c-bf34-a61645ec5421 --> — A change answered `store_unavailable` by a read-only store MUST
  print the READ-ONLY sentence and exit with the store code; any other `store_unavailable` MUST print
  that the outcome is unknown, print the id the request carried and say the verb can be repeated with
  it, and exit with the same code. The status view MUST print the READ-ONLY sentence as its first line
  while the store is read-only. _(Operator ruling, 2026-10-08, decision log row 38.)_
- **`INV-CLI-6`** <!-- uuid: fff85226-30cb-4cea-b9fd-300c051a9629 --> — Every verb that changes anything MUST send a request id, so a
  repeated request is applied once, and MUST accept the id to use so a retry after an unknown outcome
  can repeat it.
- **`INV-CLI-7`** <!-- uuid: f0dac3c0-5247-49a2-9a5f-ad96f0b497d7 --> — A task is named by its definition name or a unique prefix of it
  among the current periods. A cycle is named by its id or a unique prefix of the title of a cycle that
  is not stopped, and MAY be left out only when the service can tell which one is meant; when it cannot,
  the service answers `cycle_ambiguous` and the command line prints the candidates. There is no "last
  cycle" shortcut, and a stopped cycle is named by its id. A name that matches nothing, or several, is
  a usage error that says so. _(Operator ruling, 2026-10-08, decision log row 41.)_
- **`INV-CLI-8`** <!-- uuid: a0b2771b-6182-4878-afd2-70131d9d8653 --> — A time given to a verb is a time today in the machine's zone
  (`9:05`), a time yesterday, or an RFC 3339 instant, and the service validates it like any
  `effective_at`. The machine's zone MUST come from the `TZ` variable when it is a valid zone name, else
  the target of the local-time link, and a verb that needs the zone MUST fail with a message naming both
  inputs when neither yields one; it never guesses. _(Realizes `INV-TIME-10` for this client.)_
- **`INV-CLI-9`** <!-- uuid: 9e7fe1b5-7bfe-471d-8819-8fe246bee011 --> — The roll verb MUST change every period that has ended, with
  the start as today in the period's zone and, for a week or a sprint, the end as the start plus the
  length of the previous one, after showing the preview, and MUST carry the preview's version back so
  a stale preview is refused. While any cycle is running or paused it MUST print the blocking cycles
  and exit non-zero, and with nothing ended it says there is nothing to roll. Both the roll and the
  change verb accept a profile and a backdating time. _(Operator rulings, 2026-10-08, decision log rows
  31 and 40.)_
- **`INV-CLI-10`** <!-- uuid: d8d7b443-c70f-4de5-85b8-e38060b46256 --> — The undo verb MUST retract the most recent live event, or
  its whole batch when it is a batch member, that is not itself a retraction, and is subject to the
  rule that a batch with later dependents is refused; the refusal names the dependents.
- **`INV-CLI-11`** <!-- uuid: bfcfb02d-fec2-48da-932b-3487961d5387 --> — The offline check MUST read a log (the data directory or the
  log file) without the service and without a lock, replay its committed events, and say what the next
  start would recover and, when a service would refuse the log, why, with the line number of a
  corruption; it exits non-zero then. A configuration check MUST list every problem with the path of the
  value at fault and exit non-zero when there is one. Neither changes anything.
- **`INV-CLI-12`** <!-- uuid: fee9ab7a-f6f2-4a4a-9821-43147873d4ef --> — A skip MUST be refused by the command line before a request is
  sent when its reason is empty or only white space, as the service refuses it. _(Operator ruling,
  2026-10-08, decision log row 42.)_
