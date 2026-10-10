# pg-task-focus — the web UI

The web UI is the client the operator keeps open in a browser tab: the checklists of the day, week and
sprint, the running cycle and the cycles it interrupted, the forms that start, pause, switch and stop
cycles, the modals that change periods and the profile, and the editor that fixes mistakes. It is a client
of the service's one interface ([`service.md`](service.md)); it holds no rule of its own about the log.
This doc owns what the page shows, which controls it offers and when it disables them, what it says when
the service refuses, and how it keeps the operator's words out of every log. The rules each action
obeys are in [`periods-and-rollover.md`](periods-and-rollover.md), [`cycles-and-timer.md`](cycles-and-timer.md),
[`corrections.md`](corrections.md) and [`event-log.md`](event-log.md); the zones it names are in
[`time-and-zones.md`](time-and-zones.md); the reasons behind its construction are in
`docs/adr/0092-pg-task-focus-web-ui-is-dependency-free-modules-embedded-in-the-daemon.md`.

## Stories

- **`STORY-WEB-SEE-AND-ACT`** <!-- uuid: ecd5948f-6094-4b73-9f4c-2b44eb66c911 --> — As the operator, I want one page that shows what is due, what is
  overdue and what is running, and lets me complete or skip a task, or start, pause, switch and stop a
  cycle, in one action each, so the routine costs me no thought. _(→ `JOURNEY-WEB-TASK-DONE`,
  `JOURNEY-WEB-INTERRUPT`; `INV-WEB-9`, `INV-WEB-10`, `INV-WEB-11`, `INV-WEB-12`, `INV-WEB-13`.)_
- **`STORY-WEB-LIVE`** <!-- uuid: 80b9657b-5dad-4024-975f-d53d9d5b008b --> — As the operator who also uses the command line and the menu bar, I want an
  open tab to show a change made anywhere at once, and to say so when it has lost the service, so the page
  is never quietly out of date. _(→ `JOURNEY-WEB-LIVE`; `INV-WEB-3`.)_
- **`STORY-WEB-READ-ONLY`** <!-- uuid: 99e95df2-5955-483a-9ab7-fde3bb2d21fb --> — As the operator, I want the page to say loudly, at the top, that the
  store can no longer be written, and to stop offering what cannot work, so a failed disk is never
  mistaken for a lost click. _(→ `JOURNEY-WEB-READ-ONLY`; `INV-WEB-6`.)_
- **`STORY-WEB-HONEST-ERRORS`** <!-- uuid: 91777d3b-0684-4f0f-ad11-890598c864dc --> — As the operator, I want every refusal shown in the service's own
  words, and a repeat of an action that is already in effect to stay quiet, so I learn what is wrong and am
  never scolded for a harmless click. _(→ `JOURNEY-WEB-REFUSED`; `INV-WEB-4`, `INV-WEB-5`.)_
- **`STORY-WEB-ROLL`** <!-- uuid: e3c5f981-2e38-483e-a159-f24fc931a1ff --> — As the operator, I want to start a new day, week or sprint after seeing what
  becomes of every open task, and to be stopped while a cycle is running, so a rollover is a decision and
  never an accident. _(→ `JOURNEY-WEB-ROLLOVER`; `INV-WEB-7`, `INV-WEB-8`.)_
- **`STORY-WEB-FIX`** <!-- uuid: ae2608f4-25d7-4a5d-8315-97989c8e5d76 --> — As the operator, I want to see the corrected log, one click from the original,
  and to fix a time, a reason, a cycle's type, a forgotten break or a forgotten stop, with a picture of the
  result before I confirm, so I can make the record honest without losing what it said. _(→
  `JOURNEY-WEB-FORGOTTEN-LUNCH`; `INV-WEB-16`, `INV-WEB-17`, `INV-WEB-18`.)_
- **`STORY-WEB-LINK`** <!-- uuid: 31cbdcd9-d5cf-4cdc-b98a-e69940128ffd --> — As the operator, I want a link in a notification or an attention item to
  open the task or the cycle it names, so one click goes from the warning to the thing. _(→
  `JOURNEY-WEB-DEEP-LINK`; `INV-WEB-19`.)_
- **`STORY-WEB-PRIVATE-AND-ACCESSIBLE`** <!-- uuid: f5a01269-15de-49c8-827e-de7da9cbfd87 --> — As the operator, I want a page I can use with the keyboard and a
  screen reader, that never tells me about the clock every second, and that sends what I type nowhere but to
  the service. _(→ `JOURNEY-WEB-READ-ONLY`; `INV-WEB-20`, `INV-WEB-21`, `INV-WEB-22`.)_

