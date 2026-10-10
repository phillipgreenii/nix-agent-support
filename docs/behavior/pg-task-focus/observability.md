# pg-task-focus — what the service exports

The service is run unattended, so it MUST be observable as a running thing: its logs, its metrics, its
traces, its health, a dashboard and the alerts that tell the operator it needs a restart. This doc owns
what is exported, the promise that none of it holds the operator's own words, and the registrations
that make it visible. The behavior it observes is in [`service.md`](service.md).

## Stories

- **`STORY-OBS-KNOW-ITS-HEALTH`** <!-- uuid: a1fce8ff-cbda-4284-a4df-14178e65275e --> — As the operator, I want to be told when the service is down, stuck,
  unable to write, failing to alert me or restarting in a loop, and to see why, so a silent failure of
  my routine tool is impossible. _(→ `JOURNEY-OBS-DOWN`; `INV-OBS-5`, `INV-OBS-6`, `INV-OBS-8`,
  `INV-OBS-9`, `INV-OBS-10`.)_
- **`STORY-OBS-NO-LEAKS`** <!-- uuid: d8c3fb39-054e-418c-b8a3-8996af38a741 --> — As the operator who writes private notes, reasons and labels into the
  routine, I want none of those words to reach a log, a metric, a trace or a health page, so
  observability never becomes a copy of my notes. _(→ `JOURNEY-OBS-DOWN`; `INV-OBS-2`, `INV-OBS-3`.)_
- **`STORY-OBS-SEE-MY-USAGE`** <!-- uuid: 30b39ed1-89e3-436d-9921-7416d9db0016 --> — As the operator, I want a dashboard of my own habits (time by cycle
  type, tasks overdue, miss rate, how often I correct) beside the service's health, so I can see the
  routine working. _(→ `JOURNEY-OBS-DOWN`; `INV-OBS-4`, `INV-OBS-7`.)_

## Journeys

### `JOURNEY-OBS-DOWN` — the service stops answering <!-- uuid: acad9e40-e6b3-4079-98c1-9f9da55e8ca2 -->

**Primary actor:** `ACTOR-OPERATOR`. **Other actors:** `ACTOR-HOST`, `ACTOR-CONFIGURER`.
_Requires:_ `INV-OBS-1`, `INV-OBS-2`, `INV-OBS-3`, `INV-OBS-4`, `INV-OBS-5`, `INV-OBS-6`, `INV-OBS-7`,
`INV-OBS-8`, `INV-OBS-9`, `INV-OBS-10`. _Includes:_ none.

1. The service fails to start (a corrupt log, a held data directory, an active profile the
   configuration lost). It logs one Error line with the cause, flushed before it exits, and serves no
   metrics (`INV-OBS-2`, `INV-OBS-9`).
2. After the configured minutes the daemon-down alert fires; the Error line in the log names the cause
   (`INV-OBS-8`, `INV-OBS-9`).
3. The operator reads the log and the health page of an earlier run, fixes the cause, restarts, and the
   alert clears; the dashboard shows the restart and that the replay was recovered or not
   (`INV-OBS-6`, `INV-OBS-7`).

Extensions:

- 1a. No telemetry collector is running. The service starts and serves all the same, and exports only
  to a loopback collector when there is one (`INV-OBS-1`).
- 2a. The service runs and then crashes repeatedly. The restart-loop alert fires instead, and the log
  holds the panic (`INV-OBS-8`).

## Invariants

- **`INV-OBS-1`** <!-- uuid: 7455a67b-f4e1-4eee-b816-6f5839afa9cc --> — Observability MUST be best-effort: the service starts and
  serves with no collector present, a failing exporter is logged once and ignored, and telemetry MUST go
  only to a collector on the loopback interface (an endpoint elsewhere is refused and telemetry stays
  local). All telemetry stays on the machine.
- **`INV-OBS-2`** <!-- uuid: 83ae8f20-799a-4348-9c91-3cd3626da351 --> — Logs MUST be structured JSON lines, one per event of interest,
  carrying `component`, `request_id`, and, where a request has a trace, `trace_id` and `span_id`, plus
  `route`, `status`, `duration_ms` and, on every refusal, its `reason`; every mutation writes one line
  at Info carrying its `event_id` and `event_type`; startup logs the replay's start and end with the
  event count and duration, the configuration digest and the version; a startup failure is logged at
  Error with its cause and flushed before the process exits. The file carries Info and above and the
  collector carries Warn and above. The log source's error alert fires on one Error line. Loki labels
  are only the service name and the level: ids, routes and event types are fields, never labels.
