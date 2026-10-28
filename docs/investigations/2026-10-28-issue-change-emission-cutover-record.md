# Investigation: issue change emission, pre-cutover record (phase 5)

Run on 2026-10-28 between 15:46Z and 16:23Z (all times UTC), on the one machine that runs the
deployment. This is the pre-cutover record of the beads cutover (steps 4 and 6 of the per-backend
issue change emission design): the entity counts per backend, the router queue headroom, the wait of
a new event behind the decider backlog (design spike 13(e)) and the watched-query coverage check. It
follows the model of the phase-1 spike document
(`2026-10-22-issue-change-emission-spikes-live-data.md`), and its method sources are that document's
section 13(a) for the counts and its "What was and was not run" list for the verbs.

This repository is public, so the document gives counts, shapes, defaults and arithmetic only: no
issue key, bead title, tracker instance name, host name, project key, user name or home-directory
path appears in it. Backends are named by kind ("the primary beads backend", "the Jira backend", "the
second beads backend").

## Read this first

- Every read is read-only: `pg-desk status`, `pg-desk doctor`, `sqlite3 -readonly` against the store
  that `pg-desk status` reports (its `store:` line), `pg-connector issue list ... --ids-only`,
  `pg-connector config show --queries`, `pg-router status --json`, `pg-router gate list --json`, a
  plain HTTP GET of the router's own metrics endpoint (the address in `PG_ROUTER_METRICS_ADDR`,
  `127.0.0.1:9820`), and reads of the router's `queue.jsonl` file. No consumer cursor moved, no
  change row was written and no process was paused, restarted or configured by the measurement. The
  full list is under "What was and was not run".
- A number that could not be measured says so with the reason. No number is invented.
- **A release landed during the measurement.** The router process restarted at 15:49:24Z (new build
  `562fa16c`, the old process ran `42fc8a41` since 11:32:26Z), after the first read at 15:46:49Z and
  before the second. The router counters (`pg_router_throughput_total` and the queue-wait and run-time
  series) restart at zero with the process, so a delta across the restart is meaningless. Section 3
  therefore takes its two reads inside the new process (15:50:58Z and 16:22:08Z) and reports the old
  process separately. The pg-desk store, and so every count in section 1, survives the restart. The
  installed `pg-desk` is build `a97c6b50`; its `doctor` does not yet print the "an entity nothing
  watches" report line (checked: `pg-desk doctor | grep -i 'nothing watches'` prints nothing), so
  section 4 computes that number from the store.
- **The premise "the flood continues at 130 an hour and the decider backlog is thousands deep" does
  not hold on this machine today.** Section 3 shows `issue.changed` queue depth 0 at every read and no
  `reconcile` row written since 12:00Z. It is reported as measured; nothing here explains why, and the
  cutover packets keep their own safeguards (the headroom of section 2 is computed for the flood case
  anyway).

## Summary

| Record                                    | Result                                                                                                                                                                                                   |
| ----------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1 Active and watched counts per backend   | Primary beads backend 453 active, 453 watched; Jira backend 192 active, 192 watched; 645 active issues, 645 flagged watched; 646 ids in the watch sets                                                   |
| 1 Member count of each watched query      | beads: `focus-open-beads` 453, `work-beads` 124, `focus-held-beads` 0 (no stored set); Jira: 13, 31, 113, 81 (union 193)                                                                                 |
| 2 Router queue headroom                   | Event 513.67 bytes; log 700,993 of 67,108,864 bytes; 116,216 events (894.0 h at 130 an hour) to the 90% halt point; 129,281 events (994.5 h) to the 100% reject                                          |
| 3 Wait of a new event behind the backlog  | Backlog depth 0 (`issue.changed`), drain rate 5.78 events an hour (arrival-limited), so depth / drain rate = 0 / 5.78 = 0 h; measured queue wait 6.13 s (3 events) and 41.16 s (229 events, old process) |
| 4 Watched-query coverage check            | Primary beads backend: 0 active entities outside every watched query; Jira backend: 1; total 1 of 645. Second beads backend: 0 entities held, no watched query                                           |
| 5 Real-change rate under `fields_changed` | Not measurable before the release is applied everywhere; method stated, verify appends                                                                                                                   |

