# Investigation: desk-pr dispatch stall, 2026-10-08 20Z to 2026-10-09 01Z (bead `pg2-mhz7b`)

Found by verification bead `pg2-0a0su` (measured 2026-10-09 about 04:54Z). All times are UTC.
Evidence is local and was read without modifying any service: `~/.local/state/pg-router/events.jsonl`,
`~/.local/state/pg-router/launchd-stderr.log`, `~/.local/state/pg-desk/store.db` (opened with
`sqlite3 -readonly`), `~/.local/state/pg-connector-pr-github/events.jsonl`, `pg-desk status`, and the
local Prometheus on `127.0.0.1:9090`.

This repo is public, so the write-up names no organization PR, branch or tracker id.

## Summary

The stall was NOT the connector and NOT the router queue. pg-router's `LOW_DISK_USAGE` gate was held
because free space on `/` fell below the watchdog's 20 GiB floor. A gate halts every listener and
every polled source except the disk watchdog, so desk-pr, desk-issue, desk-heartbeat, the probes and
all pull sources stopped together for the 4h18m from 20:47:16Z to 01:05:10Z (except 22:41:06Z to 22:47:59Z, when
the gate's lease had lapsed while the daemon was down).

Independently, the 37318 s oldest-anchor age is a separate defect: after the 2026-10-08 `bd` 1.3.1
upgrade, pg-desk closed a merged PR's anchor bead BEFORE its child beads, and `bd` 1.3.1 refuses to
close a parent with open children. Those closures failed on every retry and froze four anchors'
check timestamps. That defect is fixed in this change.

| #   | Cause                                                            | Status                | Fix owner                          |
| --- | ---------------------------------------------------------------- | --------------------- | ---------------------------------- |
| C1  | `LOW_DISK_USAGE` gate held while free space was 13.7 to 19.3 GiB | PROVED                | operator + ZR config               |
| C2  | anchor closed before its children; `bd` >= 1.3.1 refuses         | PROVED, fixed here    | this repo (needs apply)            |
| C3  | desk-pr lane saturated (serial role, shared FIFO)                | PROVED as a condition | this repo / operator (`pg2-xg2k8`) |
| C4  | connector `gh` calls killed at their deadline                    | PROVED, not causal    | ZR config / operator               |
| C5  | `change_flow` unmigrated, `last_sweep` 2026-10-01                | PROVED not causal     | none (cutover is planned work)     |
| C6  | desk-pr dispatch cancelled by daemon shutdown (`exited -1`)      | PROVED, minor         | this repo (low priority)           |
| C7  | router daemon down 22:15:13Z to 22:39:22Z                        | SUSPECTED cause       | operator                           |
| C8  | disk consumers that took free space from 47.6 GiB to 13.7 GiB    | SUSPECTED             | operator                           |

## Timeline

```mermaid
timeline
    title 2026-10-08 to 2026-10-09 (UTC)
    19:00 : free space 47.6 GiB
    20:28 : daemon restarted for apply of new config and bd 1.3.1 (in-flight desk-pr cancelled, exited -1)
    20:34 : bd refuses writes, schema v53 to v66 pending (one desk-reconcile failure)
    20:45 : free space below 20 GiB
    20:47 : LOW_DISK_USAGE gate set; last desk-heartbeat at 20:46:51
    22:15 : daemon shutdown requested; down until 22:39:22
    22:41 : gate lease lapsed during downtime, all roles dispatch once
    22:48 : watchdog re-sets the gate; last desk-pr at 22:48:57
    22:46 : first "cannot close, open child issue(s)" failure (desk-reconcile)
    01:00 : free space 24.3 GiB, then at or above 25 GiB recover threshold
    01:05 : gate cleared, every role dispatches at 01:05:10
```

## C1: LOW_DISK_USAGE gate (proved)

The watchdog (`packages/pg-router-disk-watchdog`) sets the gate below `--min-free 20GiB` and clears it
at `--recover-free 25GiB`; the deployed values are in the ZR config. Every role other than
`disk-watchdog` (declared `non_blocking_gates = ["LOW_DISK_USAGE"]`) is blocked while it is held.

Evidence:

```sh
# Gate was active 20:50Z-22:15Z and 22:50Z-01:00Z (5 min samples, jq strftime is UTC)
curl -s "http://127.0.0.1:9090/api/v1/query_range?query=pg_router_gates_active&start=1791486000&end=1791522000&step=300"
# Free space on / in GiB, 10 min samples: 47.6 at 19:00, 18.5 at 20:50, 13.7 at 23:00, 24.3 at 01:00
curl -s "http://127.0.0.1:9090/api/v1/query_range?query=host_filesystem_free_bytes/1073741824&start=1791482400&end=1791522000&step=600"
# Blocked counts: pg_router_gate_blocked_total{type="LOW_DISK_USAGE"} by role (desk-pr 55, desk-heartbeat 674 by 01:05)
curl -s "http://127.0.0.1:9090/api/v1/query?query=pg_router_gate_blocked_total"
# Hourly dispatches by role: only disk-watchdog fires between 21Z and 00Z
jq -r 'select(.time>="2026-10-08T14" and .time<"2026-10-09T06") | [.time[0:13], .role] | @tsv' ~/.local/state/pg-router/events.jsonl | sort | uniq -c
# The gate lines the watchdog's own CLI calls left in the daemon log (local time, EDT = UTC-4)
grep -n 'gated by LOW_DISK_USAGE' ~/.local/state/pg-router/launchd-stderr.log
```

Every role that had a dispatch at 20:32:42Z, 22:41:06Z and 01:05:10Z shares the identical `started_at`
in `events.jsonl`: that is the gate lifting, not queue progress.

`pg_router_gate_expiries_total` is 1 around 22:45Z: the 7 minute lease lapsed while the daemon was
down, so roles ran from 22:41:06Z to 22:47:59Z until the watchdog set the gate again. That is the
designed behavior, not a bug.

Nothing alerted: `packages/pg-router/grafana/alerting/alerts.yaml` suppresses the source-failure rule
under any active gate and has no rule on how long a gate has been held.

Who fixes it:

- Operator: reclaim disk (a `pg-disk-reclaimer` exists) and identify what consumed about 34 GiB
  between 19:00Z and 23:00Z (C8, suspected: review and worker sessions running at the time).
- ZR config (`phillipg-nix-ziprecruiter`, not edited here): decide whether the cheap local roles
  (desk-pr, desk-issue, desk-heartbeat, desk-reconcile, the probes) should declare
  `non_blocking_gates = ["LOW_DISK_USAGE"]`, and whether 20/25 GiB suits a 461 GiB disk that sits at
  25 to 60 GiB free in normal use.
- This repo: add an alert on a gate held longer than a threshold (see follow-ups).

## C2: anchor closed before its children (proved, fixed here)

Evidence:

```sh
pg-desk status                                  # sync_errors: 4, oldest_anchor_check_age_seconds: 38251
sqlite3 -readonly ~/.local/state/pg-desk/store.db "select entity_id, bead_id, last_synced_at from ledger where kind='anchor' and bead_id<>'' and last_synced_content_hash<>'closed' order by last_synced_at limit 6;"
# 4 oldest rows: last_synced_at 2026-10-08T18:32Z to 18:44Z, all four also carry interpretation.sync_error
jq -r 'select(.level=="warn" and (.error|test("open child"))) | .time[0:16]' ~/.local/state/pg-router/events.jsonl
# 18 desk-pr and 8 desk-reconcile failures from 22:46Z: "cannot close X: N open child issue(s); close children first or use --force"
bd --version                                    # bd version 1.3.1
ls ~/.local/share/ | grep beads-dolt            # beads-dolt.pre-1.3.1-20261008 (the upgrade backup)
```

`handleClosure` in `packages/pg-desk/internal/sync/rules.go` closed the anchor first and the cycle,
review and other children after. `bd` 1.3.1 (applied 2026-10-08) refuses that. Consequences:

- the anchor's ledger `last_synced_at` never advanced, which is exactly what
  `pg_desk_oldest_anchor_check_age_seconds` measures (18:32Z to 04:54Z is 37318 s);
- each retry re-stamped `closed_at` on the anchor before failing, bumping `updated_at` and echoing an
  `issue.changed` dispatch;
- every closed-PR dispatch failed three times (`max_dispatch_retries = 2`) and `desk-reconcile`
  exited 1 on every run.

Fix (this change): children first, the anchor last, and the terminal-state stamp is written only
after the children are closed. Tests: `TestSync_ConfirmedClosure_ClosesChildrenBeforeAnchor` (red
before the change: `cannot close bd-anchor-existing: 3 open child issue(s)`) and the updated
`TestSync_ConfirmedClosure_ConnectorErrorIsReturnedAndRetrySafe`.

Limits, not fixed here: children of children (a feedback cycle's own children) are still not
walked; the cascade only reaches what the work-beads read returned.

All eight affected anchors were closed by hand at 04:13Z to 04:48Z, but at 05:09Z the store still
lists four of them with a `sync_error`. Whether re-driving an already-closed anchor is a no-op in
`bd` is NOT verified (no mutation was run).

## C3: desk-pr lane saturation (proved as a condition, not as the stall's cause)

Already tracked as `pg2-xg2k8`. New measurements on 2026-10-09 05:10Z, excluding the four C2 rows:
41 of 81 open anchors had `last_synced_at` older than 3600 s, 15 older than 7200 s, the oldest
11826 s. Per-PR desk-pr gaps over 65 minutes (consecutive `events.jsonl` dispatches of the same PR,
gap start on the given day, same method for all three days):

| Day        | Gaps | Over 65 min | Share | Note                                     |
| ---------- | ---- | ----------- | ----- | ---------------------------------------- |
| 2026-10-06 | 3138 | 278         | 8.9%  | pre-apply baseline                       |
| 2026-10-07 | 2188 | 545         | 24.9% | before the stall                         |
| 2026-10-08 | 2319 | 390         | 16.8% | 87 of the 390 overlap the two gate spans |

So the per-PR gap regression the bead reported (27.0% by the bead's own method, which this method does
not reproduce) predates the stall; the stall explains only about a fifth of the 2026-10-08 long
gaps. The cause is queue wait on a saturated serial lane, not an outage.

```sh
jq -r 'select(.role=="desk-pr" and .time>="2026-10-08T00:00" and .time<"2026-10-09T06:00") | [(.bead|sub("@.*";"")), (.time|sub("\\.[0-9]+Z$";"Z")|fromdateiso8601)] | @tsv' ~/.local/state/pg-router/events.jsonl | sort -k1,1 -k2,2n
```

## C4: connector unavailability (proved, not causal)

`pg-connector-pr-github` logged 26 `list failed: unavailable` between 19Z and 05Z, all `gh ...: signal:
killed` (the connector's own 30 to 40 s deadline), plus one `unauthenticated` from a killed
`gh auth token`. The log has 788 info lines in the 01Z hour and 18 across 23Z and 00Z and none at 21Z, so the
connector was healthy while the stall lasted. Related earlier work: `pg2-r9ly8` (host overload).

## C5: change_flow unmigrated (proved not causal)

`pg-desk status` prints `change_flow: unmigrated` and `last_sweep: 2026-10-01` because the store is
still schema version 1. The deployed path reaches pg-desk through `pg-router-source-pg-connector`
(`pr-sweep`, `pr-mine`, `pr-team`), not `pg-desk changes`, so neither field reflects the live sweep.
The cutover (`pg2-2j5ac.52`) is the structural fix for C3 and is planned work, not a stall defect.

## C6 and C7

C6: at 20:28:20Z the daemon was told to shut down; after the 20 s drain timeout it cancelled the
in-flight desk-pr dispatch, which logged `exited -1` at 20:28:40Z. Session roles survive shutdown;
command roles such as desk-pr do not, and a cancelled dispatch is not retried by the router.

C7 (suspected): `launchd-stderr.log` shows `run: shutdown requested` at 22:15:13Z and the next start
at 22:39:22Z. `~/.local/state/beads-migration/alt-server-config.yaml` was written at 22:16Z and the
`bd` schema migration (v53 to v66) was refused at 20:34Z until then, so this looks like the operator's
migration. Not proven: nothing in the router log names the reason.

## Post-apply checks for the C2 fix

After `pn workspace apply` of a commit containing the fix:

1. `pg-desk status` reports `sync_errors: 0` after the next `desk-reconcile` tick (30 min), and
   `oldest_anchor_check_age_seconds` is no longer dominated by the four stuck anchors.
2. No `cannot close ... open child` in `~/.local/state/pg-router/events.jsonl` after the apply time.
3. A merged PR whose anchor still has an open child closes child then anchor in one run.

## Proposed follow-up beads

| Title                                                                              | Scope (one line)                                                                                                          | Owner          |
| ---------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------- | -------------- |
| pg-desk closure order fix: verify after apply (C2)                                 | Gated verify bead for the three post-apply checks above, then re-measure `pg2-0a0su` check 5.                             | this repo      |
| pg-router: alert when any gate is held longer than a threshold (C1)                | New rule in `packages/pg-router/grafana/alerting/alerts.yaml` on `pg_router_gates_active == 1` for over 30 min, per type. | this repo      |
| ZR config: scope `LOW_DISK_USAGE` for cheap local roles and re-rule 20/25 GiB (C1) | Decide `non_blocking_gates` for desk-\* and probe roles and the thresholds in `phillipg-nix-ziprecruiter`.                | ZR config      |
| Operator: attribute the 2026-10-08 disk drop and reclaim (C1, C8)                  | Find what took 47.6 to 13.7 GiB between 19Z and 23Z (review and worker worktrees, nix builds); schedule reclaim.          | operator       |
| desk-pr capacity: attach 2026-10-09 measurements to `pg2-xg2k8` (C3)               | Add the 41 of 81 stale anchors and the 8.9% / 24.9% / 16.8% gap shares as evidence on the existing bead.                  | this repo      |
| pg-desk closure: walk grandchildren of cycle beads (C2 limit)                      | Close a cycle bead's own open children before the cycle so `bd` 1.3.1 accepts it.                                         | this repo      |
| pg-router: do not cancel in-flight command-role dispatches at shutdown (C6)        | Design question: longer drain or re-offer for command roles whose runs take 20 to 160 s; low priority.                    | this repo      |
| pg-desk status: label `last_sweep` and `change_flow` as cutover-only (C5)          | Stop `last_sweep: 2026-10-01` reading as a live-sweep age; small output change.                                           | this repo      |
| Connector `gh` calls killed at deadline under load (C4)                            | Re-measure after the host-overload caps (`pg2-r9ly8`); no new code unless kills persist.                                  | ZR config / op |