## Journeys

### `JOURNEY-WEB-TASK-DONE` — completing and skipping a task <!-- uuid: dccfca8d-167e-4844-84f5-b4b8d715db89 -->

**Primary actor:** `ACTOR-OPERATOR`. **Other actors:** `ACTOR-CLIENT`. _Requires:_ `INV-WEB-9`, `INV-WEB-10`,
`INV-WEB-11`. _Includes:_ none.

1. The operator opens the page. Each checklist lists its tasks with the due time and its zone, and the
   header says what is next (`INV-WEB-9`).
2. The operator presses Done on a task. The task is completed now, with no further input, and an Undo
   appears for ten seconds (`INV-WEB-10`, `INV-WEB-11`).
3. For another task the operator presses Skip, types a reason and confirms; the Undo appears again.

Extensions:

- 2a. The task was done earlier. The operator opens "Done at", which shows a time that defaults to now,
  in a named zone, and confirms a chosen time (`INV-WEB-10`).
- 3a. The reason is blank. The skip cannot be confirmed (`INV-WEB-10`).
- 3b. The operator has skipped for the same reason before. The recent reasons are offered (`INV-WEB-10`).
- 2b. The operator presses Undo. The event just appended is retracted; if the service refuses, its sentence
  is shown (`INV-WEB-11`).

### `JOURNEY-WEB-INTERRUPT` — an interruption and the way back <!-- uuid: 1127ac1a-3753-4c59-a134-9dd80b58bbf8 -->

**Primary actor:** `ACTOR-OPERATOR`. **Other actors:** `ACTOR-CLIENT`. _Requires:_ `INV-WEB-12`, `INV-WEB-13`,
`INV-WEB-14`. _Includes:_ none.

1. A cycle runs; its timer, Pause, Stop and Boost are at the top of the page, and the tab's title shows the
   timer (`INV-WEB-12`).
2. The operator starts another cycle. The page says the running one will be paused; it is then shown below
   the new one, dimmed and marked paused, with Switch and Stop (`INV-WEB-12`, `INV-WEB-13`).
3. When the interrupting cycle is stopped, the page asks "Resume `<title>`?" and keeps asking until the
   operator resumes or stops that cycle (`INV-WEB-12`).
4. Pressing Switch on a dimmed cycle makes it the focus and dims the one that ran (`INV-WEB-12`).

Extensions:

- 2a. A cycle's time runs out. The timer shows the overtime as a negative remaining time and the tab title
  shows it too; a screen reader is told once (`INV-WEB-12`, `INV-WEB-20`).
- 4a. The service says several cycles could be meant. The page lists them and the operator chooses one;
  it never picks (`INV-WEB-14`).

### `JOURNEY-WEB-LIVE` — a change made elsewhere <!-- uuid: ffd8ed36-c947-4739-9ca5-74d54e571735 -->

**Primary actor:** `ACTOR-OPERATOR`. **Other actors:** `ACTOR-CLIENT`, `ACTOR-HOST`. _Requires:_ `INV-WEB-3`.
_Includes:_ none.

1. The page is open. The operator stops a cycle with the command line. The page shows it stopped without a
   reload and without polling (`INV-WEB-3`).
2. The laptop sleeps and wakes. The page reads the state again as soon as it is visible, and its timer is
   right (`INV-WEB-3`, `INV-WEB-12`).

Extensions:

- 1a. The service stops or restarts. The page says it has lost the service and shows the last state it had,
  marked as stale, and recovers by itself when the service is back (`INV-WEB-3`).

### `JOURNEY-WEB-READ-ONLY` — the disk fails <!-- uuid: 00aabb5a-ed6c-4256-a267-8b87fe2a9989 -->

**Primary actor:** `ACTOR-OPERATOR`. **Other actors:** `ACTOR-CLIENT`. _Requires:_ `INV-WEB-6`, `INV-WEB-20`.
_Includes:_ none.

1. A write fails and the store becomes read-only. An open page is told at once.
2. A banner at the very top of every area reads `READ-ONLY: <reason>. Restart pg-task-focus to recover`; a
   screen reader is told. Every control that would change something is disabled; reading, navigating and
   the editor's original view still work (`INV-WEB-6`).
3. The operator restarts the service; the page recovers and the banner is gone.

