# pg-task-focus — the service and its API

The core library is served by one long-running service: the single writer of the log, the one place
every client reads state from, and the process that plays the overtime sound. This doc owns what the
service does as a running thing: how it starts, stays ready, reloads its configuration and stops, the
API every client uses and the rules that keep it safe to expose to a browser, the stream that tells
open clients something changed, and the sound and notification at the end of a cycle. The rules the
service enforces on every request are in [`event-log.md`](event-log.md), [`periods-and-rollover.md`](periods-and-rollover.md),
[`cycles-and-timer.md`](cycles-and-timer.md) and [`corrections.md`](corrections.md); the configuration it
reads is in [`configuration.md`](configuration.md); what it exports to be observed is in
[`observability.md`](observability.md); the command-line client is in [`command-line.md`](command-line.md).

## Stories

- **`STORY-SVC-ONE-API`** <!-- uuid: 3f37e210-5dc4-4225-aa54-418fbfcbd277 --> — As the operator, I want every client I use (a terminal, a browser tab, a
  menu-bar item, another tool) to talk to one running service through one documented interface, so a
  change made in one place is seen in all of them at once and no client has a private idea of the
  state. _(→ `JOURNEY-SVC-STARTUP`, `JOURNEY-SVC-BROWSER`; `INV-SVC-1`, `INV-SVC-5`, `INV-SVC-6`,
  `INV-SVC-7`, `INV-SVC-8`, `INV-SVC-14`, `INV-SVC-15`.)_
- **`STORY-SVC-SAFE-IN-A-BROWSER`** <!-- uuid: b72592c4-2b77-47aa-baef-9bf1ca95aaf6 --> — As the operator who also browses the web, I want a page I visit
  to be unable to reach or alter my routine through the service, so a local service with no password
  is still safe to run. _(→ `JOURNEY-SVC-BROWSER`; `INV-SVC-1`, `INV-SVC-2`, `INV-SVC-3`,
  `INV-SVC-4`.)_
- **`STORY-SVC-START-AND-RELOAD`** <!-- uuid: 81d546d6-2ba1-4868-893a-e5bd2b10f10b --> — As the operator, I want the service to refuse to start on a log or a
  configuration it cannot trust, say exactly why, come up ready only when it can serve, and take a
  corrected configuration without a restart, so a mistake in either is found at once and never half
  applied. _(→ `JOURNEY-SVC-STARTUP`, `JOURNEY-SVC-RELOAD`; `INV-SVC-9`, `INV-SVC-10`, `INV-SVC-11`,
  `INV-SVC-16`.)_
- **`STORY-SVC-HEAR-THE-END`** <!-- uuid: 574f4054-950b-4731-8675-e02d4fc2a747 --> — As the operator deep in work, I want to hear a short sound and see a
  notification when a cycle runs out and at every reminder, with no browser open, so I notice without
  watching a timer. _(→ `JOURNEY-SVC-OVERTIME`; `INV-SVC-12`.)_
- **`STORY-SVC-KNOW-WHEN-READ-ONLY`** <!-- uuid: 97963693-35a8-4557-a67d-47c49b75e402 --> — As the operator, I want a service that can no longer write to say
  so loudly in every place I look, and still let me read, so a failed disk is never mistaken for a
  lost action. _(→ `JOURNEY-SVC-READONLY`; `INV-SVC-13`.)_

## Journeys

### `JOURNEY-SVC-STARTUP` — from launch to serving <!-- uuid: 629a20d7-bb33-4e3e-b72f-3e0c5c841a35 -->

**Primary actor:** `ACTOR-OPERATOR`. **Other actors:** `ACTOR-HOST`, `ACTOR-CLIENT`,
`ACTOR-CONFIGURER`. _Requires:_ `INV-SVC-1`, `INV-SVC-9`, `INV-SVC-10`, `INV-SVC-17`. _Includes:_ none.

1. The host launches the service. It reads and validates the configuration, binds the loopback port
   and answers health and metrics requests at once (`INV-SVC-9`).
