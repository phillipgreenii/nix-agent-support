# Investigation: pg-router `pr.changed` queue depth of 88 (24 h max 664), 2026-11-04

Found by the 2026-11-04 pg-router health review. All times are UTC unless a local time is named
(EST = UTC-5; the queue log stamps local time). Evidence is local and read-only:
`~/.local/state/pg-router/queue.jsonl` (2.5 MB, covers 2026-11-03 21:20Z to 2026-11-04 09:59Z),
`events.jsonl` (bounded with `tail -c 45000000` and `rg`), `launchd-stderr.log`, and the local
Prometheus on `127.0.0.1:9090` (hourly `query_range`). Nothing live was paused, resumed, restarted or
written. This repo is public, so the write-up names no organization repo, PR or tracker id.

## Summary

| Question                                                       | Answer                                                                                                                                                                    | Status                    |
| -------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------- |
| Is 88 (max 664) a backlog of unprocessed `pr.changed` events?  | Mostly no. `pg_router_queue_depth` counts RETAINED events, and a settled `pr.changed` event stays 6 h. The role's own pending count was 0 whenever depth was at its peak. | PROVED                    |
| Dominant cause                                                 | A heavy workday arrival rate (about 100 to 190 events per hour) multiplied by the 6 h retention window. Depth is roughly the last 6 hours of arrivals.                    | PROVED (arithmetic below) |
| Did the `LOW_DISK_USAGE` gate hold `pr.changed`?               | No. `pr-change` never appears in gate drops, and it completed its highest hourly volumes while the gate was active.                                                       | PROVED                    |
| Did failing or declining dispatches re-queue enough to matter? | No. 15 handler errors in 1841 dispatches (0.8%).                                                                                                                          | PROVED                    |
| Were events lost by expiring unsettled?                        | No, for the window the queue log covers: 0 of 294 enqueued and 0 of 358 archived events lacked an accept.                                                                 | PROVED for the window     |
| Was there a real (transient) queueing delay?                   | Yes, twice: oldest pending 2205 s (pending 100) and 2562 s (pending 135). The handler ran one dispatch at a time and was saturated.                                       | PROVED                    |

## Why the gauge misleads

`DEC-OBS-12` in `packages/pg-router/docs/decisions/observability.md` already decides this: the per-type
`queue_depth` gauge counts events the queue still retains, a handled event stays until its time to
live ends, so it climbs on a busy weekday while every listener is caught up, and it MUST NOT be read
as a backlog. The per-listener `pg_router_listener_pending` and
`pg_router_listener_oldest_pending_age_seconds` are the backlog signals.

Retention for `pr.changed` in the raw log: every archive record is evicted at its own `expiresAt`.
`evictedAt - at` ranged 6.0 h to 11.0 h (jq over all 358 `pr.changed` archive records; the 11 h events
are a second TTL class that also appears on `enqueue` records as `expiresAt - at = 11 h`). Settled events therefore leave only
at expiry (`INV-EVT-3` keeps the id for de-duplication while retained; `INV-EVT-4` expiry).

## Arithmetic: depth is the trailing 6 hours of arrivals

Dispatch results per hour (`events.jsonl`, role `pr-change`, hour of dispatch start) track arrivals,
because every enqueued event was accepted (see below).

- Peak: depth 664 at the 2026-11-03 20:00Z window. Dispatches in the six hours 15:00Z to 20:59Z are
  115 + 104 + 62 + 192 + 92 + 101 = 666. 666 against 664 observed.
- Now: depth 66 at read time. Dispatches 04:00Z to 09:59Z on 2026-11-04 are
  26 + 14 + 17 + 9 + 0 + 1 = 67. 67 against 66 observed.
- Baseline: on 2026-11-02 before 12:00Z depth was 6 to 10 because arrivals were 0 to 16 per hour; it
  reached 419 by 20:00Z once arrivals rose (80, 69, 51, 106, 76 per hour in the preceding window).

## Hourly timeline (UTC hour of window start)

