# pg-task-focus — time and zones

Ambiguous time is the dominant source of defects in a scheduler, so `pg-task-focus` forbids implicit
zones. This doc owns what an instant is, what a zone name is, how a wall-clock time that does not
exist or happens twice is resolved, which zone decides what, and how a zone is found for a client
that defaults it. The event log's use of instants is in [`event-log.md`](event-log.md); the
periods that carry a zone are in [`periods-and-rollover.md`](periods-and-rollover.md).

## Stories

- **`STORY-TIME-DUE`** <!-- uuid: 92b3485b-3906-44c2-920b-1c47fc100f41 --> — As the operator, I want each task's due time to be a
  real instant resolved in a zone I named, so "09:30" means the same thing on every machine and every
  day of the year. _(→ `JOURNEY-TIME-TRANSITION`; `INV-TIME-1`, `INV-TIME-2`, `INV-TIME-6`,
  `INV-TIME-7`, `INV-TIME-8`.)_
- **`STORY-TIME-ZONES`** <!-- uuid: 52e3ea56-86aa-4375-b350-6c68e76d72cd --> — As the operator, I want a mistyped zone to be caught
  when I write it, and a zone that quietly ignores daylight saving to be called out, so a due time is
  never an hour off without my knowing. _(→ `JOURNEY-TIME-ZONE-NAME`; `INV-TIME-3`, `INV-TIME-4`,
  `INV-TIME-5`.)_
- **`STORY-TIME-TRAVEL`** <!-- uuid: 5ae0d5db-460f-4c6a-aa0d-773e1b93d54e --> — As the operator, I want "today" to follow the zone my
  day was set up in, and a client that guesses my zone from the machine to fail loudly rather than
  guess, so the "the date has moved on" banner is right. _(→ `JOURNEY-TIME-HOST-ZONE`; `INV-TIME-8`,
  `INV-TIME-9`, `INV-TIME-10`.)_

## Journeys

### `JOURNEY-TIME-TRANSITION` — a due time on the day the clocks change <!-- uuid: 451befa8-7b1c-4cd4-bd75-2744fe12176b -->

**Primary actor:** `ACTOR-OPERATOR`. **Other actors:** `ACTOR-HOST`, `ACTOR-CONFIGURER`.
_Requires:_ `INV-TIME-1`, `INV-TIME-2`, `INV-TIME-6`, `INV-TIME-7`, `INV-TIME-8`. _Includes:_ none.

1. A task is due at 02:30 in `America/New_York`. On the spring day the clocks go from 02:00 to 03:00
   there is no 02:30.
2. The due time resolves to the first valid instant after the gap: 03:00 local time on that day
   (`INV-TIME-6`). It is not shifted by the length of the gap.
3. On the autumn day the clocks go back, a due time of 01:30 happens twice and resolves to the
   earlier occurrence (`INV-TIME-7`).
4. Every client shows the due time together with its zone identifier (`INV-TIME-8`).

Extensions:

- 2a. A zone skips a whole civil date (a zone moved across the date line). A due time on the skipped
  date resolves to the first valid instant after the skipped day, which is midnight of the next date
  that exists (`INV-TIME-6`).
- 2b. A zone's gap is half an hour, or begins exactly at midnight. The same rules apply.

### `JOURNEY-TIME-ZONE-NAME` — writing a zone into the configuration <!-- uuid: 06ef62ad-33d8-47eb-9870-5d7da65c4b45 -->

**Primary actor:** `ACTOR-CONFIGURER`. **Other actors:** `ACTOR-OPERATOR`.
_Requires:_ `INV-TIME-2`, `INV-TIME-3`, `INV-TIME-4`, `INV-TIME-5`. _Includes:_ none.

1. The configurer writes `America/New_York` as a due rule's zone. It is accepted.
2. They try `PST`. It is rejected because the standard library does not know that name, and nothing
   is stored (`INV-TIME-3`). An empty zone, `Local` and a bare offset like `-05:00` are rejected too.
3. They try `EST`. It is accepted, but it is a fixed offset with no daylight saving: in July `EST` is
   UTC-5 while `America/New_York` is UTC-4 (`INV-TIME-4`). The documentation recommends the region
   name, and the configurer switches to it.

### `JOURNEY-TIME-HOST-ZONE` — a client defaults the zone from the machine <!-- uuid: aea76dfa-c452-4bad-8f29-1c236ac00c70 -->

**Primary actor:** `ACTOR-CLIENT`. **Other actors:** `ACTOR-HOST`, `ACTOR-OPERATOR`.
_Requires:_ `INV-TIME-8`, `INV-TIME-9`, `INV-TIME-10`. _Includes:_ none.

1. The operator changes the day from a command-line client that defaults the zone from the machine.
2. The client reads the `TZ` environment variable, dropping one leading `:`; if that is not a valid
   zone name it follows the machine's local-time link and takes the zone name after `zoneinfo/`
   (`INV-TIME-10`).
3. The new day period stores that zone, which decides only which civil date "today" is
   (`INV-TIME-9`).

Extensions:

- 2a. Neither yields a valid zone name (`TZ` is `Local`, or the link names no zone). The client
  fails with a message naming both inputs; it never guesses (`INV-TIME-10`).

## The host interface

### `INTF-HOST` — the clock, the zone rules and the machine's zone hints <!-- uuid: f72907a8-bf67-4293-81a5-f726515a50ac -->

**Counterparty:** `ACTOR-HOST` (kind: actor; essential participant: no instant or civil time can
be computed without it). **What crosses in:** the current instant (injected, so no behavior depends on
real time), the zone rules for a zone name, and the machine's own zone hints (`TZ`, the local-time
link). **What must hold:** `INV-TIME-1` to `INV-TIME-10`.

## Invariants

- **`INV-TIME-1`** <!-- uuid: 4297917d-7141-4019-95df-40ddc9527fa9 --> — Every stored instant MUST be an RFC 3339 timestamp in UTC
  with millisecond precision, for example `2026-10-07T13:30:00.000Z`.
- **`INV-TIME-2`** <!-- uuid: 3a8a47b7-a480-482f-889b-862b6ce24dcb --> — Every computation that depends on a civil date or a
  wall-clock time MUST name a zone. There is no default zone anywhere: not in the configuration, not
  in a period, not in a request.
- **`INV-TIME-3`** <!-- uuid: 645e0ec3-b05a-4d19-8e06-e159edbf8ea9 --> — A zone name MUST be valid exactly when the Go standard
  library's zone lookup resolves it in its exact letter case (`america/new_york` is rejected even
  where the host's file system would accept it). Two inputs the lookup resolves are still rejected,
  because they are escapes to an implicit zone and not zone names: the empty string (which is UTC)
  and `Local` (the host's zone). Names the standard library does not know, such as `ET`, `PST`, a
  bare offset like `-05:00` or `+0530`, and `UTC-5`, are rejected for that reason alone. A rejected
  name is refused as `invalid_zone` at configuration load and at request input. _(Operator ruling,
  2026-10-08: "D12, what is in the standard library is fine.")_
- **`INV-TIME-4`** <!-- uuid: 6025ef31-69d8-4c92-93cb-f930cce582ef --> — The legacy names the zone database carries (`EST`, `MST`,
  `EST5EDT`, `PST8PDT`) are accepted, but `EST` and `MST` MUST be documented as fixed offsets with no
  daylight saving rule: in July `EST` is UTC-5 while `America/New_York` is UTC-4. The documentation
  MUST recommend region names such as `America/New_York`. No test or doc pins what offsets a legacy
  name carries, because they differ across zone database releases.
- **`INV-TIME-5`** <!-- uuid: 20a0fd9f-f1f1-4329-83b4-287140068fe6 --> — Zone data MUST come from the standard library's embedded zone
  database, so a lookup works on a host with no usable zone files; the host's own zone files win when
  they are present. No zone database is committed to the repository. Behavior pinned in tests MUST be
  limited to transitions that are stable across zone database releases. _(Operator ruling, 2026-10-08:
  "we can use libraries which handle timezones, no need to pull in a zip.")_
- **`INV-TIME-6`** <!-- uuid: 5825e89a-1e09-4c50-bf58-68bd923c09a2 --> — A civil time that does not exist (skipped when clocks go
  forward) MUST resolve to the first valid instant after the gap: with a 02:00 to 03:00 gap, `02:30`
  resolves to `03:00` local time. A civil date that a zone skips entirely MUST resolve to the first
  valid instant after the skipped day. This MUST be implemented and tested explicitly, because the
  standard library does not guarantee it.
- **`INV-TIME-7`** <!-- uuid: 939f7e6c-060a-4b03-8d8b-aec291c42bf7 --> — A civil time that occurs twice (repeated when clocks go
  back) MUST resolve to its earlier occurrence, implemented and tested explicitly.
- **`INV-TIME-8`** <!-- uuid: 55b49c89-1686-4ac3-a1fb-491e42ce7b68 --> — A period's dates are civil dates with no zone. A due rule
  resolves a civil date of its period in the rule's own zone; a rule whose zone differs from the
  period's zone is legal. Every client MUST show every due time together with its zone identifier.
- **`INV-TIME-9`** <!-- uuid: 1b0a1bb7-4031-4d5a-b25b-001e55a307a7 --> — A period MUST store the zone in force when it was created,
  and that zone decides one thing: which civil date "today" is, for the "the date has moved on"
  banner. "Today" is therefore read in the period's zone, never the machine's zone. A request that
  changes a period MUST supply that zone.
- **`INV-TIME-10`** <!-- uuid: d2aba69e-0662-4f67-8a98-a4ac58cc80f1 --> — A client that defaults the zone from the machine MUST read
  the `TZ` variable (valid if it is a valid zone name after one leading `:` is dropped) or the target
  of the machine's local-time link (the zone name is the path after `zoneinfo/`; on macOS the link
  points into the system zone directory), and MUST fail, naming both inputs, if neither yields a valid
  zone name. A web client SHOULD default the zone from the browser. No client guesses.
