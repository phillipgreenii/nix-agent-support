# pg-task-focus — the connector backend

The service exports two reads for other tools: the cycles as calendar events, and what needs the
operator's attention. One small program, `pg-connector-calendar-task-focus`, turns those two reads
into the wire format of the operator's connector (`pg-connector`), so the rest of the operator's
tools see focus work beside their mail, tickets and pull requests without knowing this product
exists. This doc owns what that program does and promises. What the service answers is in
[`service.md`](service.md) (`INV-SVC-14`); the rules of the connector's own Tier-1 and Tier-2
contract are in `packages/pg-connector/docs/behavior` and are not restated here.

The backend is an adapter and decides nothing about focus guidance: every segment, severity and
link it reports is the service's, and the backend's own promises are about translating them
faithfully and about failing visibly.

## Stories

- **`STORY-CONN-CALENDAR`** <!-- uuid: d56c7ae0-4fb6-49c7-9327-2b3036bb1711 --> — As the operator who tracks work in another tool, I want my focus
  cycles to show up there as calendar events that mean the time I actually worked, with the tickets and
  note I attached, so the work tracker needs no second entry and a pause across lunch is not counted as
  work. _(→ `JOURNEY-CONN-CALENDAR`; `INV-CONN-2`, `INV-CONN-3`, `INV-CONN-4`, `INV-CONN-5`,
  `INV-CONN-6`.)_
- **`STORY-CONN-ATTENTION`** <!-- uuid: 98eb515c-a098-4cd5-a028-bd86935476ff --> — As the operator, I want an overdue task, a cycle in overtime and a
  cycle paused too long to appear in the same attention list as everything else I must look at, with a
  link that opens the item, so I do not have to remember to open a separate window.
  _(→ `JOURNEY-CONN-ATTENTION`; `INV-CONN-7`.)_
- **`STORY-CONN-HONEST`** <!-- uuid: ab3b3241-c27f-459b-9482-94eea9e5e52a --> — As the operator, I want a service that is not running, not ready or
  unable to write to be reported as exactly that, never as "nothing to do", so a silent failure is never
  mistaken for a clear day. _(→ `JOURNEY-CONN-DOWN`; `INV-CONN-8`, `INV-CONN-9`, `INV-CONN-10`.)_

## Journeys

### `JOURNEY-CONN-CALENDAR` — a cycle with a pause becomes two events <!-- uuid: cf93c846-9626-43e7-a91a-5a388efd75c0 -->