Columns: `enq` is `increase(pg_router_enqueued_total{type="pr.changed"}[1h])` (it undercounts across a
counter reset at a restart, so `disp` is the better arrival proxy); `depth` is the max
`pg_router_queue_depth{type="pr.changed"}` in the hour; `pend` and `age` are the max
`pg_router_listener_pending{role="pr-change"}` and `pg_router_queue_oldest_age_seconds` (s); `gate`
is whether `LOW_DISK_USAGE` was active; `disp`, `err` (handler-error results) and `avg s` come from
`events.jsonl`. `-` means no sample. A retired event leaves at expiry, so evictions in an hour are
about the arrivals of 6 hours earlier.

| Hour (UTC) | enq | disp | err | avg s | depth | pend | age s | gate |
| ---------- | --: | ---: | --: | ----: | ----: | ---: | ----: | :--: |
| 11-02 11   |  16 |   16 |   0 |   9.0 |    21 |    - |    31 |  -   |
| 11-02 13   |  36 |   41 |   6 |  12.6 |    66 |    - |   108 |  -   |
| 11-02 15   |  80 |   78 |   0 |  10.0 |   164 |    - |   151 |  -   |
| 11-02 16   |  69 |   70 |   1 |  20.8 |   232 |    - |   111 |  -   |
| 11-02 17   |  51 |   55 |   2 |  32.6 |   275 |    7 |   235 |  -   |
| 11-02 18   | 106 |  104 |   0 |  21.1 |   365 |   10 |   203 |  -   |
| 11-02 19   |  76 |   76 |   0 |  22.3 |   415 |   11 |   325 |  -   |
| 11-02 20   |  33 |   33 |   0 |  14.4 |   419 |    3 |   101 | yes  |
| 11-02 21   |  21 |   21 |   0 |  33.8 |   413 |    4 |   186 | yes  |
| 11-03 01   |  49 |   50 |   1 |  19.3 |   216 |    5 |   218 |  -   |
| 11-03 12   |  12 |   45 |   0 |  10.2 |    73 |   41 |   413 |  -   |
| 11-03 14   |  87 |   27 |   1 |  25.7 |   136 |   66 |  1919 | yes  |
| 11-03 15   |  55 |  115 |   0 |   8.7 |   191 |   59 |   809 | yes  |
| 11-03 16   | 102 |  104 |   2 |  10.8 |   293 |    8 |   124 | yes  |
| 11-03 17   | 126 |   62 |   0 |  15.3 |   418 |   63 |   494 | yes  |
| 11-03 18   | 164 |  192 |   0 |  18.8 |   564 |  100 |  2205 | yes  |
| 11-03 19   |  62 |   92 |   2 |  37.1 |   592 |   34 |  1395 | yes  |
| 11-03 20   |  97 |  101 |   0 |  21.6 |   664 |   11 |   343 | yes  |
| 11-03 21   |  94 |   96 |   0 |  22.7 |   643 |   14 |   421 |  -   |
| 11-04 01   |  36 |  133 |   0 |  18.2 |   372 |  135 |  2562 |  -   |
| 11-04 02   |  38 |   73 |   0 |  17.3 |   361 |   32 |  2294 |  -   |
| 11-04 03   |  21 |   21 |   0 |  22.6 |   299 |    4 |   126 |  -   |
| 11-04 04   |  24 |   26 |   0 |  27.1 |   253 |    5 |   245 | yes  |
| 11-04 05   |  13 |   14 |   0 |  16.7 |   267 |    4 |    98 | yes  |
| 11-04 06   |  13 |   17 |   0 |  15.1 |   284 |    4 |    36 |  -   |
| 11-04 07   |   9 |    9 |   0 |  22.1 |   290 |    2 |   143 | yes  |
| 11-04 08   |   0 |    0 |   - |     - |   126 |    0 |     0 | yes  |
| 11-04 09   |   1 |    1 |   0 |   7.7 |    88 |    0 |     0 | yes  |

