Status: DRAFT pending the operator decisions

# Plan: ccpool recorder-load reduction (batched, persisted snapshot)

Status: DRAFT for operator review (2026-11-10). Written by a planning subagent from a read-only review; not yet critiqued or approved.
Tags: [V] verified from source by the planner, [I] inferred. Paths are under /Volumes/gitrepos/phillipg/nix-agent-support/packages.

## AMENDMENTS after independent critique (2026-11-10) — these OVERRIDE the sections below

1. The ccpool recorder op is named `sessions.facts` (per-id facts), NOT `sessions.snapshot`. The pa-monitor live-session list is served by an extended `process.list` (if_seq, ended_since, detail=registry); see the pa-monitor plan. Requests are `{session_ids (at most 500, required), fields[], since}`; add a per-call time budget and a `truncated` flag with partial results (each Engine.Show or List does several queries per conversation, engine.go:527-560, so 500 ids with awaiting and tokens could exceed the 30 s timeout). Add capability detection from hello with fallback to the old calls, update the capability-list tests (serve/daemon_integration_test.go:491, client/contract_integration_test.go:31), cancel_ops_test.go, README, and a query-plan guard test like TestLoadQueryPlansUseTheSourceIndex (engine.go:117-119) for each new query (awaiting_input and output_tokens on the 9.9M-record session are the risky ones; the missing sqlite_stat1 and the unary-plus issue can pick a wrong index).
2. Interim slice 2 (`session_ids` filter on conversation.list): the id filter MUST go inside `evidenceMatch`, BEFORE `transcriptFacts`. Engine.List runs transcriptFacts (cwd query, file-facts query, CurrentPaths, an exists check per file) for every conversation passing evidenceMatch, before the since and cwd filters (engine.go:611-619); an id filter added after would not reduce the work.
3. Slice 1 (stat-first, per-pass breaker, one WARN, 5 s timeout, 24 h positive TTL) is the best first change and needs no recorder change; land it first. Two additions: (a) the probe runs inside the per-id lock (reap.go:461-480, resumableForReap under withLock), so a batch result gathered before the lock MUST be re-verified under the lock before any delete; (b) behavior change to state: a transcript on disk that the recorder has not ingested is ErrNotFound today ("not resumable", row pruned, facts.go:315-337); with stat-first the row is kept. Benign, but call it out in the test fixtures.
4. Slice 3 (snapshotter built from existing ops) is worse than today before the recorder op exists, because it would run the old per-session queries for every pool row. Move it AFTER the recorder op (slice 5), or restrict it to alive-only.
5. `ccpool state` is per session (cmd/ccpool/state.go:40-60). A per-pool snapshot of up to 500 ids including awaiting_input only to answer one id contradicts "fetch only what the tool needs". Scope the refresh to the ids the calling command needs, or use a pool-wide refresher only for `list` and `reap`. Locks: ccpool flocks are per name (lock/flock.go), so single flight needs a NEW lock name with a defined waiter behavior (lock timeout, then use the stale local rows); SQLite is WAL with a 5 s busy timeout (store/store.go:110-128). Define rollback: ccpool must tolerate the migration-013 table being absent after a roll back.
6. Staleness: pg-router's needs_input edge detection and lease-release guards (executor/ccpool.go:1638, 2148) depend on awaiting; a 10 s stale awaiting answer could release a lease or worktree under a session waiting for input. Keep awaiting direct (bounded, since-floor, limit 50) for `list` and control paths as well as post-send. A `degraded` answer never drives a "not resumable" or a prune.
7. The cheapest-first order: slice 1, then 2, then 4 (bounded direct reads), then recorder `sessions.facts`, then the snapshotter.

Operator constraints (Phillip, 2026-11-10): fetch only what the tool needs; query a batch periodically instead of ad hoc N+1; querying every 5 to 10 seconds (or longer) and answering sub-queries from an in-app snapshot is preferred; state the time context of every request.

## 1. Current state