**Primary actor:** `ACTOR-CLIENT` (the connector's consumer, for example a work tracker).
**Other actors:** `ACTOR-OPERATOR`. _Requires:_ `INV-CONN-2`, `INV-CONN-3`, `INV-CONN-4`,
`INV-CONN-5`, `INV-CONN-6`. _Includes:_ none.

1. The operator ran a deep-work cycle from 09:00 to 10:00, paused it until 13:00 and ran it to 13:30,
   with two ticket pairs and a note.
2. The consumer asks the connector for the calendar events of the day. The backend answers two events
   in the fixed calendar: 09:00 to 10:00 and 13:00 to 13:30, each titled with the cycle type's title
   (`INV-CONN-2`, `INV-CONN-3`).
3. Each event's notes start with the cycle's type, then the ticket pairs in the order entered, then a
   line that is exactly `---`, then the note; the consumer reads the pairs up to the first such line
   (`INV-CONN-4`).
4. The cycle is still running at 14:00. The consumer asks for the running cycle; the backend answers the
   open segment, ending at the time of the read (`INV-CONN-3`, `INV-CONN-5`).

Extensions:

- 2a. The operator later back-fills a break inside the first segment. The next read answers three
  events, and the second half carries a different id; the consumer replaces what it held rather than
  appending (`INV-CONN-6`).
- 2b. The consumer asks for a calendar by a name other than the fixed one. The answer is a successful
  empty list (`INV-CONN-5`).

### `JOURNEY-CONN-ATTENTION` — the feed follows the state <!-- uuid: acc4af9b-6eca-4e7c-a26e-06642d2f82ca -->

**Primary actor:** `ACTOR-CLIENT`. **Other actors:** `ACTOR-OPERATOR`. _Requires:_ `INV-CONN-7`.
_Includes:_ none.

1. A daily task is overdue and a cycle is twelve minutes over. The consumer asks for the attention
   list. The backend answers an item for each, with the service's severity, group and link
   (`INV-CONN-7`).
2. The operator completes the task and stops the cycle. The next read answers neither: the feed holds
   an item exactly while its condition holds, and nothing in the backend remembers it (`INV-CONN-7`).

### `JOURNEY-CONN-DOWN` — the service is not there, or cannot write <!-- uuid: 155c09bc-33cf-4194-b063-24dfe6c971f0 -->

**Primary actor:** `ACTOR-CLIENT`. **Other actors:** `ACTOR-OPERATOR`, `ACTOR-HOST`. _Requires:_
`INV-CONN-1`, `INV-CONN-8`, `INV-CONN-9`, `INV-CONN-10`. _Includes:_ none.

1. The service is stopped. The consumer asks for the attention list. The backend answers that the
   source is unavailable and says the service may not be running; the consumer reports a degraded
   source, never an empty one (`INV-CONN-8`).
2. The call also leaves a start row and a final row in the backend's own log, naming where it broke, so
   it can be explained later (`INV-CONN-1`).
3. The service is running but its store has become read-only. The backend answers the list with a
   high-severity item that says the service is read-only and that restarting it is the recovery
   (`INV-CONN-9`).
4. The operator restarts the service; the next read has no such item (`INV-CONN-9`).

## The connector interface

### `INTF-CONN` — the connector's backend contract <!-- uuid: fab0edb1-dfcf-4c4b-b85c-a032823e6c98 -->

**Counterparty:** `ACTOR-CLIENT` (kind: actor; optional participant: the product runs untouched
without a connector). **What crosses in:** one request per launch, from the connector's umbrella: a
calendar window (or a named look-ahead query) with an optional calendar name, or a request for the
attention list, each carrying the backend's own configuration block. **What crosses out:** one
response per launch in the connector's wire format: calendar events, attention items, or one error from
the connector's closed taxonomy. **What must hold:** `INV-CONN-1` to `INV-CONN-12`.

## Invariants

### Process model

- **`INV-CONN-1`** <!-- uuid: 9a453cf5-fae4-415a-8f1b-cae0f8c096af --> — The backend MUST be launched once per request: it MUST keep no state between
  requests, and it MUST NOT stay running between them. It MUST expose no metrics of its own; its use
  is the service's own count of requests from a client of the connector kind, and an unreachable
  service is visible as an unavailable source in the connector's fan-out. It MUST write a start row
  and a final row for every request to a log it owns, in the operator's JSONL logging standard, with a
  heartbeat row while a request runs long, so that a request the umbrella ends at its deadline is
  still attributable; the final row MUST say how many requests the service answered, the last status
  and, for a failure, where it broke. Writing the log MUST be best effort and MUST NOT change the
  response. _(Decision, bead `pg2-t7me1.4`, 2026-10-10: the design left open whether the backend is
  launched per request or resident. Per request, because the connector's wire protocol is exactly one
  request on input and one response on output per launch, so a resident backend would need a second
  transport the protocol does not define; because the service is already the one resident process, so a
  second would add a process to supervise and a second place state could drift; and because keeping the
  adapter stateless keeps `INV-SVC-14`'s stateless feed stateless end to end. It costs one process
  start per request, which the connector's per-request budget absorbs and the service's loopback
  answer makes small. The operator MAY overrule it; nothing else in this set depends on it.)_

### Calendar

- **`INV-CONN-2`** <!-- uuid: 8c62cda5-7d63-45df-9682-27b058d50b6d --> — Every event the backend reports MUST be one running segment of a cycle, never
  a whole cycle, because a calendar event means time occupied and a pause across a break is not work.
  Its identity MUST be the service's: the id of the event that opened the segment. Its title MUST be the
  cycle type's title, its start and end the segment's bounds as instants in UTC, its calendar the fixed
  calendar of the service, and its as-of time the service's time of the read. _(Realizes `INV-SVC-14`
  for the connector; decision log row 23.)_
- **`INV-CONN-3`** <!-- uuid: 1bd6aec0-b1ee-428f-9630-03f893d4d209 --> — An open segment MUST end at the time of the read, and a segment of zero length
  MUST NOT be reported; both are the service's answer and the backend MUST NOT recompute them. A read
  that succeeded MUST be marked as not stale; an event with no as-of time MUST be marked stale rather
  than carry a meaningless one. There is no fallback to an earlier answer.
