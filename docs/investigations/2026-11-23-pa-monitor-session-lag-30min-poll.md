# Investigation: pa-monitor session appear and leave lag, 30 minute live poll, 2026-11-23

Scope: re-run of the 30 minute lag poll after the assemble sub-phase split and cost cuts were applied
(installed build `0.0.0-bbfaf53c`, warm: `pa_monitor_scan_dirty_files` was 0 for the whole window). All
times are UTC. Evidence is local and read-only (`pa-monitor status --json`, `kill -0`, the local
Prometheus). This repo is public, so the page names no organization repo, PR or tracker id.

## Method

- Window 2026-11-23T06:03:42Z to 06:33:41Z, 1171 poll ticks of about 1.5 s, no gaps over 5 s.
- Poll loop: every tick `pa-monitor status --json`, and for each listed session `kill -0 <pid>`. Rows
  are `{ts, session_id, pid, status, started_at, alive}`.
- Appear lag = first tick that lists the session minus its `started_at` (second resolution, so up to
  about 1.5 s of poll cost is included).
- Leave lag = last tick that still lists the session minus the first tick where `kill -0` failed
  (a lower bound: the pid may have died up to one tick earlier).
- Host load average sampled each minute; phase means from `pa_monitor_poll_phase_duration_sum/_count`
  (`increase()` over 2 minute windows), `service_version=0.0.0-bbfaf53c`.
- No sessions were started or stopped by the poll; every start and exit is from real concurrent work.
  A same-pid session-id change (pid 11130, resume) is not counted as an exit or a start.

## Results

Appear lag (target 10 s or less), 4 real starts: 4 of 4 over target.

| started_at | first listed | lag  | load (1 min) |
| ---------- | ------------ | ---- | ------------ |
| 06:04:55   | 06:06:24     | 89 s | 45 to 57     |
| 06:07:10   | 06:07:37     | 27 s | 39 to 45     |
| 06:18:54   | 06:19:25     | 31 s | 30           |
| 06:27:17   | 06:27:29     | 12 s | 20           |

Leave lag (target about 10 s or less), 4 real exits: 2 of 4 within about 12 s.

| first `kill -0` failure | last listed | lag  | load (1 min) |
| ----------------------- | ----------- | ---- | ------------ |
| 06:08:23                | 06:08:29    | 6 s  | 27 to 64     |
| 06:19:06                | 06:19:38    | 32 s | 31           |
| 06:27:13                | 06:27:24    | 11 s | 20           |
| 06:32:50                | 06:33:02    | 12 s | 14           |

Host load (1 min): 41 at the start, 14 at the end, falling from about 30 at 06:19 to 14 to 23 after
06:21 (the run covered both a heavy and a quiet half).

`process.list` ok: 39.7 calls in 30 min (1.3 per minute; baseline about 1 per minute plus forced
discoveries for the 4 starts and 4 exits, inside the at most 6 per sessionless Process bound).

Scan cycle by 2 minute window (assemble mean, of which `assemble_resolve`, `db_write_sessions`, scans
per window):

| window start    | assemble     | assemble_resolve | db_write_sessions | scans    |
| --------------- | ------------ | ---------------- | ----------------- | -------- |
| 06:04           | 16.7 s       | 15.1 s           | 3.7 s             | 3        |
| 06:06           | 5.5 s        | 5.3 s            | 5.6 s             | 1        |
| 06:08           | 11.1 s       | 8.8 s            | 4.3 s             | 7        |
| 06:16           | 37.1 s       | 24.1 s           | 7.5 s             | 4        |
| 06:18           | 7.6 s        | 6.5 s            | 2.8 s             | 10       |
| 06:20           | 10.7 s       | 9.6 s            | 3.1 s             | 8        |
| 06:22 and later | 1.7 to 3.0 s | 1.5 to 2.5 s     | 0.8 to 1.2 s      | 13 to 18 |

Whole window: assemble 5317 ms mean over 175 scans (assemble_resolve 4433 ms, assemble_fold 117 ms,
assemble_providers 51 ms, assemble_pr 189 ms), discover 347 ms, `db_write_sessions` 2114 ms. At quiet
load (after 06:22) assemble is 1.7 to 3.0 s, matching the 1551 ms warm result of the post-apply check.

## Attribution

Each lag spans whole scan cycles: one cycle is the scan timer (about 5 s) plus assemble plus
`db_write_sessions`. A start or exit is only seen at the end of the next cycle, so the best case is
about one cycle (about 8 s at quiet load) and the worst case is two to four cycles.

- 89 s appear and 27 s appear: window 06:04 to 06:08, cycles of 17 to 40 s and longer. Named cause:
  assemble sub-phase `assemble_resolve` (15.1 s mean) plus `db_write_sessions` (3.7 to 5.6 s) at load
  41 to 64.
- 31 s appear and 32 s leave (06:18 to 06:20): cycles of 12 to 15 s with assemble 7.6 to 10.7 s
  (`assemble_resolve` 6.5 to 9.6 s), two to three cycles. Same named cause at load about 30. The
  `discover` phase also spiked to 760 ms in 06:18.
- 12 s appear, 11 s and 12 s leave, 6 s leave (quiet or lucky phase): one cycle to 1.5 cycles, about
  8 to 9 s at quiet load; the residual is the scan timer plus `db_write_sessions` (0.8 to 1.2 s) plus
  assemble (1.7 to 3.0 s), so the 10 s target is only barely reachable even at quiet load.
- The 6 s leave at load 27 to 64 passed because the exit fell just before a scan; it is phase luck, not
  a faster path.
- Gate try budgets (`maxGoneTries` = 4, `maxEmptyTries` = 6): no series records forced discoveries or
  an exhausted budget, so they can be neither confirmed nor excluded as the cause of any single lag.
  `process.list` ok stayed at about 1.3 per minute, so no budget runaway occurred. The 60 s discovery
  heartbeat is not implicated by the numbers (no lag landed near 60 s; the longest, 89 s, fits three to
  four cycles of 17 to 40 s).
- Recorder ingest latency: not measured here (no recorder-side timestamps); not excluded.

## Findings

1. The remaining lag is the scan cycle length under host load, dominated by `assemble_resolve`, then
   `db_write_sessions`. Even at quiet load the structural floor is about 8 to 9 s.
2. Whether the 10 s target must hold at load 30 to 60 is an operator decision, not an engineering one.
3. No metric exposes appear or leave lag directly, or the gate forced-discovery and try counts, so each
   verification needs a hand-run poll.