All reads go through ccpool/internal/facts/facts.go (Reader) [V].

| Request                                                                                                        | Cadence / N                                                                                                                                      | Time context                                        | Rows returned vs used                                                                                                              |
| -------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------ | --------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------- |
| Mark: Select[type] Limit 1, no where (facts.go Mark; send.go:64-68)                                            | 1 per confirmed send, plus 1 probe after a not-ingested window (send.go ~140)                                                                    | the instant "now"                                   | 1 row, only AsOf used                                                                                                              |
| feed long poll, session + main + user/assistant, after=mark, wait_ms up to 30000 (WaitMessage)                 | 1 per confirmed send, looping per page of at most 30 s                                                                                           | this live session, cursor-bounded                   | 0..1 entry, existence only                                                                                                         |
| conversation.show per dead row (OriginalsPresent; reap.go reapDeadRow -> resumableForReap, session.go:598-619) | every 300 s sweep (home/programs/ccpool/default.nix reap.intervalSeconds=300) x each dead row not positively cached for 1 h; errors never cached | one session, all history                            | a full Conversation (recorder engine.go:527-562: cwd query, file facts, CurrentPaths and an exists check per file) for one boolean |
| MessageActivity (newest user/assistant, limit 1)                                                               | per `ccpool state` on a live session (registry.go:96-100)                                                                                        | this session, no time floor                         | 1 row, 1 field                                                                                                                     |
| LastAssistantText (raw, limit 1)                                                                               | per `ccpool state` for idle or error, and per Idle send result (send.go:308)                                                                     | this session, no floor                              | 1 raw line                                                                                                                         |
| IsAwaitingInput: tool_use AskUserQuestion limit 1000, then ALL tool_results limit 10000                        | per `ccpool state` on a live session, plus every send timeout (send.go:294)                                                                      | this session, whole history                         | up to 11000 rows for one boolean                                                                                                   |
| OutputTokens: distinct-sum over message.id                                                                     | once per ended run (delete.go:76-95)                                                                                                             | whole session, then a stored snapshot is subtracted | 1 row, but a whole-session scan                                                                                                    |
| ProcessAlive (process.show); hello                                                                             | per `ccpool state`; hello once per process (PingOnce) plus the capacity gate                                                                     | now                                                 | 1 row                                                                                                                              |

Process shape [V]: ccpool is a one-shot CLI and reap-all is a timer-driven one-shot process, so there is no long-lived ccpool process; in-process caches (Reader.pinged, the Reader connection) die with each run. The only cross-process memory is the per-pool SQLite store.db, which already holds a recorder-derived cache, `originals_probe` (migration 012, internal/store/originals_probe.go), keyed by external_id and transcript_path with a positive-only 1 h TTL. The reap probe is serial per dead row with a 30 s timeout; each failure becomes `reap dead row %s: check resumable` in rowErrs, which explains the 1950 "check resumable" ERROR lines [V for the code path, I for the volume].

Where the failures come from [I]: a slow recorder scan (the session-pinned plan issue, fixed elsewhere) holds one of 8 query connections for 30 s; serial dead-row probes then queue behind it, one ERROR per row.

## 2. Target design

Principle: ccpool holds a per-pool fact snapshot and answers sub-queries from it. It is refreshed in ONE batched recorder request, on demand with a minimum age, and persisted in store.db. Only the post-send path stays direct.

Persistence: store.db, not memory (ccpool is a one-shot CLI plus a one-shot reaper). Add migration `013_fact_snapshot.sql`, one row per external_id: claude_session_id, transcript_path, alive, originals_present, originals_at, awaiting_input, awaiting_tool_use_id, last_message_at, last_write_at, last_text_record_ref, last_text_at, output_tokens_total, tokens_floor, recorder_as_of, fetched_at.