Rows with no sample at all are omitted (11-02 10, 11-02 12, 14, 22 and 23, 11-03 00, 02 to 11, and
11-03 22 to 11-04 00); see "Not determined". Whole-window totals for 2026-11-02 11:00Z to 2026-11-04
09:59Z: 1841 dispatch results, 15 with a handler error (all `will_retry`), 9 retried. Arithmetic:
15 / 1841 = 0.8%.

Gate windows from the raw queue log (local time converted to UTC by +5 h): 04:35Z to 05:18Z
(`gate_cleared`), 07:20Z to 07:42Z, 07:58Z to 08:26Z, then 08:35Z set continuously through the last
record at 09:59Z (renewed about every five minutes with 7-minute leases; free space on `/` fell from
18.2 GiB to 7.8 GiB). Earlier gate windows come from `pg_router_gates_active` only.

## Cause by cause

1. Arrival burst times 6 h retention (DOMINANT). Arrivals ran 100 to 190 per hour from 11-03 15:00Z to
   21:00Z against about 30 per hour on 11-02 evening. Depth 664 is that burst held for six hours.
2. Handler saturation (real, transient). One dispatch at a time
   (`max_over_time(pg_router_listener_in_flight{role="pr-change"}[48h])` = 1). At 11-03 18:00Z
   192 dispatches x 18.8 s = 3610 s, about a full hour of work, so pending reached 100 and the
   oldest waited 2205 s. At 11-04 01:00Z a backlog of 135 pending drained with oldest 2562 s. Accept
   lag over all 294 queue-log events: median 611 s, p90 2250 s, max 2574 s.
3. Gate holds (NOT the cause). `pg_router_gate_drops_total` has series for repo sync, repo tip,
   feedback, review, fix-ci and drain roles, none for `pr-change`; `pr-change` finished 693
   dispatches in 11-03 14:00Z to 20:59Z, all inside gate-active hours.
4. Failing or declining dispatches (NOT the cause). 0.8% error rate above; the logged cause of the
   errors in the older warn rows is a transient work-item lookup failure, and the router retries.
5. Unsettled expiry (NOT occurring in the covered window). Queue log: 294 `pr.changed` enqueues, 294
   with an accept, none unaccepted; 358 archives, each with exactly 1 accept; no
   `pg_router_unconsumed_expired_total` or `pg_router_listener_expired_pending_total` series for
   `pr.changed`/`pr-change` since the last counter reset. This does NOT cover events whose retention
   ended before 2026-11-03 21:20Z; the existing open work about expired `pr.changed` events being lost
   owns that question, and this finding neither duplicates nor closes it.

## Evidence commands

```bash
# retention vs backlog: per-hour range queries, step 3600, 48 h
curl -s -m 30 -G http://127.0.0.1:9090/api/v1/query_range --data-urlencode 'query=max_over_time(pg_router_queue_depth{type="pr.changed"}[1h])' ...
# also pg_router_listener_pending{role="pr-change"}, pg_router_queue_oldest_age_seconds,
# pg_router_gates_active{type="LOW_DISK_USAGE"}, increase(pg_router_enqueued_total/throughput_total ...[1h])

# lifecycle from the raw queue log (2.5 MB, safe to read whole)
jq -s '[.[]|select(.eventId|tostring|startswith("pr.changed:"))]|group_by(.eventId)' queue.jsonl
jq -r 'select(.op=="archive" and .type=="pr.changed")|[.reason,(.accepts|length)]|@tsv' queue.jsonl
rg '"op":"gate' queue.jsonl                      # gate_set / gate_cleared / gate_expired

# dispatch outcomes, bounded
tail -c 45000000 events.jsonl | rg '"role":"pr-change"' | jq -r 'select(.msg=="dispatch result")|[.time[0:13],(.error!=null),.will_retry,.duration_ms]|@tsv'
```

## Not determined

- 11-03 22:00Z to 11-04 00:59Z and 11-03 09:00Z to 11:59Z show no Prometheus samples and no
  `pr-change` dispatches, yet 135 events were pending at 11-04 01:00Z. Whether the router, the scrape or
  only the `pr-changes` source was down in those hours is not established (the source failed 14 times
  in total in the current log, the last at 11-04 08:35Z, the same second the gate was set again).
