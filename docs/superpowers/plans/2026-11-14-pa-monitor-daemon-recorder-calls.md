Status: DRAFT pending the operator decisions

# Plan: one periodic discovery snapshot for pa-monitor-daemon

Status: DRAFT for operator review (2026-11-10). Written by a planning subagent from a read-only review; not yet critiqued or approved.
Tags: [V] verified from source by the planner, [I] inferred. Paths are under /Volumes/gitrepos/phillipg/nix-agent-support/packages.

## AMENDMENTS after independent critique (2026-11-10) — these OVERRIDE the sections below

1. DO NOT SHRINK GC `keepIDs` (the draft would lose history). `keepIDs` today is every session the recorder knows (alive or dead, dead capped at 500: daemon/gc.go:107-117, recorderfacts/facts.go:26,126); `MarkSessionsDeleted(keepIDs)` soft-deletes every pa-monitor row NOT in it and a hard delete follows 24 h later (gc.go:42-56). So slice 1 must not drop the dead read from the GC path, and "24 h" is the hard-delete grace, not a keep window. GC keeps reading the full known-id set (or a recorder-side "known ids" answer), never the discovery snapshot, and never acts on a `Stale` or `degraded` snapshot. [critique, verified at file:line]
2. Drop the new op name `sessions.snapshot` for pa-monitor. Extend `process.list` instead: add `if_seq` and `ended_since` to ProcessFilter, plus an opt-in `detail:"registry"` adding kind, entrypoint, waiting_for, status_updated_at; response `{processes, degraded, seq, unchanged}` with no processes when unchanged. Per-id facts for ccpool are a separate op (`sessions.facts`, see the ccpool plan). Add capability detection from hello with a fallback to the old calls so a new pa-monitor still works on an older recorder; update the capability-list tests (serve/daemon_integration_test.go:491, client/contract_integration_test.go:31), cancel_ops_test.go (19-39), and README.md (process.list at lines 633, 661). No protocol-version bump is needed (wire.ProtocolVersion=1 is additive).
3. `entrypoint` is a records column (`store:"record:entrypoint_id"`), not necessarily a `fields` row, so the extended registry query must join entrypoint_id; kind and status_updated_at are stored fields [verified, source/sessionregistry/registry.go:20-41].
4. The "unchanged when nothing committed" claim needs care: ModelInputSeq moves on every committed session-registry record, every history or control-end record, every TProc/TStage/TSettle/TTorn journal record and every row-clearing write (parser.go:1057-1059, 787; store.go:773-783, 1170-1179), and each miss re-reads EVERY registry version ever stored (registryQuery, engine.go:129-135, no time bound). Measured on a clone of the live store: 17,005 registry versions for 885 processes took 1.83 s to read, and only 166 arrived in the last hour. The recorder must bound `registryQuery` (live and recently ended processes only) or load incrementally; without that, batching three client calls into one does not remove the reload cost. This recorder change is part of the plan (new slice R, ahead of the op change).
5. The file inventory is NOT the current source of load. A recorder doc of 2026-11-09 (docs/investigations/2026-11-09-op-query-slow-and-cancelled.md) names the FilesInventory bootstrap as dominant, but the live log shows that group-by shape stopped on 2026-11-09 at about 15:00 EST after commit 1aa480161 (files.inventory: 7 requests, 0.9 s total since the restart), while the registry-in-list query kept running (565 slow requests, mean 14.9 s, to 2026-11-10 15:00). Still add the bootstrap-convergence check (keep partial progress, bound the window) as a small slice, because the doc describes a failure that can return.
6. Best first slice (replaces the draft's slice 1): add an id restriction (live ids only) and a time floor to the 5000-row records query, and fix nothing in GC. Then the periodic refresh at the 5 s floor.
7. State explicitly: control and nudge actions resolve their target live; a `degraded` snapshot never drives GC or deletion.

Operator constraints (Phillip, 2026-11-10): fetch only what the tool needs; query a batch periodically instead of ad hoc N+1; querying every 5 to 10 seconds (or longer) and answering sub-queries from an in-app snapshot is preferred; state the time context of every request.

## 1. Current state (per Scan)

Scan runs at max(RefreshInterval, 5 s) [V: pa-monitor/internal/core/poller/changesource.go:10,28]. RefreshInterval defaults to 1 s [V: config/config.go:242]. Scan order is Discover, sync, folds [V: core/corpus/monitor.go:~105-130].

| Request                                                                                          | Where                      | Cadence                              | Time context                     | Rows returned vs used                                                                                             |
| ------------------------------------------------------------------------------------------------ | -------------------------- | ------------------------------------ | -------------------------------- | ----------------------------------------------------------------------------------------------------------------- |
| process.list alive=true, limit 1000                                                              | recorderfacts/facts.go:122 | every Scan                           | live now                         | N live vs N used                                                                                                  |
| process.list alive=false, limit 500                                                              | facts.go:126               | every Scan                           | all history, capped at 500       | up to 500 vs 0 new; already persisted until the 24 h GC [V: daemon/gc.go]                                         |
| query records, source=session-registry, session IN (live + dead ids), time desc, raw, limit 5000 | facts.go:156-171           | every Scan                           | all history, no time bound       | up to 5000 raw rows vs 1 per live session; a busy session can crowd out others and the cap truncates silently [V] |
| FeedPage (transcript + status), page 1000                                                        | core/corpus/reader.go:300  | every Scan                           | since cursor                     | the delta                                                                                                         |
| readLines (file, item=-1, offset > next)                                                         | reader.go:369              | per dirty file                       | since offset                     | the delta                                                                                                         |
| FilesInventory                                                                                   | reader.go:239, 268         | bootstrap, plus once per new session | bounded by cutoff or session ids | bounded                                                                                                           |
| GC Registrations                                                                                 | daemon/gc.go:115           | hourly                               | same as above                    | a second full copy of the three discovery queries [V]                                                             |

Other facts [V]: Discover has a 20 s timeout and an error fails the whole Scan (monitor.go:111-114). Producer.Assemble publishes an empty "Offline" state on ErrDaemonOffline (producer.go:~198). process.list carries no kind, entrypoint, waitingFor or statusUpdatedAt; the recorder's registryQuery (internal/lifecycle/engine.go:129) also omits kind, entrypoint and status_updated_at. process.list is served from loadCtx, which caches the whole model on ModelInputSeq (engine.go:164-175), so the two process.list calls are cheap when nothing changed; the records query is the expensive one.

## 2. Target design

One `discoverySnapshot` owned by a new `recorderfacts.SnapshotSource`, built by one batch request and published as an immutable struct through an atomic pointer.

Contents: Sessions (PID, SessionID, Cwd, Name, Status, StartedAt, Kind, Entrypoint, WaitingFor, StatusUpdatedAt, PidAlive, Host), FetchedAt, Seq, Err, Stale, Age. Time context: live-now only plus a short bounded "recently ended" tail.

Refresh: a background goroutine every `discovery_interval` (default 5 s, configurable, floor 5 s, reusing MinPollInterval at facts.go:19), outside Scan so a slow recorder cannot stall Scan. Scan, GC, the CLI and the tree builder read the pointer. Single flight; the fetch timeout is 10 s, below the 20 s discoverTimeout, so a hung request cannot hold one of the recorder's 8 query connections for long.

Replaced: both process.list calls and the registry-in-list query become one `sessions.snapshot` call (section 3). Dead processes are no longer read; a session alive in the previous snapshot and absent or dead now is carried forward as PidAlive=false with its last-known fields, for 24 h to match GC. A daemon that restarts mid-death loses only the dead flag of a session that died while it was down; one `alive=false` read at startup is the fallback.

Stays event-driven: FeedPage drain, readLines by offset, FilesInventory. The Scan fold phase still runs every tick.

Gate: refetch when (a) the feed shows a registry change (a second small feed cursor with where source=session-registry; do NOT add registry to the transcript drain, it would create bogus fileState entries [V: reader.go:300-330]), (b) the slow heartbeat expired (default 60 s), or (c) the previous fetch failed. With the new op the gate is built in: the request carries `if_seq` and the answer is `{unchanged:true}` when ModelInputSeq has not moved.

Caveat [I]: registry status flips count as registry changes, so on an active machine the gate fires most periods. The real saving is one call instead of three plus the cheap unchanged answer.

Errors: keep the last good snapshot, set Stale and Err, back off to 2x the interval (max 30 s) with jitter. Scan never fails because of discovery. On ErrDaemonOffline serve the last snapshot for `max_stale` (proposed 60 s) then publish the existing Offline state. Cold start with no snapshot returns ErrDaemonOffline as today.

Maximum staleness in the healthy case: discovery_interval plus a Scan tick, about 10 s at defaults; during failure, `max_stale`, then Offline.

## 3. Recorder-side requirements (SHARED with ccpool: op name `sessions.snapshot`)

New op `sessions.snapshot`, KindQuery, registered next to process.list in serve/daemon.go:587, served from Engine.loadCtx (free when the model is unchanged).

Request:

```json
{
  "alive": true,
  "ended_since": "RFC3339",
  "limit": 1000,
  "if_seq": 12345,
  "fields": ["registry_extra", "host", "control_inbox"]
}
```

(`alive` is true, false or null for both; `ended_since` omitted means no dead tail; `if_seq` comes from the previous answer; `fields` is opt-in.)

Response:

```json
{
  "seq": 12345,
  "as_of": "...",
  "unchanged": false,
  "degraded": false,
  "sessions": [
    {
      "pid": 0,
      "proc_start": "",
      "session_id": "",
      "name": "",
      "status": "",
      "cwd": "",
      "started_at": 0,
      "alive": true,
      "host": "",
      "control_inbox": "",
      "kind": "",
      "entrypoint": "",
      "waiting_for": "",
      "status_updated_at": 0,
      "last_seen_alive": 0,
      "exit_time": 0
    }
  ]
}
```

Recorder changes: extend registryQuery (engine.go:129) and regRow (engine.go:79) with kind, entrypoint, status_updated_at (the source struct already stores entrypoint and statusUpdatedAt [V: source/sessionregistry/registry.go:32,41]; kind is [I] a stored field and must be checked); add the new fields to the Process result only for this op (do not change process.list or process.show shapes); return `degraded` when !d.backfillDone as opProcessList does; add the op to the cancel-ops audit (cancel_ops_test.go); add Client.SessionsSnapshot in client/lifecycle.go. A different op already named `snapshot` (feed state at a cursor, daemon.go:582) exists; do not reuse that name.

## 4. Call-site changes

- recorderfacts/facts.go:116-195 `Registrations`: a thin reader of the snapshot; the fetch moves to SnapshotSource.refresh; delete the `done` map loop, registryExtra and the 5000-row query once slice 3 lands.
- facts.go:122,126: slice 1 keeps only the alive call; the dead call runs once at startup and when the snapshot is empty.
- facts.go:156-166: slice 1 restricts ids to live sessions and adds a time floor of now - 24 h (bounded interim).
- core/session/discovery.go:51-70 `Discover`: reads the snapshot; the 20 s timeout moves to the fetch; env reads stay, cached per (sid, pid) [I].
- core/corpus/monitor.go:111: Discover errors become impossible once a snapshot exists; Stale becomes a metric label.
- daemon/gc.go:115 `keepIDs`: reads the snapshot including the carried-forward tail and keeps all rows when Stale (preserves "offline keeps every row" [V: gc.go:~36]).
- core/corpus/reader.go:300: unchanged (add the registry cursor drain only if slice 2 ships on the old recorder).
- config/config.go:21,166: add discovery_interval_ms (floor 5000), discovery_heartbeat_ms, discovery_max_stale_ms.

## 5. Slices (sizes S/M/L/XL)

All tests use a real recorder with a recording proxy; base: the recordingProxy helper at recorderfacts/sessions_test.go:126 and the corpustest.Env harness.

1. S: stop re-reading dead and old data (old recorder; lands first). Drop the per-Scan dead read, restrict registry ids to live, add a 24 h floor, carry dead state forward in memory. Tests: per Scan exactly 1 process.list (alive) and 1 records query, the `session IN` set holds only live ids, a session dying mid-run still persists as dead with its DB row unchanged, startup does one dead read.
2. M: periodic snapshot source (old recorder; lands second). Background refresh with floor, single flight, last-good snapshot, Stale and Err, backoff, max_stale to Offline; Scan, GC and CLI read memory; registry-cursor gate and heartbeat. Tests: discovery requests do not scale with Scan count (10 Scans in 1 s produce at most 1 fetch); fault tests (delayed or dropped proxy leaves Scan succeeding with Stale=true; recorder stopped and restarted gives Offline after max_stale then recovery; a 20 s hang does not stall Scan).
3. L: recorder op `sessions.snapshot` (needs a recorder change). Add the op, the extended registryQuery, the client method and the if_seq path; switch pa-monitor and delete the records query. Recorder tests: new fields from the newest version, unchanged when nothing committed, degraded while backfilling, cancel-op audit, the huge-session case does not scan. pa-monitor tests: per refresh exactly 1 `sessions.snapshot`, 0 `query`, 0 `process.list`; an equivalence test against the old three-call answer before deleting old code.
4. S: staleness surfacing. Metrics snapshot_age_seconds, snapshot_stale, discovery_errors_total and tree health; test: age rises while the proxy blocks and resets on recovery.
5. S: cleanup. Remove registryExtra, the dead Registrations path and the old tests.

Order: 1 and 2 land first against the existing recorder; 3 is the only slice needing a recorder change and needs the ccpool plan reconciled on the shared shape; 4 and 5 follow 3.

## 6. Post-apply verification

- Recorder log, client pa-monitor-daemon, requests per minute by op. Before: about 12 Scans per minute x 3 discovery calls plus the feed. After: at most 60 / discovery_interval refreshes per minute (12 at 5 s, fewer when gated), 1 request each.
- No slow_request lines for the records-by-session query; no discoverTimeout cancellations; no wait_ms over 1 s with exec_ms 0.
- Kill a test claude and see it dead within 10 s; snapshot_age_seconds under 2x the interval; snapshot_stale recorded while the recorder is stopped and the Offline state after 60 s; dead sessions in the DB at startup unchanged.

## 7. Risks, where delay is not acceptable, open questions

Risks: dropping dead reads loses PidAlive=false for a session that dies while the daemon is down (slice 1 startup read covers it [I]); the registry gate may fire almost every period [I]; a stale snapshot may hide a just-started session for up to one interval; Kind as a stored registry field is not yet verified; GC must never soft-delete because of a stale snapshot (keep all rows when Stale).

Delay NOT acceptable: control and nudge actions through pg-claude-control (resolve the target live, gate on snapshot Age and re-check one process.show before acting); GC deletion decisions (only on a fresh snapshot); the Offline state must still appear when the recorder is really down; authoritative cost and limits stay on the feed.

Open questions for Phillip:

1. On ErrDaemonOffline, keep serving the last snapshot for max_stale (proposed 60 s) or show Offline immediately as today?
2. Is a 5 s default and 5 s floor right, or should a quiet machine back off to 10 s or more?
3. Keep dead sessions in memory 24 h (GC parity) or fewer?
4. Does ccpool need `ended_since` and `fields=registry_extra`? (To reconcile with the ccpool plan.)
5. Is a 60 s heartbeat right when no registry change is seen?

Critical files: pa-monitor/internal/recorderfacts/facts.go, pa-monitor/internal/core/session/discovery.go, pa-monitor/internal/core/corpus/monitor.go, pg-claude-code-recorder/internal/lifecycle/engine.go, pg-claude-code-recorder/internal/serve/lifecycle_ops.go.