## 1. Active and watched counts per backend, and member count of each watched query

Backend is read from the store's `backend` column (the entity table has the nullable `backend` and
`watched` columns; every issue row has both set).

Command: `sqlite3 -readonly <store> "select backend, active, count(*), sum(watched=1) from entity where entity_type='issue' group by 1,2"`
(the store path is the `store:` line of `pg-desk status`).

| Backend (by `backend` column) | Active | Watched flag set on the active | Inactive rows |
| ----------------------------- | ------ | ------------------------------ | ------------- |
| Primary beads backend         | 453    | 453                            | 61            |
| Jira backend                  | 192    | 192                            | 12            |
| Total issue entities          | 645    | 645                            | 73            |

Cross-check: `pg-desk status` prints `type issue: active: 645`, equal to 453 + 192 = 645. The
`pr` entity type holds 651 active rows with no `backend` and no `watched` value; it is outside this
cutover (the entity-type scope is issue).

Member count of each watched query. The membership each query last listed is persisted by pg-desk as
a watch set (meta key `change_flow.watchset.issue.<query>`, a JSON list of ids).

Command: `sqlite3 -readonly <store> "select value from meta where key='change_flow.watchset.issue.<query>'" | jq 'length'`.
The list of watched queries is the `watched queries:` block of `pg-desk doctor` (seven issue queries,
each `ok`), and the declared query names per backend are the `queries` object of
`pg-connector config show --queries`.

| Watched query      | Members | Backend | Note                                                         |
| ------------------ | ------- | ------- | ------------------------------------------------------------ |
| `focus-open-beads` | 453     | Beads   | persisted watch set                                          |
| `work-beads`       | 124     | Beads   | wholly inside `focus-open-beads` (intersection 124)          |
| `focus-held-beads` | 0       | Beads   | no watch set is stored for it; the live listing is empty (0) |
| `mine`             | 13      | Jira    |                                                              |
| `assigned-to-me`   | 31      | Jira    |                                                              |
| `created-by-me`    | 113     | Jira    |                                                              |
| sprint query       | 81      | Jira    | the deployment's fourth Jira query, named only by its shape  |

Unions, counted once per entity (`jq -s 'add | unique | length'` over the stored lists):

- Beads: `focus-open-beads` U `work-beads` = 453 U 124 = **453**, because the second is contained in
  the first; `focus-held-beads` adds 0. Equals the 453 active.
- Jira: union of the four queries = **193** ids (pairwise overlaps: mine and assigned-to-me 13, mine
  and created-by-me 8, mine and sprint 13, assigned-to-me and created-by-me 19, assigned-to-me and
  sprint 13, created-by-me and sprint 8). The active Jira count is 192, so the union is one more than
  the active count (see section 4: two stored lists still name an inactive row and one active row is
  in no list, a lag between the stored lists and the active flag, not a miscount).
- All: 453 + 193 = **646** ids in the watch sets, against 645 active.

Live agreement for the beads queries (the verb the phase-1 spike used, pinned to the primary beads
backend so that only a local `bd` read runs): `pg-connector issue list --backend <primary beads backend> --query <q> --ids-only --output json`
gave `present_ids` of 124 for `work-beads`, 453 for `focus-open-beads` and 0 for `focus-held-beads`
(`truncated` false for all three). Compared id for id with the stored watch sets: 0 ids only live, 0
ids only stored, for both non-empty queries; all 124 of `work-beads` are inside the 453 of
`focus-open-beads`; 0 of the 453 active beads are outside the live `focus-open-beads` listing.

The Jira listings were NOT re-run live: `pg-connector issue list --query mine --ids-only` against
the Jira backend would contact the remote service, which this packet does not do on its own. The
Jira member counts above are the stored watch sets of the last poll (`pg-desk status` shows the last
heartbeat as the same day), not a fresh listing.

Versus the phase-1 spike (2026-10-22): the primary beads backend went from 425 active to 453 (a
tracker that grows), and the Jira backend from 11 active to 192 (the `mine` query then listed 11 and
now lists 13; the other three Jira queries are now part of the watched set, which is why the Jira
total is no longer the `mine` count).