### `JOURNEY-WEB-REFUSED` — a refusal and a repeat <!-- uuid: 42ab293e-dd7a-4328-b92c-5b9f7b67dec9 -->

**Primary actor:** `ACTOR-OPERATOR`. **Other actors:** `ACTOR-CLIENT`. _Requires:_ `INV-WEB-4`, `INV-WEB-5`.
_Includes:_ none.

1. The operator completes a task that another client already completed. The page shows the service's own
   sentence, naming the resolving event, with its code and trace id (`INV-WEB-4`).
2. The operator presses Pause on a cycle that is already paused. Nothing is refused and nothing is shown as
   an error; the page may say, quietly, why nothing changed (`INV-WEB-5`).

Extensions:

- 1a. The service cannot be reached while a change is in flight. The page says the outcome is unknown and
  offers a retry that repeats the same request (`INV-WEB-4`).
- 1b. The service answers that the store is unavailable and the outcome is unknown. The same retry is
  offered (`INV-WEB-4`).

### `JOURNEY-WEB-ROLLOVER` — a new day while a cycle is paused <!-- uuid: 2d52c028-e42b-41b7-b7bc-91f613784579 -->

**Primary actor:** `ACTOR-OPERATOR`. **Other actors:** `ACTOR-CLIENT`. _Requires:_ `INV-WEB-7`, `INV-WEB-8`.
_Includes:_ none.

1. The day has ended. The header says "New day: roll over" and a screen reader is told once. The operator
   opens the period modal, which proposes today as the start, in the browser's zone (`INV-WEB-7`, `INV-WEB-8`).
2. A preview lists the open tasks that will be marked missed, the tasks the new period will add and any that
   cannot be added, and states the limit on undoing (`INV-WEB-8`).
3. A paused cycle blocks the change. The modal says so, lists the cycle with Stop and "End at", and will not
   confirm. The operator ends the cycle at the time it really ended (no time is proposed), and the preview
   refreshes (`INV-WEB-8`).
4. The operator marks every open task skipped for one reason, except one, confirms, and the new day starts
   (`INV-WEB-8`).

Extensions:

- 1a. The log is empty. The header invites the operator to set the day, week and sprint and the modal
  proposes all three (`INV-WEB-7`).
- 2a. Something changed after the preview. The service answers that the preview is stale; the page refreshes
  the preview and asks again (`INV-WEB-8`).
- 1b. The operator is a day late and backdates the change by any amount (`INV-WEB-8`).

### `JOURNEY-WEB-FORGOTTEN-LUNCH` — fixing the record <!-- uuid: 9eafd93d-2102-4e2c-a923-1f80a8b86e13 -->

**Primary actor:** `ACTOR-OPERATOR`. **Other actors:** `ACTOR-CLIENT`. _Requires:_ `INV-WEB-16`, `INV-WEB-17`,
`INV-WEB-18`. _Includes:_ none.

1. In the editor the operator sees the corrected events, with the original one click away on any event that
   was fixed.
2. The operator chooses "Insert break", names the cycle and the two times in the zone shown, and sees the
   cycle's running time before and after, with the date and zone (`INV-WEB-17`).
3. The operator confirms. The service accepts, and the editor shows the new pause and resume as one batch
   that can be retracted whole (`INV-WEB-16`).

Extensions:

- 3a. The service refuses (the break overlaps a pause, or ends at the stop). Its own sentence is shown and
  nothing changed (`INV-WEB-4`, `INV-WEB-17`).
- 1a. The operator corrects a cycle's type. The new type is chosen from the configured types and the title
  follows from the service (`INV-WEB-18`).
- 1b. The operator retracts an event. The page says retraction is refused once later events depend on it, and
  if it is refused it lists those events as links into the editor (`INV-WEB-16`).

### `JOURNEY-WEB-DEEP-LINK` — a link to a task or a cycle <!-- uuid: cbe11b6e-9f48-445e-8323-059405df9c1d -->

**Primary actor:** `ACTOR-OPERATOR`. **Other actors:** `ACTOR-CLIENT`. _Requires:_ `INV-WEB-19`. _Includes:_ none.

1. The operator follows a link that ends in `#/tasks/<task id>`. The page opens at that task, which is
   marked and focused (`INV-WEB-19`).
2. A link that ends in `#/cycles/<cycle id>` opens at that cycle (`INV-WEB-19`).

Extensions:

- 1a. The task belongs to an earlier period, or the cycle is stopped. The page shows what the log says
  about it (`INV-WEB-19`).