Refresh: `facts.Snapshotter.Ensure(ctx, pool, maxAge)`. If now - fetched_at < maxAge, read locally and make no request. Otherwise take a short flock or SQLite BEGIN IMMEDIATE single flight so concurrent CLIs share one fetch; send one `sessions.snapshot` for every pool row that has a transcript; upsert; a loser of the lock re-reads the table. Every caller states its time context: `scope: session_ids` (a known list, not "everything"), `since` (a floor) for tokens and activity, and `as_of_min`.

Max staleness by caller:

| Caller                                        | Source                                                                                                                                               | Max staleness           |
| --------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------- |
| `ccpool state` alive, awaiting, last activity | snapshot, maxAge 10 s                                                                                                                                | 10 s                    |
| `ccpool state` last_reply                     | snapshot gives the record ref and last_text_at; one raw fetch of that record only when state is idle or error and the ref changed; cache text by ref | 10 s                    |
| reap-all originals check                      | local stat first, then snapshot (maxAge 300 s)                                                                                                       | 300 s plus positive TTL |
| ended-run output tokens                       | snapshot with a tokens floor, forced refresh once at run end                                                                                         | exact at run end        |
| pool-metrics, janitor, router (via the CLI)   | whatever `ccpool state` or `list` read                                                                                                               | 10 s                    |

Staleness NOT acceptable, so these stay direct and bounded:

- Send confirmation. The Mark taken before delivery and the feed after=mark long poll (session, main, user/assistant filter, project [type], cursor-bounded) must be fresh: a mark from a stale snapshot's as_of could see an earlier turn's record and falsely confirm. Mark stays one Select[type] Limit 1; the not-ingested liveness probe stays (already limited to the not-ingested window).
- Awaiting-input immediately after a send. At the resolveOutcome timeout (send.go:294) do a direct bounded call: the newest unanswered AskUserQuestion for one session, since the send's mark time (at most 2 small rows). Until the recorder op exists, keep the two queries but add a `time >= sentAt` floor to both and drop the limits to 50 each. `ccpool state` called within 10 s of a send may use maxAge=0 for awaiting only.

## 3. Recorder-side requirements

New op `sessions.snapshot` (named to be shared with pa-monitor; see the reconciliation note at the end).

Request:

```json
{
  "session_ids": ["..."],
  "fields": [
    "alive",
    "originals_exist",
    "awaiting_input",
    "last_message_time",
    "last_write",
    "last_assistant_ref",
    "output_tokens"
  ],
  "since": "RFC3339",
  "as_of_min": "cursor"
}
```

(`session_ids` required, at most 500, the ccpool pool rows; `since` is the floor for last_message, awaiting and token sums; `as_of_min` is optional: wait for this input_seq.)

Response:

```json
{
  "as_of": "...",
  "degraded": false,
  "sessions": {
    "<id>": {
      "found": true,
      "alive": true,
      "originals_exist": true,
      "awaiting_input": { "open": false, "tool_use_id": "", "asked_at": 0 },
      "last_message_time": 0,
      "last_write": 0,
      "last_assistant_ref": { "seq": 0, "time": 0 },
      "output_tokens": { "since": 0, "total": 0 }
    }
  }
}
```

Each field is independently requestable so a caller pays only for the fields it names. `found:false` is an explicit answer for ids the recorder has never seen (today ErrNotFound, which ccpool maps to "not resumable"). `degraded:true` means the recorder is behind; ccpool must not read negatives from a degraded answer.

Cost [I; confirm against the store schema]:

- Cheap from the lifecycle model (loadCtx keyed on ModelInputSeq, engine.go:164): alive, last_write, the segment data.
- originals_exist needs CurrentPaths plus a stat per file: add a per-file `exists` memo keyed on the model seq plus a 60 s TTL (file facts are the existing fileFactsQueryRef).
- New indexed reads: last_message_time (limit-1 descending read per session on records_session_ref_source_time filtered on type; the batch resolves session_ref once and does N sessions in one request on one connection); awaiting_input (an incremental per-session "open AskUserQuestion tool_use_id" set updated on ingest, or an anti-join of tool_use name=AskUserQuestion against tool_result by tool_use_id bounded by `since`; the largest new piece); output_tokens (an incremental per-session accumulator keyed by message.id, or a `since`-bounded distinct sum; the whole-session distinct scan is the expensive read today); last_assistant_ref (a limit-1 read returning the record ref only; text then comes through the existing `raw` op).