2. It takes the exclusive claim on the data directory, recovers the end of the log, replays it and
   checks that the profile the log says is active is one the configuration defines (`INV-SVC-10`).
3. The first check that the data directory can take a write passes. The service is ready, and every
   other request is now answered (`INV-SVC-9`).
4. The host asks the service to stop. It drains requests, releases the data directory and flushes its
   telemetry (`INV-SVC-17`).

Extensions:

- 2a. The log is corrupt, has an unknown event version, or another service holds the data directory.
  The service refuses to start, logs the cause (with the line number for a corrupt log) and exits
  non-zero (`INV-SVC-10`).
- 2b. The configuration no longer defines the log's active profile. The service refuses to start and
  says which profile, which ones the configuration defines and how to recover (`INV-SVC-10`).
- 3a. The write check fails. The service stays not ready and says which check is failing; it keeps
  checking, and becomes ready when one passes (`INV-SVC-9`).

### `JOURNEY-SVC-RELOAD` — editing the configuration while it runs <!-- uuid: 15227f23-a14c-4c82-bf25-2b3f41c04df5 -->

**Primary actor:** `ACTOR-CONFIGURER`. **Other actors:** `ACTOR-OPERATOR`, `ACTOR-HOST`.
_Requires:_ `INV-SVC-11`. _Includes:_ none.

1. The configurer changes the configuration file and asks the service to reload.
2. The service validates the new document. It is valid, so it replaces the old one for every later
   read and alert, and the state version changes so open clients refetch (`INV-SVC-11`).
3. The service reports the new configuration's digest and the time it was loaded.

Extensions:

- 2a. The document is invalid, or removes the profile that is active. The service keeps the previous
  configuration, reports the problems by their paths in the document and never repeats a configured
  value, and the operator sees the configuration is not the latest (`INV-SVC-11`).
- 2b. The new document changes the listening port or the public URL. The reload is accepted, and the
  service reports that those two take effect only on restart (`INV-SVC-11`).

### `JOURNEY-SVC-BROWSER` — a page that is not the operator's tries the service <!-- uuid: bb96b532-7a89-4ca3-a283-3804f774e5db -->

**Primary actor:** `ACTOR-CLIENT`. **Other actors:** `ACTOR-OPERATOR`. _Requires:_ `INV-SVC-1`,
`INV-SVC-2`, `INV-SVC-3`, `INV-SVC-4`, `INV-SVC-5`. _Includes:_ none.

1. The operator opens a page from another site. Its script requests the service by name, or through
   a hostname that has been re-pointed at the loopback address.
2. The service sees a Host that is not on its list and refuses the request, on every route
   (`INV-SVC-2`).
3. The script tries a cross-origin request: the Origin is not on the list, or is `null`, and the
   request is refused; no permission header is ever sent that would let the browser read an answer
   (`INV-SVC-3`).
4. A form post without the JSON content type is refused before its body is read (`INV-SVC-3`).
5. Each refusal is a problem document with its own reason, so the operator's own tools can tell a
   defence from a defect (`INV-SVC-5`).

### `JOURNEY-SVC-OVERTIME` — the sound and the notification <!-- uuid: 34af7994-d2fc-477c-b650-ee275542ab55 -->

**Primary actor:** `ACTOR-OPERATOR`. **Other actors:** `ACTOR-HOST`. _Requires:_ `INV-SVC-12`.
_Includes:_ none.

1. A cycle's time is up while the operator works with no browser open. The service plays the cycle
   type's sound once and sends one notification (`INV-SVC-12`).
2. Each further interval of running time in overtime plays the reminder sound and sends a
   notification, until the operator stops the cycle (`INV-SVC-12`).

Extensions:

- 1a. The cycle's type is configured with no sound. The notification is still sent and the reminders
  still repeat (`INV-SVC-12`).
- 1b. The sound cannot be played, or the notification cannot be shown. The failure is logged and
  counted and the cycle is untouched (`INV-SVC-12`).