- Whether the `pg_router_queue_depth` growth alerts in `packages/pg-router/grafana/alerting/alerts.yaml`
  fired for this retention growth was not read from Grafana.
- Per-event timelines before 2026-11-03 21:20Z (the queue log was compacted) and the 24 h maximum's
  exact instant are bounded by Prometheus hourly maxima, not by event.
- Why 11 h TTL events exist alongside 6 h events was not traced.

## Result

No code or config change. The behavior is already specified (`DEC-OBS-12`); the misreading is in the
health review's use of `queue_depth` as a backlog. Follow-ups are filed by the orchestrator, not here.

## Addendum 2026-11-04: the two silent windows

Read-only, localhost only. Times are UTC unless marked EST (host clock is EST, UTC-5).

**Cause.** The two windows have different causes, and neither is "the `pr-changes` source failing".

1. 11-03 09:00Z to 11:59Z (really 08:32Z to 12:49Z): a host reboot, then no user session. Not sleep,
   not a router-only stall.
2. 11-03 22:00Z to 11-04 00:59Z (really 21:53Z to 01:16Z): an operator `SYSTEM_PAUSE` gate, so the
   router was up and scraped but dispatched nothing. The "no Prometheus samples" in the table above
   were only the idle counters, not the scrape.

### Window 1: reboot at 03:31 EST, login at 07:45 EST

| Evidence                                                   | Value                                                                                                                                                                                |
| ---------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `sysctl -n kern.boottime`                                  | `Tue Nov 3 03:31:06 2026` (08:31:06Z)                                                                                                                                                |
| `last reboot console`                                      | reboot 11-03 03:31; the console session before it (from 10-21 09:30) ended in `crash`, i.e. an unclean end; new console login 11-03 07:45                                            |
| Last daemon log line before the gap (`launchd-stderr.log`) | `2026-11-03T03:28:34.886-05:00` (exporter export timeout)                                                                                                                            |
| Next daemon lines                                          | `2026-11-03T07:47:36` config load, `07:47:38` `msg=starting` (12:47Z)                                                                                                                |
| Hours with daemon log lines                                | EST hours 04, 05, 06 have none (`rg -o '^time=2026-11-03T[0-9]{2}' \| uniq -c`)                                                                                                      |
| `pmset -g log` Sleep/Wake lines                            | none between 11-02 19:58:02 and 11-03 08:00:11 EST: the host was not asleep in the gap (the first sleep after is 08:00 EST, i.e. 13:00Z, matching the 13:04Z to 13:18Z scrape holes) |
| Prometheus `up` per job, step 60, 11-03 04:00Z onward      | every job including `prometheus`, `otelcol-self` and `loki` has no sample from 08:32Z to 12:49Z: the whole stack was absent (12:49Z - 08:32Z = 4 h 17 m)                             |
| `loki`, `grafana-notifier` process start                   | `Tue Nov 3 07:47:29 2026`                                                                                                                                                            |

The router daemon, Prometheus, otelcol, Grafana and the exporters are user LaunchAgents (inferred:
their processes all start at 07:47 EST, two minutes after the 07:45 console login, and nothing
logs between the reboot and the login). So after the 03:31 reboot the whole observability stack and
the router were down until login: 4 h 17 m (12:49Z - 08:32Z). The router's queue-side gate records
(`LOW_DISK_USAGE` sets) skip this span as well. Whether `/` filling up (7.2 GiB free now, 99 % used)
caused the unclean end is NOT DETERMINED.

Should an alert have fired? No alert could: Grafana, Prometheus and the exporters were all down,
so there was nothing to evaluate or notify. `pg-router liveness is down` also would not page, by design
(`noDataState: OK`, gated on host-activity awake gauges that were absent). This is the
"everything including the monitor is down" case that only an external dead-man's switch can cover.

### Window 2: operator `SYSTEM_PAUSE` 16:52 EST to about 20:16 EST