Minimum useful interim, no new op: add a `session_ids` filter to `conversation.list` (a type change in ConversationFilter, types.go:178). One call then returns OriginalsExist, LastWrite and State for N dead rows. It is a small, separate recorder change that covers the reap-all fix alone.

## 4. Call-site changes

- internal/session/session.go:598-619 resumableForReap, reap.go:111-121, reap.go:460 reapDeadRow:
  1. Stat row.TranscriptPath locally. If it exists the row is resumable with no recorder call; write the positive result to originals_probe.
  2. Collect only the stat-missing rows and send ONE batched check for them (conversation.list{session_ids} as the interim, then sessions.snapshot). A stat-missing row is almost always a phantom deleted in the same pass.
  3. One availability decision per pass: on the first ErrDaemonOffline or timeout, stop probing, keep every unresolved row, log ONE WARN with the count. No per-row ERROR lines (rowErrs no longer appended for probe failures).
  4. Probe timeout from 30 s to 5 s. Positive TTL from 1 h to 24 h because the local stat now backs it.
  5. Negatives are not cached (a not-resumable dead row is deleted in the same pass).
- internal/facts/facts.go OriginalsPresent (~290): gains `OriginalsPresentBatch(ids) (map[string]bool, error)`; Conversation() per id remains only as a fallback.
- cmd/ccpool/registry.go:36-44 registryFacts: alive, awaiting and lastActivity become reads from the snapshot (Ensure maxAge 10 s). Replace factReader.ProcessAlive/IsAwaitingInput/MessageActivity with snap.Row(sessionID) accessors: 3 requests per `ccpool state` become 0 or 1.
- cmd/ccpool/state.go:64-80: the awaiting and lastText closures read the snapshot; lastText fetches one raw record by ref.
- internal/session/send.go:294: direct, bounded (since floor, limit 50 each) as in section 2.
- internal/session/send.go:308: LastAssistantText after an Idle result stays direct (limit 1) with a `time >= sentAt` floor.
- internal/session/delete.go:76-95 attributeRunTokens: OutputTokens becomes a snapshot read with since = run.StartedAt minus slack, or keeps the stored-snapshot subtraction in RecordRunOutputTokens as the correctness guard. Do not drop the subtraction until a fixture test shows parity.
- facts.go Mark and WaitMessage: unchanged. Optional recorder nicety: a feed `after:"head"` token so Mark and the first feed page are one request.
- Gate (facts_wiring.go:83, capacity.go:45): PingOnce stays; a successful snapshot refresh within maxAge can satisfy it without a hello.

## 5. Slices

1. S, no recorder change. Reap-all: local stat first, a per-pass circuit breaker, one WARN, 5 s probe timeout, 24 h TTL. Tests with a stub recorder that counts requests: N dead rows with existing paths cause 0 requests; N dead rows with missing paths cause at most N requests the first time, then at most 1 via the breaker when the stub hangs; a hung recorder yields 0 per-row ERROR lines and 1 WARN; fixture parity: a phantom is still pruned when the recorder answers "missing".
2. S, recorder: `session_ids` filter on conversation.list. Recorder tests: 3 ids requested, 3 rows returned, OriginalsExist correct, the model read once. Then a ccpool follow-up with a request-count assertion: N dead rows with missing paths cause exactly 1 request.
3. M, no recorder change. The store.db migration and the Snapshotter skeleton with single flight and maxAge, built from existing ops (a batched process.list alive plus the existing per-session reads, only when older than maxAge). Tests: two `ccpool state` runs inside 10 s cause one refresh; two concurrent CLIs cause one fetch (flock); answers unchanged on fixtures.
4. M, no recorder change. Bound the direct reads: since floors and smaller limits for the send-timeout awaiting check, the post-send LastAssistantText and the MessageActivity fallback. Tests assert the request shape (`where` contains the time floor, limit 50) and that confirmation tests still pass.
5. L, recorder change. `sessions.snapshot`: alive, originals_exist and last_write first, then awaiting_input, last_message_time, last_assistant_ref and output_tokens. Recorder tests: shape, degraded, found:false, field selection, and cost when nothing changed (model seq unchanged so no store read).
6. M, depends on 5. Switch ccpool to sessions.snapshot (registryFacts, state.go, token attribution), keeping the stored-snapshot subtraction. Tests: `ccpool state` makes at most 1 request per 10 s regardless of invocation count; a hung recorder makes `state` fall back to the last snapshot marked stale with no ERROR spam; token parity on fixtures with resumed sessions.
7. S, optional. feed `after:"head"` so Mark plus the first poll is one request.