- **`INV-CONN-4`** <!-- uuid: 394203ba-452f-4339-b94d-d1b7a6ab6188 --> — An event's notes MUST be the service's notes block, byte for byte: the
  reserved `cycle_type` pair first, the operator's pairs one per line in the order entered (a repeated
  key is several values), a line that is exactly `---`, then the note, which MAY itself contain such a
  line. The backend MUST NOT parse, reorder, trim or rewrite it; a consumer parses it with the rules of
  `INV-SVC-14`. _(Operator's key/value ruling, decision log row 14.)_
- **`INV-CONN-5`** <!-- uuid: d8f32bce-14d1-4b04-8993-d352dd690857 --> — A request for the events of a window MUST report the segments that overlap it,
  for all calendars when no calendar is named and for the fixed calendar when it is named by either its
  id or its name; any other name MUST be a successful empty answer. A request for a named look-ahead
  query takes durations, MUST use the largest, and MUST report the segments overlapping the window from
  now to now plus that duration, which can only be the open segment of the running cycle because
  nothing exists in the future. A duration that does not parse, is not positive, or an empty query, and
  a window that is empty or reversed, MUST be an invalid-argument error, answered without asking the
  service. A named query the configuration does not define MUST be the connector's query-not-recognized
  answer.
- **`INV-CONN-6`** <!-- uuid: f13b5e41-43b2-4246-8584-1ecefd0fe720 --> — A consumer MUST treat the events as a snapshot of the corrected view of the
  log, not as an append-only stream: a correction that inserts a break splits a segment into two with
  different ids, and a retraction removes one. The backend MUST NOT remember what it reported earlier.
  _(Decision log row 23.)_

### Attention

- **`INV-CONN-7`** <!-- uuid: 72ed7cc7-f469-42c7-bb23-07527d151441 --> — The attention list MUST be the service's feed, item for item: each item's type,
  id, summary, group and link as the service built them, in a feed that holds an item exactly while
  its condition holds. Severity MUST be the service's own (`low`, `medium` or `high`); a severity
  outside that set, or none, MUST be left out and never defaulted. An item without a type or an id
  makes the whole answer a malformed one (`INV-CONN-8`). The backend MUST offer no way to acknowledge,
  hide or snooze an item: resolving the condition is the only way an item goes. _(Realizes
  `INV-SVC-14` and `INV-CYCLE-7` for the connector.)_

### Failure

- **`INV-CONN-8`** <!-- uuid: d4d8a5a5-2ed1-454d-b281-fd46bc6e3fb6 --> — A service that cannot be reached, is not ready, answers an error status or
  answers a body the backend cannot read MUST be the connector's unavailable error, with a message
  that names the service and, when it could not be reached, says it may not be running; it MUST NOT
  be an empty answer, because "unknown" and "nothing" have to be told apart. A request the service
  refuses as malformed MUST be an invalid-argument error. The backend MUST NOT retry, cache or
  substitute an earlier answer.
- **`INV-CONN-9`** <!-- uuid: 4de56e73-d14f-4df7-a22d-3ea2303120ef --> — While the service reports its store read-only, the attention list MUST contain
  a high-severity item of type `store` that says the service is read-only and names restarting it as
  the recovery, and the backend MUST make sure the wording does so even when the service's own summary
  does not; the item MUST be gone once a restart has cleared the mode. Reads of the calendar MUST keep
  working. _(Realizes `INV-LOG-22` for the connector; operator ruling, 2026-10-08, epic ruling 7:
  "make sure it is obvious that we are in read-only mode".)_
- **`INV-CONN-10`** <!-- uuid: 4f63f82d-f219-4642-930c-0d3376de9418 --> — The backend has no credential to check: it MUST NOT answer the connector's
  authentication-status request, so that the connector reports it as not applicable.

### Configuration and registration

- **`INV-CONN-11`** <!-- uuid: 2742d09c-4b18-4c3c-8e40-5ded2865cc2a --> — The backend MUST find the service at the address in its configuration block
  when there is one, else at the address the command-line client reads from its environment, else at
  the service's documented default loopback address; an address that is not an HTTP(S) URL or a host and
  port MUST be an invalid-argument error. A deployment that changes the service's listening port MUST
  keep the two in step; the home module that registers the backend does so by deriving the address from
  the port. Each request MUST say it comes from a client of the connector kind, and MUST carry the
  caller's trace context when one is given. The backend MUST depend only on the service's documented
  HTTP contract: it MUST NOT read the log or the service's files, and it MUST NOT share code with the
  service.
- **`INV-CONN-12`** <!-- uuid: 8a0fd3ed-69be-4cb8-8fd9-8d9c53d0f47b --> — The backend is registered as a calendar backend and, independently, as an
  attention source; it MUST answer both, and the registration MUST declare at least one named calendar
  query (a list of look-ahead durations), because the connector resolves a named calendar query from the
  backend's configuration. The attention rules of the connector's calendar capability that rank calendar
  events by their start (`INV-CAL-2` to `INV-CAL-6` in `packages/pg-connector/docs/behavior`) bind a
  backend that reports calendar events as attention items; this backend reports none, so they do not
  apply: its attention items are the service's tasks and cycles (`INV-CONN-7`).