| Evidence                                                                                                                                                                                           | Value                                                                                                                                         |
| -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| `pg_router_gates_active{type="SYSTEM_PAUSE"}`, step 60, merged runs                                                                                                                                | `2026-11-03T21:53Z -> 2026-11-04T01:16Z` and `2026-11-04T02:52Z -> 03:17Z`                                                                    |
| `queue.jsonl` gate notice                                                                                                                                                                          | `gated by SYSTEM_PAUSE (owner operator, set 2026-11-03T16:52:15-05:00)` (21:52:15Z); second pause `set 2026-11-03T21:51:37-05:00` (02:51:37Z) |
| `up{job="pg-router"}`, `pg_router_liveness`, `pg_router_queue_depth{type="pr.changed"}`, `pg_router_listener_pending`, `pg_router_listener_in_flight{role="pr-change"}`, step 60, 20:00Z to 03:00Z | 421 samples each, no gap (7 h x 60 = 420 steps, plus one)                                                                                     |
| `pg_router_listener_pending{role="pr-change"}` 22:00Z to 01:15Z                                                                                                                                    | 0 on every 15-minute point; 117 at 01:30Z after the pause lifted                                                                              |
| `pg_router_throughput_total{role="pr-change"}` and `pg_router_enqueued_total{type="pr.changed"}`                                                                                                   | no series from 21:53Z to 01:21Z (counters are only exported while they move), which is what the table above recorded as "no sample"           |
| `queue_depth` 22:00Z to 01:15Z                                                                                                                                                                     | 643, 629, ... 237 (retention drain, not pending, see cause 1 above)                                                                           |
| `pg_router_source_last_success_age_seconds{source="pr-changes"}`                                                                                                                                   | 1.7 s on every 15-minute point: the source kept succeeding, so no source failure                                                              |
| `source failed` lines for `pr-changes`                                                                                                                                                             | first at 2026-11-04T02:36 EST, after both windows                                                                                             |
| `events.jsonl`                                                                                                                                                                                     | no `pr-change` dispatch in 22Z, 23Z, 00Z; any-role lines also stop in those hours                                                             |

A pause is a gate: emitters are not polled and listeners not dispatched to, so events pile up
until resume (pending 0 at 01:15Z, then 117 at 01:30Z and 133 dispatches in 01Z). The "135 pending
at 01:00Z" is that hour's maximum, reached right after the pause was cleared.

Should an alert have fired? `pg-router-gate-held` (`alerts.yaml`, uid `pg-router-gate-held`,
`for: 30m`, `noDataState: OK`, `severity: warning`, expression
`max by (type)(last_over_time(pg_router_gates_active[10m])) > 0`) covers exactly this: the gauge
was continuous for 3 h 23 m (21:53Z to 01:16Z = 203 min) against a 30 min `for`, so it would
have gone pending at 21:53Z and firing at about 22:23Z. It is a warning by design (an operator
pause is usually deliberate), and `pg-router-queue-stalled` is deliberately suppressed under any
gate. The live rule currently shows `state=firing` for `LOW_DISK_USAGE` (activeAt 04:06:40 EST),
so the rule works. No alert gap in this window.

### Sleep, scrape gap, source failure: ruled out

- Sleep: `pmset -g log` has Sleep/Wake lines only from 11-03 08:00 EST, none in either window.
- Scrape gap: in window 2 `up` is continuous; in window 1 every job is missing (stack down), which
  is not a scrape fault either.
- `pr-changes` failing: `source_last_success_age_seconds` 1.7 s in window 2; no failure lines before
  02:36 EST on 11-04.
- Router down: true only for window 1 (and only as a consequence of no login).

### NOT DETERMINED

- Why the host ended its session uncleanly at about 03:30 EST on 11-03 (`last` reports `crash`); the
  same word appears for the 10-21 reboot at 03:10 EST. No panic log was read.
- Whether the Grafana rule `pg-router-gate-held` actually reached `Alerting` and notified during
  window 2: the Grafana state-history annotation API returned no rows, and only the current rule
  state was read.
- Who ran the 16:52 EST `pg-router pause` and why it lasted 3 h 23 m.