### `JOURNEY-SVC-READONLY` — the disk fails <!-- uuid: 4895714a-3fd9-42bb-bffd-62f63e050057 -->

**Primary actor:** `ACTOR-OPERATOR`. **Other actors:** `ACTOR-HOST`, `ACTOR-CLIENT`.
_Requires:_ `INV-SVC-13`. _Includes:_ none.

1. An append cannot be made durable and the file cannot be restored; the store becomes read-only
   (`INV-LOG-21`).
2. The service's state and health now carry the mode, its reason and since when; an open client is
   told at once; the attention feed gains a high-severity item; the metrics and an alert say so
   (`INV-SVC-13`).
3. Every change is answered `store_unavailable` with the READ-ONLY sentence. Reads keep working
   (`INV-SVC-13`).
4. The operator restarts the service; the mode, its item and its alert are gone (`INV-SVC-13`).

## The API interface

### `INTF-API` — the one interface of the service <!-- uuid: 1209de00-b443-41d8-ad3f-7aa186612088 -->

**Counterparty:** `ACTOR-CLIENT` (kind: actor; essential participant: without a client the service
has no operator). **What crosses in:** the requests of `INTF-REQUEST` over HTTP with JSON bodies,
reads of the state, the event listing of the editor, the resolved configuration, the calendar and
attention reads for the connector, and the ops reads (liveness, readiness, metrics); each client
SHOULD say which kind of client it is and MAY carry a trace context. **What crosses out:** the
documents of the API contract, a problem document for every refusal, and a stream of change events.
**What must hold:** `INV-SVC-1` to `INV-SVC-8`, `INV-SVC-13`, `INV-SVC-14`, `INV-SVC-15`. The contract
is written first, as one OpenAPI 3.1 document in the package, and is the authority on every path,
body and status; a test fails when a route is missing from it or a path in it has no route, and
every response the service sends in the tests is checked against it.

## Invariants

### Exposure

- **`INV-SVC-1`** <!-- uuid: 4721952d-3897-464f-9026-cb08eaf85671 --> — The service MUST listen on the loopback address only, on the
  configured port, and MUST work with no reverse proxy in front of it. It uses no authentication
  token: it is single-user and loopback-only, and that is a recorded decision. Machine clients use
  the loopback address directly, so the timer controls keep working when a proxy is down.
- **`INV-SVC-2`** <!-- uuid: 14a16862-5035-4c65-b020-e85369c4c8cb --> — The service MUST require a `Host` header from an allowlist on
  every route, health, metrics and the stream included, and refuse any other as `forbidden_host`
  (403). The allowlist is the loopback address and `localhost`, each with the configured port, and
  the host of the configured public URL. This is the defence against re-pointing a hostname at the
  loopback address.
- **`INV-SVC-3`** <!-- uuid: 5acea7e4-de95-4d21-a4f5-97aae90d9c7c --> — The service MUST accept an `Origin` header only from the
  same allowlist's origins and refuse any other, `null` included, as `forbidden_origin` (403); an
  absent `Origin` is permitted. A mutation MUST send the JSON content type even when its body is `{}`,
  otherwise `unsupported_media_type` (415). The service MUST NOT send any cross-origin permission
  header, and a preflight request is a method it does not take.
- **`INV-SVC-4`** <!-- uuid: d232deeb-a115-4b8e-bb8a-2219b419ab92 --> — A request body MUST be refused as `invalid_request` before its
  content is used when it is not valid UTF-8 (the bad bytes are never replaced by a placeholder),
  repeats a key, names a field the endpoint does not take, has data after the object, is empty or is
  longer than the limit. _(Realizes `INV-LOG-27` for the transport.)_

### Contract