- **`INV-OBS-3`** <!-- uuid: b296fc4d-8085-4294-8b59-89e36908c917 --> — Logs, span attributes, metric labels and health output MUST NOT
  contain any free-text field: a note, a key/value value, a skip reason, a correction reason, a
  retraction reason or a period label. Request and response bodies MUST NOT be logged. A test pushes a
  unique sentinel through every field the event schema marks as free text and fails if it appears in
  the log, the exported logs, the span attributes, the metrics or the health page, and fails when the
  schema gains a free-text field the test does not push.
- **`INV-OBS-4`** <!-- uuid: b0fad9d5-109e-4aeb-aa2b-c113628d45df --> — Metrics MUST be served as Prometheus text, every name prefixed
  `pg_task_focus_` and ending as convention says (`_total` for counters, a unit suffix such as
  `_seconds` or `_bytes`). Labels MUST be route templates or members of a closed set (the client kind,
  the reason, the append stage, the event type, the cadence, the outcome, the severity) or a configured
  cycle-type id, never an id, a title or free text, and a test guards that. Operational counters (HTTP
  requests and their latency, events appended, append and fsync latency, append failures, refusals,
  reloads, corrections, alerts played and failed, stream events) are queried with a rate; totals derived
  from the log (tasks by status, resolutions, cycle time by type, overdue tasks, attention items, the
  next reminder) are gauges read from the log at scrape time, so a restart changes none of them. The
  append and fsync latencies observe an append once, not once per event of a batch.
- **`INV-OBS-5`** <!-- uuid: ceacdf2d-411d-403d-8a6d-781cabd9745a --> — At start the service MUST add zero to every series of the
  counters an alert reads (the append failures for each stage, the alert failures), so the first
  scrape has a baseline for a rate; and a test MUST check the alert rules and the dashboard against the
  real scraped output, so a name or label value that does not exist fails.
- **`INV-OBS-6`** <!-- uuid: d2fb8ca8-1145-4e69-aab2-60e13490c809 --> — Health MUST report, from process start, the status, readiness
  (and the failing check), the version, the schema version, uptime, the replay (events and duration), the
  last append, the store (state, the READ-ONLY reason and since when, writability, size), the
  configuration (validity, digest, load time, reload problems by path, settings that need a restart),
  the recovery (torn tail, uncommitted batches) and the alert scheduler's reading (the running cycle
  and its next reminder). Its reload problems never echo a configured value.
- **`INV-OBS-7`** <!-- uuid: ede8c49e-bf7d-4fd9-958c-4397e1d51d3b --> — The service MUST ship a dashboard in two halves: the service's
  health, and the operator's own usage (cycle time by type, tasks overdue, miss rate, correction rate,
  a high value of which signals a design problem, and the last request of each kind of client, so a
  silent menu-bar plugin is visible). The usage panels are live-state views: a correction or a
  retraction rewrites the past while the metrics keep what they said at the time, and the log is the
  authoritative history.
- **`INV-OBS-8`** <!-- uuid: 235cf9fc-9ed2-49e0-b731-d2b128372637 --> — The service MUST register alert rules, each with an
  identifier, a duration, a no-data and an error behavior, a severity and an annotation that names the
  remedy: daemon down (critical, no data counts as firing); not ready after replay (critical); the data
  directory not writable (critical); the store read-only (critical, at once); append failures
  (critical, at once); the configuration reload failing (warning); a restart loop, three starts in half
  an hour (critical); startup recovery in the last hour (warning); the sound or notification failing
  (warning); a slow replay (a sizing flag, info); a cycle stuck in overtime for an hour (warning).
- **`INV-OBS-9`** <!-- uuid: fbc15050-c3bb-4471-8bca-67442e216cdc --> — A service that refuses to start never serves metrics, so a
  refusal MUST show as the daemon-down alert plus the Error log line, not as the restart-loop alert,
  which covers a service that runs and then crashes.
- **`INV-OBS-10`** <!-- uuid: 2011816d-6df0-41a0-9b91-ba1149223dd6 --> — The deployment MUST register the four hooks that make the
  service visible to the machine's observability stack: its scrape target (without which nothing is
  scraped and every alert is dead), its log source, its alert rule files and its dashboard provider, with
  the alert rules and the dashboard in one folder title so they converge. A traced request MUST have a
  span with child spans for validating, appending, syncing and projecting, sampled when its parent is
  and always otherwise, whose attributes come from an explicit list that excludes every free-text field.