### Proposed follow-ups (not filed here)

- Detect "stack absent" from outside the stack (dead-man's switch), size M. Acceptance: an
  independent heartbeat that pages when no Prometheus sample for any job arrives for 30 minutes
  while the host reports awake, with a test.
- Start the observability stack and router at boot rather than at console login (or document
  auto-login as an accepted operator decision), size M, operator decision first.
- Investigate the unclean 03:xx reboots (10-21, 11-03) including disk pressure, size S.

## Addendum 2026-11-04: did the `queue_depth` alerts fire on retention growth?

Read-only, localhost only: the Grafana annotation (state history) API on `127.0.0.1:3000` for
2026-11-02 06:03Z to 2026-11-04 12:00Z, and Prometheus on `127.0.0.1:9090`.
`pg_router_listener_pending` and `pg_router_listener_oldest_pending_age_seconds` have samples only
from 2026-11-03 (first sample about 11-03 09:30Z in the hourly `query_range`), so firings on 11-02
cannot be classified against them.

Alerting transitions for `pr.changed` in the window:

| Rule                            | Alerting transitions (UTC)                                                       | Pending gauge readable? |
| ------------------------------- | -------------------------------------------------------------------------------- | ----------------------- |
| `pg-router-queue-depth-growing` | 11-02 14:03, 11-02 15:49, 11-03 15:22, 11-03 18:17                               | 11-02 17:30Z onward     |
| `pg-router-queue-stalled`       | 11-02 12:32, 13:21, 14:29, 20:02, 22:23; 11-03 03:01, 04:45, 06:41, 07:25, 14:24 | 11-02 17:30Z onward     |
| `pg-router-queue-slow-growth`   | none for `pr.changed` (its Alerting rows were `review.ready` and `repo.tip`)     | n/a                     |

Findings:

- `pg-router-queue-stalled` fired spuriously on retention. At the 11-03 14:24Z transition the role's
  `pg_router_listener_pending` read 0 on every scrape from 14:09Z to 14:23Z and `in_flight` read 0,
  while `pg_router_queue_depth{type="pr.changed"}` read 50 to 63. Nothing was owed; the depth was
  settled events waiting out their 6 h retention. The rule is re-keyed (see Change below). The other
  `pr.changed` stalled firings after the gauge existed read: 11-02 20:02Z pending 0 on every scrape of
  the 15 minutes before (retention only); 11-03 04:45Z the same; 11-02 22:23Z, 11-03 03:01Z, 06:41Z and
  07:25Z had pending of 1 or 2 on a few scrapes with a 0 elsewhere in the window, so a backlog was
  intermittent at most. Before 11-02 17:30Z (five firings) the gauge does not exist.
- `pg-router-queue-depth-growing` fired on genuine consumer lag on 11-03. In the 14:50Z to 15:25Z
  window `pending` reached 65 and the oldest pending age 1889 s; in the 17:40Z to 18:20Z window
  `pending` reached 99 and the oldest age 1155 s. The depth rose by more than the pending count
  (retention adds to it), but the lane did have a backlog growing at the time, so these are not
  spurious firings and the rule is left unchanged. Its 11-02 firings predate the gauge and are not
  classified.
- `pg-router-queue-slow-growth` did not fire for `pr.changed`; it counts arrivals against
  completions, and an event that expires unconsumed is subtracted, so retention does not feed it.
  Its `min_over_time(pg_router_queue_depth[1h]) >= 5` floor is only an extra gate.

Change: `pg-router-queue-stalled` now also drops a role whose `pg_router_listener_pending` read 0 on
any scrape of the 10 minute window (`unless on(role) (min by (role) (min_over_time(pg_router_listener_pending[10m])) == 0)`).
It is an exclusion, so a scrape with no pending series leaves the rule as it was. Covered by a
promtool case in `grafana/alerting/rule-tests/pg-router-queue-stalled.test.yaml` and by
`TestStalledRetentionOnlyDepthDoesNotPage` in `internal/alertrules/queue_stalled_test.go`.