- **`INV-SVC-5`** <!-- uuid: 7aee2795-b6b1-4ea0-b8eb-78f58ac4e414 --> — Every refusal MUST be an RFC 9457 `application/problem+json`
  document carrying a `reason` from one closed set, a `trace_id`, a `detail` that is a plain sentence
  naming the entity, the instants and the stored event ids involved, and structured `details`
  (`entity`, `events`, `instants`, the `cycles` of an ambiguous, blocked or conflicting cycle verb,
  and the `store` of a store refusal); every response carries a `traceresponse` header, also when no
  collector is attached. The closed set is the library's catalog plus the reasons only the transport
  can find (`forbidden_host`, `forbidden_origin`, `unsupported_media_type`, `not_found`,
  `method_not_allowed`) and `internal_error` for a defect; there is no catch-all such as an
  "invalid timeline". Statuses keep their HTTP meaning: 400 malformed or ambiguous, 404 not found,
  409 conflict with the present state or other events, 422 well formed but unacceptable for its own
  target, 503 an unavailable store or a service that is not ready. _(Operator rulings, 2026-10-08,
  decision log rows 36 and 43.)_
- **`INV-SVC-6`** <!-- uuid: c9aa9cb5-57a7-4c5c-8ad6-3c577af6b78c --> — Every successful mutation MUST answer whether anything changed,
  the ids of the events appended, its batch id and the state version, so a client can undo it; a
  no-op answers `changed: false` with its one-sentence note and no ids, and an answer replayed from
  an earlier request with the same id says so. A dry run answers what the real request would affect at
  a state version, appends nothing, and the version it names is the one a client carries back as the
  expected version.
- **`INV-SVC-7`** <!-- uuid: 7b3add95-9f15-4f00-841c-419d9d2e94b5 --> — The state a client reads MUST carry, from the library's view
  and nothing a client has to derive: whether the log is initialized, the active profile, each
  current period with its zone, today's date in it and the exact rollover banner once it has ended,
  the tasks of the current periods with due instant and zone, status and whether they are overdue, the
  next task, the focus cycle with elapsed and remaining time (negative in overtime), every dimmed cycle
  with whether it can be switched to, the interrupt stack, the resume offer, the store's health and
  the state version (the pair of log lines and configuration generation, which clients compare only
  for inequality). Durations are whole seconds, instants are UTC with milliseconds, and no field is the
  library's internal shape.
- **`INV-SVC-8`** <!-- uuid: 2444dda5-ac0b-4a2f-be89-395afc54adcf --> — The service MUST offer a stream of server-sent events that
  sends the state version when it connects and whenever it changes (a change committed, or a
  configuration reload), sends the store's state whenever it changes, and sends a comment line at
  least every 15 seconds so a quiet connection stays open. The stream MUST outlive the server's
  ordinary write timeout. A client refetches the state on any event.

### Lifecycle

- **`INV-SVC-9`** <!-- uuid: 6ca861b7-86f6-4943-9375-34b2975f555d --> — Liveness and metrics MUST answer from process start, replay
  included. Until replay has finished and the first check that the data directory can take a write has
  passed, readiness and every API endpoint MUST answer `not_ready` (503) and readiness MUST name the
  failing check. After that, a failing write check MUST NOT stop reads: the service is then degraded or
  read-only and still answers.
- **`INV-SVC-10`** <!-- uuid: 926bd214-7a19-4fc9-a636-17fd9b426bc0 --> — The service MUST refuse to start, logging the cause at Error
  with the line number where there is one and flushing its telemetry before it exits non-zero, when:
  the configuration is invalid; the data directory is held by another service; the log is corrupt, or
  has a torn line or uncommitted batch anywhere but its end; an event has a version it does not know;
  or the profile the log says is active is not defined by the configuration. The last refusal names the
  profile, the profiles the configuration does define and the way out (restore the profile and restart).
  It is the start-time form of the reload rule `INV-SVC-11`, which already refuses to remove the active
  profile, so a state that rule forbids is never entered by restarting. _(Decision of the daemon's
  designer, 2026-10-10, answering the open question the core library left; see the ADR amendment. The
  operator MAY reverse it.)_