Order: 1, 3 and 4 can land first against the existing recorder; 2 is a prerequisite for the clean form of 1; 5 gates 6.

## 6. Post-apply verification

- Recorder request log, per minute and per client `ccpool`, by op: conversation.show from the reaper falls from N per sweep to roughly 0; `query` count per `ccpool state` falls from about 5 to at most 1 per 10 s per pool.
- reap.err.log growth over 24 h: approximately 0 "check resumable" lines; during a deliberate slow-recorder window at most 1 WARN per sweep.
- `ccpool state` p50 and p95 latency falls to a local SQLite read between refreshes.
- Recorder query-connection use (max 8): no 30 s queueing (`wait_ms` about 30000 with `exec_ms` 0 no longer appears).
- Parity spot-check: compare `ccpool state --json` before and after on the same live sessions.

## 7. Risks and open questions

- Stat versus recorder disagreement [V definition, I impact]: the recorder's resumable test is "any file of the conversation exists" (up to 50 files); stat checks only the row's TranscriptPath. The stat-first shortcut is positive-only, so it cannot cause a wrongful prune; a path moved while another file survives falls through to the recorder check, so it is safe.
- Persisted snapshot staleness after a crash: a stale alive=true could survive; mitigated by the 10 s maxAge on liveness and tmux liveness staying local and authoritative.
- Awaiting-input semantics [V]: the facts.go docstring says "asked after the last assistant record that is not a question", but the code only diffs tool_use against tool_result. The recorder's awaiting_input should be defined as "newest AskUserQuestion with no tool_result". Operator confirmation needed.
- The 10 s maxAge is a guess. Is 10 s acceptable for `ccpool state` as the router sees it (router poll cadence not read, [I])?
- Is an on-demand refresh acceptable, or does the operator want a small long-lived refresher (it would change the "short-lived CLI" shape and needs a service-daemon-checklist pass)?
- `sessions.snapshot` field naming and the 500-id cap must be reconciled with the pa-monitor proposal.
- The token floor approach could shift attribution at run boundaries; keep the stored-snapshot subtraction until parity is shown.
- The recorder fix for session-pinned query plans is handled elsewhere; these slices assume it but do not depend on it.

Reconciliation note (added by the orchestrator, not the planner): the pa-monitor plan defines `sessions.snapshot` as "all live processes, optionally with a dead tail, with `if_seq` unchanged answers and registry facts (kind, entrypoint, waitingFor, statusUpdatedAt)"; this plan defines it as "these named session ids, with per-field facts (originals, awaiting, last message, tokens)". These are two different ops under one name. The critique step must decide whether to split them (for example `sessions.snapshot` for the live-session list, and `sessions.facts` for per-id facts) or merge them.

Critical files: ccpool/internal/facts/facts.go, ccpool/internal/session/session.go, ccpool/internal/session/reap.go, ccpool/cmd/ccpool/registry.go, pg-claude-code-recorder/internal/lifecycle/engine.go.