- 2a. The id is unknown. The page says so, in words, and shows the rest of the page (`INV-WEB-19`).

## The page's interface

### `INTF-WEB` — the page the service hands the browser <!-- uuid: 312785b6-855c-4d1d-92b6-c6a2929f552b -->

**Counterparty:** `ACTOR-OPERATOR` (kind: actor; essential participant: the page exists to be used).
**What crosses in:** the operator's actions, the state, configuration, event listing and stream of
`INTF-API`, and the operator's clock and zone as the browser knows them. **What crosses out:** requests of
`INTF-API` and nothing else. **What must hold:** `INV-WEB-1` to `INV-WEB-22`.

## Invariants

### Serving

- **`INV-WEB-1`** <!-- uuid: 3cd1eacc-ca6e-42e2-8a5e-875b0a805619 --> — The service MUST serve the page from assets built into it, at its root and under
  one assets path, through the same Host and Origin defences as every other route, so it works with no
  proxy and no network beyond the loopback. A name that is not one of the assets MUST be answered
  `not_found`. The page MUST load nothing from another origin and MUST be served under a content security
  policy that allows scripts, styles and connections of its own origin only, and no inline script or
  style, frame or form post. An asset MUST be served with its own content type and be revalidated, so a
  new build is seen on the next load.
- **`INV-WEB-2`** <!-- uuid: 57366426-6e31-4631-b9ad-290eab6ef5ca --> — The page MUST be a client of `INTF-API` only: it MUST NOT read the log, and it MUST
  make every change as a request, naming itself as the web client. It MUST send an `id` on every mutation,
  and a retry of a request whose outcome is unknown MUST send the same `id`.

### Live state and honest errors

- **`INV-WEB-3`** <!-- uuid: 9826c30c-a6a6-436e-8864-f1ac1288b47d --> — The page MUST read the state again on every event of the stream and whenever the tab
  becomes visible again, so a change made by any client appears without polling. While the stream is
  broken it MUST say so, show the last state it had marked as of that time, and recover by itself. A
  timer MUST run from the service's remaining time at its read, less the time since the page received
  it on the page's own clock, so a skewed clock shows the right time.
- **`INV-WEB-4`** <!-- uuid: fb61f185-cc84-472a-8f68-6417a89b2b7c --> — When the service refuses a request the page MUST show the service's own sentence
  (the problem's detail), unchanged, with its `reason` code and `trace_id`, and MUST NOT replace it with a
  generic message. When the service cannot be reached, or answers that the store is unavailable with the
  outcome unknown, the page MUST say that the outcome is unknown and offer a retry that repeats the same
  request. A refusal of a cycle verb that lists several candidate cycles MUST be shown as a choice among
  them. _(Operator ruling, 2026-10-08, decision log row 43.)_
- **`INV-WEB-5`** <!-- uuid: 0c901789-b50a-4185-b43c-2624dae0f9b4 --> — A pause, resume, stop or switch that finds its cycle already in that state is a
  success with `changed: false`; the page MUST NOT show it as an error and MUST NOT change its display,
  and MAY say the service's one-sentence note, quietly. _(Operator ruling, 2026-10-08, decision log row
  36.)_

### Read-only mode

- **`INV-WEB-6`** <!-- uuid: a1ecb9f4-ba02-4f6d-b6f1-dac82863f070 --> — While the store is read-only the page MUST show, at the very top of every area and
  visible without scrolling, the banner `READ-ONLY: <reason>. Restart pg-task-focus to recover`, MUST
  announce it to assistive technology when it appears, and MUST disable or hide every control that would
  change something (the timer controls, task done and skip, the period and profile changes, undo and every
  action of the editor). Reading, navigating and the original view MUST keep working. The banner MUST
  appear as soon as the stream says the mode began. _(Operator ruling, 2026-10-08, decision log row 38.)_

### Header, periods and profile

- **`INV-WEB-7`** <!-- uuid: 97dc1755-0403-43ec-995d-da6b38a9c5b8 --> — The header MUST show the current day, week and sprint with their dates, labels and
  zone, the active profile, and the next task as `Next: <title>, due <HH:MM> <zone>, in <time>` (or that
  nothing is due). For a period that has ended it MUST show exactly `New day: roll over`, `New week: roll over`
  or `New sprint: roll over` as the next action and tell assistive technology once. With an empty log it MUST
  invite the operator to set the day, week and sprint. A due time MUST always appear with its zone
  identifier. _(Realizes `INV-PERIOD-16` and `INV-PERIOD-25` for the web client.)_