- **`INV-SVC-11`** <!-- uuid: ed0138ed-f377-4837-a4dc-22b9cb28dc50 --> — On a reload request the service MUST validate the new
  configuration whole and replace the old one only if it is valid and still defines the active profile;
  otherwise it MUST keep the previous configuration and report, through health and a metric, that the
  configuration is not the latest, naming the problems by their paths in the document and never echoing
  a configured value (the full messages are available offline). A successful reload MUST change the state
  version, MUST apply to every later read and alert, and, when it changes the listening port or the public
  URL, MUST report that those two take effect only on restart.
- **`INV-SVC-16`** <!-- uuid: d1594eb4-b279-4ebc-a655-9e5faba11af8 --> — A defect inside anything the service runs while the engine
  serializes writes (an observer of its work, a callback after a commit) MUST NOT escape: it is logged
  at Error and the rest of that commit's notifications, the push to open clients and the alert poll
  included, still happen. A defect in a request handler MUST answer `internal_error` and keep the service
  serving.
- **`INV-SVC-17`** <!-- uuid: fc74c46b-7f15-4ddd-9781-b282234ca233 --> — On a stop request the service MUST stop taking requests, let
  those in progress finish within a bounded time, release the data directory and flush its telemetry
  before it exits.

### Alerts and read-only mode

- **`INV-SVC-12`** <!-- uuid: a22ed00e-fb61-4c4d-ba8e-c05360ca8cc4 --> — The service MUST play the cycle type's sound once and send one
  notification when a running cycle enters overtime, and MUST play the reminder sound and send a
  notification each time the cycle has run a further interval of running time in overtime, until it is
  stopped. A paused cycle is silent and a resumed one continues its count. Notifications are sent
  only for overtime; due and overdue tasks reach the operator through the state and the attention feed.
  The service asks the alert scheduler what is due after every committed change and every configuration
  reload, and at the instant the scheduler names, and at least every half minute so a sleeping machine
  catches up with at most one alert. A type configured with no sound still sends and repeats the
  notification. A failure to play or to notify MUST be logged and counted and MUST NOT affect cycle
  state. There is no acknowledge, mute, snooze or cap. _(Operator rulings, 2026-10-07 and 2026-10-08,
  decision log rows 7 and 29; operator, 2026-10-09: a missed or extra sound under rare circumstances
  is not a problem.)_
- **`INV-SVC-13`** <!-- uuid: 6a769202-2640-40d9-98cc-dff9d8f3ab8f --> — While the store is read-only the service MUST carry the mode
  (its state, its closed reason sentence and since when) in the state and in health; answer every change
  `store_unavailable` (503) with the READ-ONLY sentence and the same store object; tell open streams at
  once; add a high-severity item to the attention feed that disappears when a restart clears the mode;
  report it as a metric and an alert; and keep answering every read. A retry after an unknown outcome is
  answered from the record of request ids before the read-only check, so a request whose events are
  durable returns its original result. _(Realizes `INV-LOG-22` for the service; operator ruling,
  2026-10-08, decision log row 38.)_

### Reads for other tools

- **`INV-SVC-14`** <!-- uuid: 1e4e33d9-9b6d-4059-ac55-cffc8fcb1caa --> — The calendar read MUST list the running segments, not the
  cycles, that overlap a window: one event per segment, identified by the id of the event that opened
  it, ending at the read time when open, never of zero length, in a fixed calendar, and empty for any
  other calendar name. Each event's notes are the reserved `cycle_type` pair first, the operator's
  pairs one per line in the order entered (a repeated key is several values, a value is one line), a
  line that is exactly `---`, then the note; a parser reads pairs up to the first such line and the note
  is everything after it. The attention read MUST be stateless, listing an overdue or due-soon open task
  (high or medium), a cycle in overtime (medium, high after the configured minutes), a stale pause
  (low) and the read-only store (high), each with its group, and with a deep link built from the public
  URL where it has a page.
- **`INV-SVC-15`** <!-- uuid: 48468315-df8c-4d0e-a42f-bf0cf6fcbf9f --> — The service MUST be able to answer with the resolved,
  validated configuration it is using, every optional setting filled in, so a client sees what the
  service sees.