## 2. Router queue headroom, from the measured event size

### Inputs, each with its command

| Input                                   | Value                                            | Command                                                                             |
| --------------------------------------- | ------------------------------------------------ | ----------------------------------------------------------------------------------- | ------------------------ |
| Queue log limit (hard)                  | 67,108,864 bytes = 64 MiB                        | `pg_router_queue_log_limit_bytes` on the metrics endpoint; `pg-router status --json | jq .queueLog.limitBytes` |
| Halt point for polled emitters ("soft") | 60,397,920 bytes (the status field `softBytes`)  | `pg-router status --json                                                            | jq .queueLog.softBytes`  |
| Current log bytes                       | 700,993 at 15:50:58Z; 778,496 at 16:22:08Z       | `pg_router_queue_log_bytes` on the metrics endpoint                                 |
| Current log fill                        | 1 percent at 15:50:58Z                           | `pg-router status --json                                                            | jq .queueLog.percent`    |
| Gates, emitters halted, rejected events | no gate; `emittersHalted` false; 0 rejected      | `pg-router gate list --json` (`[]`); `pg-router status --json                       | jq .queueLog`            |
| Default compaction threshold            | 8 MiB (runtime compaction; plus once at startup) | read from the router source (`DefaultCompactThresholdBytes = 8 << 20`)              |

Formulas for the points: the halt point is 0.9 x 64 MiB = 57.6 MiB = 0.9 x 67,108,864 =
60,397,977.6 bytes (the router reports 60,397,920, the figure used below, 57.6 bytes lower: the
router derives it in its own integer arithmetic); the reject point is the limit itself, 100 percent
= 67,108,864 bytes.

### Event size

An `issue.changed` event is one `enqueue` record in the queue log. Measured over the 229 `enqueue`
records of type `issue.changed` that the log held at 15:49Z:

`jq -r 'select(.op=="enqueue" and .type=="issue.changed")' queue.jsonl | wc -c`, with per-line length
by `awk '{print length($0)+1}'`:

- 229 records, 117,631 bytes: mean 117,631 / 229 = **513.67 bytes**; median 510, 95th percentile
  583, maximum 654.
- The record carries the event id, type, three timestamps and the payload with the entity id, type,
  kinds, origin, seq and version (the keys of the record, no values here).
- The other records of an event's life are an `accept` (mean 181.8 bytes) and an `archive` (mean
  317.4 bytes). They are not live state: the router compacts the log to live state at startup and above
  the compaction threshold, which is what happened at the 15:49:24Z restart (the log fell from
  1,450,911 to 675,912 bytes). A backlog of undelivered events is live state, so it costs the
  `enqueue` size only; the event size used below is therefore the `enqueue` mean, with the 95th
  percentile and maximum as the conservative bounds.
- Reproducibility caveat: those delivered `enqueue` records are compacted away, so the same command
  run after 15:49:24Z counts the (few) records still live and does not return 229 again. The per-line
  size lists that this record came from were saved under the author's scratch directory and are not
  part of the repository.

### Headroom

With current log bytes 700,993 (15:50:58Z):

- Bytes to the halt point: 60,397,920 - 700,993 = 59,696,927.
- Bytes to the reject point: 67,108,864 - 700,993 = 66,407,871.
- Headroom in events = bytes / event size; in hours = events / 130 an hour (130 an hour is the
  2026-10-21 measured flood rate, kept as the planning rate).

| Event size used       | Events to halt (59,696,927 / size) | Hours at 130 an hour | Events to reject (66,407,871 / size) | Hours at 130 an hour |
| --------------------- | ---------------------------------- | -------------------- | ------------------------------------ | -------------------- |
| 513.67 (mean)         | 116,216                            | 894.0                | 129,281                              | 994.5                |
| 583 (95th percentile) | 102,396                            | 787.7                | 113,907                              | 876.2                |
| 654 (maximum)         | 91,280                             | 702.2                | 101,541                              | 781.1                |

Cross-checks:

- At the second read (16:22:08Z, 778,496 bytes) the halt headroom is (60,397,920 - 778,496) / 513.67
  = 116,066 events = 892.8 hours at 130 an hour: no meaningful movement.
