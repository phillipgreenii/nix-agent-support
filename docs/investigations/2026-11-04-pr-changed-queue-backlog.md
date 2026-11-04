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