- **`INV-WEB-8`** <!-- uuid: 40cb2f2d-e02a-45d1-8a65-e328d6d65c75 --> — The period modal MUST: propose today in the chosen zone as the start of each kind
  and, for a week or sprint, an end of the start plus the length of the previous period; default the zone
  from the browser and let the operator change it; take several ended kinds as one confirmation; allow
  `effective_at` to be backdated without a lower bound; show the preview of the change before it can be
  confirmed (the open tasks of the periods being left and what becomes of each, the tasks the new periods
  add and any that cannot be added with the reason); offer "mark all missed" and "skip all with one
  reason" and a per-task skip with its own reason; carry the preview's version back so a stale preview is
  refused and re-previewed; and state that an undo is refused once later events depend on the new periods.
  While any cycle is running or paused, as the preview's blocking cycles or the state says, it MUST block
  confirmation and list every such cycle with Stop and "End at", where "End at" has no time filled in. The
  profile modal MUST show the preview of added, withdrawn and reinstated tasks, flagging any that would be
  overdue at once, before it can be confirmed. _(Realizes `INV-PERIOD-8`, `INV-PERIOD-13`, `INV-PERIOD-17`
  to `INV-PERIOD-19`, `INV-TIME-10`; operator rulings, 2026-10-08, decision log rows 31 and 40.)_

### Checklists

- **`INV-WEB-9`** <!-- uuid: 4aa68838-819d-4dea-a449-517bfbef6393 --> — The page MUST list, for each current period, its tasks in the service's order,
  grouped, each with its status in words, its due time and zone, and, when overdue, a text and an icon and
  never colour alone. Every area MUST have an explicit empty state, and a period that has ended MUST say so
  where its tasks are.
- **`INV-WEB-10`** <!-- uuid: c045a049-f808-45fb-9822-4a48599cc9e6 --> — Completing an open or missed task MUST take one action and default to now, sending no
  time of its own; "Done at" offers a time that defaults to now, in a named zone. Skipping MUST take one
  action plus a reason that is not blank, and MUST offer the reasons used recently. A task that is not open
  or missed MUST NOT offer either.
- **`INV-WEB-11`** <!-- uuid: 24242438-39e2-418f-8c1b-ae15b7f7b1e3 --> — After a completion, a skip or a stop, an Undo MUST stay visible for ten seconds; it
  MUST be reachable by the keyboard and announced to assistive technology, MUST retract the event just
  appended, and MUST show the service's own sentence if the retraction is refused. The same retraction MUST
  remain reachable in the editor afterwards.

### The cycle panel

- **`INV-WEB-12`** <!-- uuid: 702716f7-4d31-4bc6-911a-708a2e64e36e --> — The running cycle MUST be the focus of a panel that stays visible without scrolling in
  every area, with its timer, Pause, Stop and Boost, the configured boost buttons and a custom number of
  minutes, and the planned and boosted minutes. The timer MUST show overtime as a negative remaining time
  with a text marker. The tab's title MUST show the timer or the overtime. Every cycle that is paused and
  not stopped MUST stay visible beneath the focus, dimmed and marked paused in words, with a Switch button
  while another cycle runs (a Resume button when none does) and a Stop button, and MUST offer boost and a
  note; the resume offer MUST be shown persistently as "Resume `<title>`?" with Switch while another cycle
  runs. A cycle that is not in the active profile MUST be flagged. _(Realizes `INV-CYCLE-3` to
  `INV-CYCLE-6`; operator ruling, 2026-10-08, decision log row 28.)_
- **`INV-WEB-13`** <!-- uuid: dd6f10b2-46f8-4e4b-bece-3e8abbc6a52c --> — Starting a cycle MUST offer the configured types (those of the active profile first, the
  others flagged) and a duration override, and, while a cycle runs, MUST say that it will be paused and
  stay visible. Starting a cycle while one runs is not an error.
- **`INV-WEB-14`** <!-- uuid: aac41b97-56c8-4b90-a092-66fee61c8a59 --> — The page MUST NOT guess which cycle an action means: every cycle action it sends names
  its cycle, and a refusal that several cycles could be meant MUST be shown as a list from which the
  operator chooses. _(Operator ruling, 2026-10-08, decision log row 41.)_