- The depth that the design quoted, 4,387 queued reconcile events, would occupy 4,387 x 513.67 =
  2,253,470 bytes = 2.15 MiB = 2,253,470 / 67,108,864 = 3.36 percent of the limit; 1,757 events (the
  earlier figure) occupy 1,757 x 513.67 = 902,536 bytes = 1.3 percent. The flood itself adds 130 x
  513.67 = 66,777 bytes an hour = 0.064 MiB an hour.
- Pessimistic floor, if nothing were compacted and every event carried its full lifecycle (`enqueue`
  513.67 + `accept` 181.8 + `archive` 317.4 = 1,012.8 bytes): 59,696,927 / 1,012.8 = 58,942 events =
  453.4 hours at 130 an hour.

Conclusion: the queue is at 1 percent. The cost of waiting for step 4 at the flood rate is
roughly 0.064 MiB an hour of live-state growth, so the log reaches the 90 percent halt point after
about 894 hours (about 37 days) of an undrained flood, and no gate, halt or rejection is involved at
this depth. The ordering "deploy step 4, then the decider leaves plan mode" does not endanger the
queue on this machine at the measured rates.

## 3. Wait of a new event behind the decider backlog (spike 13(e)), depth and drain rate

### Depth, now

The `issue.changed` queue depth is read from `pg-router status --json | jq .queues` (the same series
as `pg_router_queue_depth{type="issue.changed"}`). There is no `issue.changed` entry in `queues`, and
no series on the metrics endpoint, at any of the three reads: 15:46:49Z (old process), 15:50:58Z and
16:22:08Z (new process). A type with no queued event has no series, so the depth is **0**. This
agrees with the pg-desk side: the `pg-router` consumer of the issue change feed has `lag=0` in
`pg-desk status` (cursor equal to the newest issue row, 53,396 at 15:46Z).

### Drain rate, two reads at least 30 minutes apart

Series: `pg_router_throughput_total{role="issue-decider",type="issue.changed"}` on the metrics
endpoint (the series is absent until the first delivery of a process, read as 0).

| Read     | Time (UTC)  | Value | Process                                  |
| -------- | ----------- | ----- | ---------------------------------------- |
| First    | 15:50:58Z   | 0     | new, started 15:49:24Z, no delivery yet  |
| Second   | 16:22:08Z   | 3     | same process                             |
| Interval | 31 min 10 s |       | 16:22:08 - 15:50:58 = 1,870 s = 0.5194 h |

Drain rate = (second - first) / hours = (3 - 0) / 0.5194 = **5.78 events an hour**.

Two cautions, both from the data and not guesses:

- The drain rate here is arrival-limited, not capacity: the queue was empty at both reads, so it is
  the rate at which events arrived and were delivered. The decider's own run time is a better
  capacity indicator: `pg_router_run_seconds` sum / count = 5.049 / 3 = 1.68 s a delivery (new
  process) and 115.13 / 229 = 0.503 s a delivery (old process, 11:32:26Z to 15:47Z); if deliveries to
  the role were strictly one at a time, the ceiling would be 3,600 / 0.503 = 7,157 an hour, a
  conditional figure (not measured: whether the role runs deliveries concurrently).
- The first sample is of a fresh process (see "Read this first"). The old process delivered 229
  `issue.changed` events in 4 h 14 min 37 s (11:32:26Z to 15:47:03Z = 4.2436 h): 229 / 4.2436 = 54.0
  an hour, read from the same series before the restart.

### Wait

Wait of a new event behind the backlog = depth / drain rate = 0 / 5.78 = **0 hours**: nothing is
queued ahead of a new `issue.changed` event.

The per-event wait is also measured directly (`pg_router_queue_wait_seconds`, from the enqueue to the
start of the delivery, with the role's polling interval inside it):

- New process: sum / count = 18.393 / 3 = **6.13 s** a delivery.
- Old process: sum / count = 9,424.67 / 229 = **41.16 s** a delivery (the deprecated
  `pg_router_dispatch_latency_milliseconds`, wait plus run, gives 9,541,797 / 229 = 41,668 ms; the
  difference is the run time, 0.50 s).

For comparison, the design's premise at the earlier measurements was depth 4,387 at the then drain
rate of 64 an hour: 4,387 / 64 = 68.5 hours, about 69 hours. That backlog has drained (the queue
never holds it now); the formula is recomputed above with today's measured depth and rate, which
gives 0 hours, and is to be recomputed by the cutover if the depth is not 0 when it deploys.

### What the pg-desk side shows at the same time

Command: `sqlite3 -readonly <store> "select substr(at,1,13), kinds='[\"reconcile\"]', count(*) from change_log where entity_type='issue' and at >= '2026-10-28T03:00:00Z' group by 1,2"`.

- Hourly issue rows from the 03:00Z hour to the 11:00Z hour were almost all `reconcile`: 80, 110,
  104, 76, 165, 150, 135, 87 and 64 rows for the nine hours (range 64 to 165), the same order of
  magnitude as the 130 an hour of the design.
- Since 12:00Z no `reconcile` row has been written: the 12:00Z hour holds 365 rows, none `reconcile`
  (196 `fields_changed` from origin `pg-desk`, 166 `link_changed`, 3 from the connector), then 11
  rows in the 14:00Z hour and 6 in the 15:00Z hour, none `reconcile`. Between the two router samples
  (15:50:58Z to 16:22:08Z) 6 rows were written and 0 were `reconcile`.
- Last 24 hours (since 2026-10-27T15:47Z): 2,658 rows, 2,199 `reconcile` (1,322 `sweep`, 861
  `local-reconcile`, 16 connector) and 459 not `reconcile`; the 459 include the 12:00Z hour's burst of
  362 (196 + 166), leaving 459 - 362 = 97.

This is the evidence for the "Read this first" note: on this machine the flood stopped at about the
12:00Z hour, after which a one-off burst of `fields_changed` and `link_changed` rows appears. No
conclusion is drawn about the cause.

## 4. Watched-query coverage check

The watched set decides what the decider and the focus rank can see: an issue outside every watched
query and every one-off watch delivers no event. The check names, per consumer, the entity set it
reads (from source, one sentence each), then counts the entities that set misses.

| Consumer                     | Entity set it reads (one sentence)                                                                                                                                                                                                                                                | Source                                                                  |
| ---------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------- |
| Decider (issue decider role) | It acts only on `issue.changed` events, so it sees exactly the entities pg-desk emits a change for, which are the active issue entities that a watched query (or a one-off `refresh --watch`) holds: 645 active today.                                                            | role binding `issue.changed` in `pg-router status --json` (`listeners`) |
| Focus coverage and rank      | It reads only pg-desk's stored issue entities: Jira issues seeded by the operator identity list, beads that carry the planable label in an open, in-progress or blocked state, and stored issues joined to a seed by a jira, work, parent or mentions link; no tracker is called. | `internal/focus/coverage.go`, `inputs.go`, `candidates.go`              |
| Due-date attention rules     | They read the `due_date` fact (and the done state) of every stored issue entity, Jira and bead alike, through `watch.issue.queries`, so only an issue a watched query selects can raise due-soon or overdue.                                                                      | `internal/attention/rules_issue_due.go`                                 |

Entities each reads that matter today:

- Decider: 645 active issue entities (453 beads, 192 Jira).
- Focus: 453 open beads of which 53 are bead candidates (`pg-desk doctor`, line "open beads but zero
  bead candidates: 0 (453 open beads, 53 bead candidates)"); Jira seeds are the issues whose
  assignee is in the operator identity list (the count is not printed by a read-only verb and is not
  recorded here); `unwatched selections: 0` and `degraded or failed listings: 0` in the same report.
- Due-date: of the 645 active stored issues, 2 carry a `due_date` (both Jira, 0 beads;
  `sqlite3 -readonly <store> "select backend, count(*), sum(coalesce(json_extract(facts,'$.issue_show.due_date'),'')!='') from entity where entity_type='issue' and active=1 group by 1"`).

### Count of active entities that no watched query lists

This is the number the doctor report "an entity nothing watches" gives once the installed build
prints it; today it is computed from the store.

Command: active issue ids (`select entity_id from entity where entity_type='issue' and active=1`)
minus the union of the stored watch sets (`comm -23 active.ids watched.union`).

| Backend               | Active | Active ids in no watched query's stored list |
| --------------------- | ------ | -------------------------------------------- |
| Primary beads backend | 453    | **0**                                        |
| Jira backend          | 192    | **1**                                        |
| Total                 | 645    | **1** (645 - 644 = 1)                        |

Reading of the 1 and of the opposite direction (watch-set ids with no active row: 2, both Jira,
both inactive rows still named by a stored list): these are the lag between the stored per-query
list and the active flag (an issue that has just left or just joined a query), not an unwatched
class of issue; the entity is itself flagged watched. The beads side is exact: live listing and
store agree (section 1).

### Entities outside the watched set by design

- **The second beads backend.** The connector registers a second beads tracker with six declared
  queries (none of them watched in pg-desk; the router's own sources read them) and the store holds
  **0** entities from it (`backend` values present: the primary beads backend and the Jira backend
  only). The decider, the focus rank and the due-date rules therefore see none of that tracker's
  beads today. Whether any of them should be watched is the declaring packet's decision, not this
  record's; the record states it as a coverage gap of kind "a whole backend has no watched query",
  with no count (its beads were not listed: no watched query names it).
- Beads and the due-date rules: 0 of 453 active beads carry a `due_date` (the phase-1 spike found 0
  of 425 as well), so the due-date rules have nothing to raise for beads whatever the watched set.
- `pr` entities are outside the issue cutover.

### Verdict of the check

Coverage of what the three consumers read is complete for the primary beads backend (0 active beads
outside every watched query) and for the Jira backend within one entity (1 of 192, a list lag), and
absent for the second beads backend, which no watched query lists. Declaring the watched queries
(the next packet) does not change the union of the current deployment's queries; this record is its
baseline.

## 5. Real-change rate under the `fields_changed` rule: method and what verify appends

The real-change rate under the new `fields_changed` rule cannot be measured before the release is
applied to every part of the deployment: the release landed mid-measurement and the first pass of the
new build wrote a one-off burst of `fields_changed` and `link_changed` rows (section 3), so a rate
read now would be contaminated by the burst rather than reflect steady state. The phase-1 figure of
63.9 rows a day (29.5 tracker-observed) is the OLD classifier's and is not comparable.

Method (to be run by the verify packet after the release has run for at least a full day past its
first pass):

1. Hydrations whose fingerprint changed per day: read the pg-desk hydration counters for the issue
   type (`change_flow.hydrations.issue` and `change_flow.hydration_failures.issue` in the store's
   `meta` table, or the `hydrations:` and `hydration_failures:` lines of `pg-desk status`) at two
   times a day apart and divide the difference by the days between them. Baseline for the next run:
   15,169 hydrations and 212 failures, read at 15:52:40Z on 2026-10-28 (a cumulative counter with no
   start time; the delta over a day is the figure, not this value).
2. Rows per day in `change_log` that are not reconcile:
   `select count(*) from change_log where entity_type='issue' and kinds != '["reconcile"]' and at >= <t0>` divided by the days since `<t0>`,
   excluding the first-pass burst by starting after it settles. For reference only, the retained log
   holds 814 non-reconcile issue rows of 52,450 (rows from 2026-10-19T16:15:50Z to 2026-10-28T15:42:15Z,
   8.977 days: 814 / 8.977 = 90.7 a day under the mixed old and new rules, the burst of 362 included).
3. Record both per backend, with the active and watched counts per backend of section 1 re-read at the
   same time.

Verify appends, under the heading below: the counts per backend (active and watched), the
hydration-delta figure and the non-reconcile rows per day, each with its command, and the result of
the live 13(e) wait if the depth is no longer 0.

## What was and was not run

Run, all read-only: `pg-desk status`, `pg-desk doctor`, `pg-desk --version`,
`sqlite3 -readonly` against the pg-desk store (entity, meta, change_log and consumer tables),
`pg-connector config show --queries`, `pg-connector issue list --backend <primary beads backend>
--query <q> --ids-only --output json` for the three beads watched queries (three calls, a local `bd`
read), `pg-router status --json`, `pg-router gate list --json`, `pg-router status --help`, an HTTP GET
of the router metrics endpoint at 15:47:03Z, 15:50:58Z and 16:22:08Z, `jq` and `awk` over the router's
`queue.jsonl` (no write; the file is only read), and reads of the router, focus and attention source
files named in section 4. The second read was taken by a background job that only sleeps, then GETs
the endpoint, calls `pg-router status --json` and one `sqlite3 -readonly` query.

Not run: any `pg-router pause`, `resume`, `gate set` or `clear`, `pg-desk issue changes`,
`pg-desk issue refresh`, `pg-connector issue changes`, any Jira `issue list` (it would contact the
remote service), any `bd` write verb, any configuration edit.

## Post-cutover

Appended by the verify packet on 2026-10-28, between 17:02Z and 17:37Z (UTC). Counts and results only:
no identifier of an organisation, tracker instance, bead, issue or person appears here, and the scratch
beads of the live checks are named by role. Every command was read-only except the state changes of
checks 4 and 5 (one scratch bead in each tracker, created, edited and closed).

### Release time, and what the pre-cutover record got wrong

- Release time, read from the newest applied-generation record (the `applied_at` field of the newest
  file under the `pn-workspace/applied/` state directory): **2026-10-28T15:51:47Z**. The two deferred
  follow-up beads count from it.
- The beads baseline markers in the pg-desk store are earlier than that record. The baseline of each
  of the three watched beads queries began at 11:32:54Z and finished at 11:38:07Z (meta keys
  `change_flow.baseline_open.<query>` and `change_flow.baseline.<query>`). The cutover build was
  therefore already running at 11:32Z (the old router process also started at 11:32:26Z); the
  15:51:47Z record is a second apply (router build `562fa16c`, the same installed pg-desk build
  `a97c6b50`). The measurements of sections 1 to 5 were consequently taken AFTER the pg-desk cutover,
  not before it, which is why "Read this first" found no flood: the last `reconcile` row in
  `change_log` is at 11:29:00Z, 3 min 54 s before the first baseline poll began. The 7-day follow-up
  counts from the later time (the conservative choice).

### Checks, each with its command

| #   | Check                                                  | Command                                                                                                                                                                   | Result                                                                                                                                                                                                                                                      |
| --- | ------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | Issue change-flow gates                                | `pg-desk doctor`                                                                                                                                                          | `dispatch check: ok`, `issue served by two backends: ok`, `backend without watched query: ok`, `feed cursor expired: ok`, `failed ingest age: ok`, `baseline open: ok`; all 7 issue watched queries `ok`. The command exits non-zero for one unrelated gate |
| 2   | Baseline marker, and the baselines wrote no change row | `sqlite3 -readonly <store> "select key, value from meta where key like 'change_flow.baseline%'"` and `select count(*), max(seq) from change_log where ... and at between` | markers present for all 3 beads queries (11:38:07Z); 0 issue rows with `at` from 11:32:54Z to 11:38:08Z; max issue seq before it 52787 (11:29:00Z); 0 `reconcile` rows after seq 52787                                                                      |
| 3   | Steady state of the issue source                       | `pg-router run-query --json query:<the issue source query>` with `PG_ROUTER_CONFIG` set to the daemon's config file                                                       | `{"total":0,"matches":[]}` at 17:04:17Z; the consumer of the issue feed shows `lag=0` (cursor equal to the newest issue seq) in `pg-desk status`                                                                                                            |
| 4   | One edit of a watched bead: one event, one decider run | `bd -C <watched tracker> update <scratch> --priority 3`, then `grep <scratch> queue.jsonl` and the router metrics endpoint                                                | one `change_log` row (`["fields_changed"]`, origin `pg-connector`, 15 s after the edit); one `enqueue` and one `accept` by the issue decider in the queue log; `pg_router_enqueued_total` 20 to 21 and `pg_router_throughput_total` 20 to 21                |
| 5   | An edit of an unwatched bead: none                     | the same create, `update --priority 3`, `comment` and `close` on a scratch bead of the tracker that has no watched query                                                  | 0 `change_log` rows, 0 `entity` rows and 0 queue-log records for it, read 11 min after the edit and 6 min after the close (the watched tracker's changes surfaced 15 s to 5 min after each edit)                                                            |
| 6   | A PR linked to an unwatched issue                      | not run                                                                                                                                                                   | No linked-PR change to an unwatched issue occurred in the window: not observed                                                                                                                                                                              |

Notes on the checks:

- Check 1: the one failing gate is the focus gate "unminted selections" (the PR lane is still in plan
  mode). It is outside the issue cutover and expected until the PR lane is applied. Every issue
  change-flow gate is green, including the three the packet names.
- Check 4, the whole life of the watched scratch bead: its creation produced one `added` row and one
  `issue.changed` event (decider accept 3.2 s after the enqueue), the edit one row and one event, its
  close one `["closed","status_changed"]` row. **A comment alone yields none.** The first edit tried
  was `bd comment`, which does not change the bead's `updated_at` and so not its listing fingerprint:
  no row appeared in 13 minutes, and the check was repeated with a field edit. A later reader SHOULD
  NOT use a comment as the probe edit.

### Counts per backend (read at 17:30Z)

Command: `sqlite3 -readonly <store> "select backend, active, count(*), sum(watched=1) from entity where entity_type='issue' group by 1,2"`.

| Backend               | Active | Watched flag set on the active | Inactive rows |
| --------------------- | ------ | ------------------------------ | ------------- |
| Primary beads backend | 453    | 453                            | 61            |
| Jira backend          | 191    | 191                            | 13            |
| Rows with no backend  | 0      | 0                              | 3             |
| Total                 | 644    | 644                            | 77            |

Against section 1: the beads counts are unchanged; the Jira active count is 191 (192 before) and its
inactive count 13 (12 before), one Jira issue having left its queries.

### Real-change rate under the `fields_changed` rule: interim figures, not a daily rate

The method of section 5 needs a full day past the first pass of the release, so no per-day rate is
claimed here; the verify follow-up for the 7 days owns it. The pieces measured now:

- First pass: the burst written from 12:12:59Z to 12:59Z was 362 rows from origin `pg-desk` (196
  `fields_changed`, 166 `link_changed`) plus 3 from the connector. It is excluded from the rate below.
- Rows in `change_log` that are not `reconcile`, from 13:00:00Z to 17:30:13Z (4.5036 h, the window after
  the burst): 39 rows, of which 3 belong to the watched scratch bead (excluded), so 36 rows, which is
  36 / 4.5036 = **7.99 an hour**. Rows with `kinds = '["reconcile"]'` after seq 52787: **0**.
- Hydration counters (`change_flow.hydrations.issue`, `change_flow.hydration_failures.issue`): 15,169
  and 212 at 15:52:40Z, 15,177 and 213 at 17:30Z, a delta of 8 hydrations and 1 failure in 1.6222 h
  (1 h 37 min 20 s), which is 8 / 1.6222 = 4.9 an hour.
- Router side: `pg_router_enqueued_total{type="issue.changed"}` was 14 at 17:04:47Z and 22 at 17:36:06Z.
  The 8 events between them include the 3 of the scratch bead, leaving 5 in 0.5219 h (31 min 19 s),
  5 / 0.5219 = 9.6 an hour: the same order as the 7.99 an hour above, well inside one order of
  magnitude, which is the criterion the 7-day follow-up checks over the whole window.
- `pg_router_throughput_total{role="issue-decider",type="issue.changed"}` equals the enqueued total (22
  and 22), and `pg-router status --json` shows no `issue.changed` entry in `queues` (depth 0): nothing
  is queued behind the decider, and the 13(e) wait is still 0.

### Follow-ups and daemon state

Two deferred follow-up beads carry the events that happen on their own schedule: the 7-day success
criterion (deferred to 2026-11-04T15:51:47Z, seven days after the release record) and the reconcile
backlog drain (deferred to 2026-10-31T12:51:47Z, the release record plus the roughly 69 hours the
design measured; this record's own reading on 2026-10-28 was depth 0, so it may close on first look).
At the end of the checks `com.phillipg.pg-router-daemon` is running (a process id present, last exit
status 0) and `pg-router gate list --json` is `[]`; nothing was paused or resumed.