- **`INV-WEB-15`** <!-- uuid: 4d4aca3c-9cbb-4113-9ce6-1b7f5035aff9 --> — The cycle's form MUST take a note and key and value pairs, with the names of the type's
  keys pre-filled and any other pair addable; saving MUST send the whole form; an empty pair MUST NOT be
  sent; the form MUST be optional, so stopping MUST NOT ask for it. _(Realizes `INV-CYCLE-23`.)_

### The editor

- **`INV-WEB-16`** <!-- uuid: dd3ac033-fac7-42ae-ac7b-6b84211d29c2 --> — The editor MUST list events in the corrected view with the original one action away,
  flag a corrected event and a retracted one, and offer to correct or retract, and MUST NOT offer to
  delete. A retraction control MUST state, beside it, that it is refused once later events depend on its
  target; an event that is a member of a batch MUST be retracted only as a whole batch; and a refusal that
  names events MUST link each of them into the editor. _(Realizes `INV-CORR-8` to `INV-CORR-13`,
  `INV-CORR-16`.)_
- **`INV-WEB-17`** <!-- uuid: 05270498-d500-4935-b30b-41a9b33e045b --> — "Insert break (from, to)" and "End at (time)" MUST read the times in a zone the page
  names, show a before-and-after timeline of the cycle's running segments with the date and the zone
  before the operator confirms, and say that the service checks the change when it is confirmed; the
  refusal, with its own sentence, MUST leave nothing changed. _(Realizes `INV-CORR-15`, `INV-CORR-17`.)_
- **`INV-WEB-18`** <!-- uuid: 57cb4d16-7fc2-48df-a9c8-f1e63a65105d --> — A correction MUST be able to change an event's time and its editable fields, including
  the type of a cycle, chosen from the configured types, with the title following from the service and
  never typed in the page; an identity field MUST NOT be offered. _(Operator ruling, 2026-10-08, decision
  log row 37.)_

### Deep links

- **`INV-WEB-19`** <!-- uuid: 291f3e3c-8b87-4964-b601-c7200ce2150a --> — A link ending in `#/tasks/<id>` or `#/cycles/<id>` MUST open the page at that task or
  cycle, marked and focused, and a link to something that is no longer in the current state MUST show its
  history from the log; an unknown id MUST be said in words and MUST NOT blank the page. The page MUST
  keep working when the link is followed while another view is open. _(Operator ruling, 2026-10-07,
  decision log row 27.)_

### Accessibility and privacy

- **`INV-WEB-20`** <!-- uuid: fc61bb24-016f-4003-8e9d-86ca7e314623 --> — Every action MUST be reachable by the keyboard with a visible focus; a dialog MUST trap
  and restore focus and close with the escape key; expiry, overtime, the date-moved-on banner, the
  READ-ONLY banner, the Undo and an error MUST be announced through live regions, and the timer MUST NOT
  be announced every second; motion MUST respect the user's reduced-motion setting; and nothing MUST be
  conveyed by colour alone.
- **`INV-WEB-21`** <!-- uuid: d08bc753-80e6-4954-be94-52c5ee750180 --> — Every time the operator types MUST be read in a zone the page names beside the input
  (the active day period's zone), converted by the rules of `INV-TIME-6` and `INV-TIME-7` (a time that does
  not exist resolves to the first valid instant after the gap, a time that occurs twice to its earlier
  occurrence); every time the page shows MUST carry its zone.
- **`INV-WEB-22`** <!-- uuid: d156371e-d4f5-45c9-8790-8d9957a2f2b7 --> — The page MUST NOT emit telemetry, write to the console, use browser storage, a beacon
  or a cookie, or request anything but the service's own API and assets; free text the operator types
  (a reason, a note, a label, a value) MUST leave the page only as the body of a request to the service,
  and MUST NOT appear in a URL. The page has no tracing of its own; the trace id of a request comes from
  the service. _(Extends `INV-OBS-3` to the web client; the privacy rule of the design.)_

## Scope

**Extent (in)** — the page the service serves, its header, checklists, cycle panel, editor, period and
profile modals, the deep-link routes, what it says when the service refuses or cannot be reached, how it
keeps time, and how it keeps the operator's words from logs and telemetry.

**Extent (out)** — the rules each request obeys (the other docs of this set), the visual design beyond what
is stated here, the strings of the other clients, and the reverse proxy.

**Floor** — the set speaks in what the page shows, offers and sends. It never names a module, a function or
a framework, and it quotes only the strings that other clients must match: the three roll-over banners and
the READ-ONLY sentence.
