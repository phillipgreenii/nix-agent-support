# Plan: the recorder totals token usage; pa-monitor stops folding transcripts for cost

Status: APPROVED DIRECTION. The operator answered the open questions on 2026-11-17 (Phillip); the answers are folded into the body below and listed in "Operator rulings". Evidence from spikes on an APFS clone of the live store plus two read-only code reviews, then an independent critique.
Operator direction (Phillip, 2026-11-10): "pa-monitor can own a pricing table, but i think it doesn't need to pull all of the transcripts from recorder ... the recorder [should track usage windows]" and "i agree, continue with this. do the research and/or spikes you need to verify this and then write it up as a plan".
Tags: [M] measured on the clone (a copy of the live store taken 2026-11-10 about 15:00 EST; its newest record is 20:01 UTC), [V] verified in source by a reviewer, [I] inferred.

## Operator rulings (Phillip, 2026-11-17)

These override anything older in this plan.

- **Dedupe scope: global.** Usage is de-duplicated GLOBALLY by message id across all files, not per file. This is a deliberate change in reported cost: about 10.2 percent lower than the per-file rule on last week's data (see section 2.4 for the measured figures).
- **Removed history: derive from live records.** The derived table is computed from the records that currently exist; rows for removed or truncated history are not kept (no orphans).
- **Estimated block start accepted.** The 5-hour block start is an estimate; weekly totals are exact (Monday-anchored week). ADR 0021 ("pa-monitor plan model and rate-limit source") is the precedent.
- **Degraded or offline cost display.** While the recorder is reachable but degraded, the TUI and the OTel cost gauges hold the last good value marked stale for `StaleAfter` (10 minutes), then show it as unavailable.
- **No new limits op by default.** Rate-limit readings use the EXISTING `query` op: group on the resets field (`five_hour_resets_at`, `seven_day_resets_at`) with `max` aggregates. A dedicated `limits.readings` op MUST be added only if the query-op form is measured slow. ADR 0029 (Decision: the reader holds the current window's peak, with a newest-record fallback when there is no `resets_at`) is kept: a status record with no `resets_at` falls back to the globally newest record's value.
- **Recorder owns the derived reads.** "the recorder owns the derived reads": no client-side copy of derived state is added elsewhere because of this plan (see the other two plans).

## AMENDMENTS after independent critique and follow-up measurements (2026-11-10) — these OVERRIDE the sections below, except where an operator ruling above says otherwise

Follow-up measurements on the clone [M], run to settle the reviewer's doubts:

- CORRECTION to section 2.1/2.4: I mixed two windows. The "461 messages span files" figure is for the 7 days ending 2026-11-10 20:01 UTC; the $417 cross-file difference is for the week 2026-11-02 to 11-09. For that WEEK, 8,100 messages have copies in more than one file, the extra cost of the second and later copies is $417.17 (average $0.052 per message, about the same as the overall average of roughly $0.057 per message), and the maximum is 3 files. So the reviewer's "16 times average" worry does not apply: the gap is explained by many cheap duplicates, not a few huge ones.
- Cause of the cross-file copies in that week: 8,036 of the 8,100 messages sit in the SAME session (64 in different sessions); 3,551 have a copy in a subagent file and 4,549 have all copies in main files. So the repeats come from both a resumed or forked session writing a new file with the same session id (main-only) and subagent files repeating parent message ids; neither is a different conversation. Per-file dedupe therefore overcounts the week by about 10.2% of the per-file figure (($4,106.76 - $3,689.59) / $4,106.76 = 417.17 / 4,106.76).
- No usage row lacks a message id (0 of 120,709 in 7 days); no usage row has time 0 (0); no row has an empty model (0). The 4,395 all-zero rows are the `<synthetic>` error lines (4,393 error lines carry usage fields, all zero). So the "non-zero counters" filter removes exactly the error and synthetic rows.
- Status-line volume: 5,827 `claude-status` rows in 7 days from 445 sessions (104,107 all-time); too many to return on every poll.

Required changes to the plan (from the review; each is part of the slices):

1. Idempotence under re-parse. A message-id upsert with min time and per-field max cannot undo anything, and `clearGen` (parser.go:786-826) deletes a generation's records on a re-parse or truncation without touching a derived table. In `clearGen`, collect the message ids of the deleted usage records and RECOMPUTE those ids from the records that remain (the same pattern as `recomputeSessionStats`), deleting a `usage_messages` row when no copy remains. Keys: `message.id` when present; otherwise `(file_id, off, item)` (the record id is AUTOINCREMENT and is reissued on every re-parse, so it cannot be the key). Maintenance points: `flushFileFacts` at the end of each batch transaction (parser.go:632, 774) and the bulk path (store/bulk.go:11-30). Operator ruling (Phillip, 2026-11-17): the table is DERIVED FROM LIVE RECORDS and orphan rows are not kept, which matches how the recorder's other derived tables behave.
2. The op applies pa-monitor's filter: non-error lines only, model non-empty, at least one of the four counters non-zero (do not store zero or error rows); skip time_ns = 0 rows from the hour buckets (none observed).
3. State the input horizon. pa-monitor's block phase is a chain from the first record, with no inactivity reset, and its `pricingWindow` selects FILES, so it also sees older records inside those files (monitor.go:83-97, reader.go:198-201). The op takes an explicit `from`; pa-monitor passes `from = (now - pricingWindow) - 5 h`. Exact phase parity after more than a week of continuous activity is NOT promised: block start is declared estimate-only, as the existing comment at monitor.go:83-90 already says. Weekly totals are exact for the Monday-anchored week.
4. RATE LIMITS are NOT a field of `usage.totals`. They are read as FACT aggregates, not an election, through the existing `query` op: group on `five_hour_resets_at` (and on `seven_day_resets_at`) over `[since, now]` with `max` aggregates of the percentage and of the reading time, plus the host (operator ruling, Phillip, 2026-11-17). A few rows per week, small enough to poll, and pa-monitor's `limits.windowPeak` election (limits.go:157-215) stays in pa-monitor, so the recorder does not take on interpretation. A status record with no `resets_at` falls back to the globally newest record, as ADR 0029 (Decision: the reader holds the current window's peak, with a newest-record fallback when there is no `resets_at`) decided. A dedicated `limits.readings` op is added ONLY if the query-op form is measured slow (slice U6). "Last 20 rows" is withdrawn: it could miss the peak row of the elected window.
5. Completeness and rollback. Add the table with `CREATE TABLE IF NOT EXISTS` in derivedTablesSQL (derived.go ~60-115) and a layoutIndexes entry (derived.go:338); no `SchemaVersion` bump (precedent: the `seen` table and the file_facts backfill; Open refuses only a LOWER version). Add a completeness marker, a backfill cursor and a drift check modeled on `MetaFileFactsComplete`, `BackfillFileFacts` and `CheckFileFacts` (parser/filefacts.go:87, 163), so a rollback to an older recorder (which would stop maintaining the table) followed by a re-upgrade is detected and re-backfilled rather than silently undercounting. The op returns `degraded` until the marker says complete.
6. Consistency across tools. ccpool's `OutputTokens` uses per-file `distinct message.id` (ccpool/internal/facts/facts.go:285-300); per-session output totals move to this table (through `sessions.facts` in the other plan) so the two tools agree. pg-ccaudit `cost` is a classifier-spend ledger and is out of scope.
7. Degraded and offline behavior of consumers (poller, aggregate, OTel emitter, TUI, status_json all depend on cost): when `usage.totals` is `degraded` (recorder reachable), pa-monitor holds the last good block and week, marks them stale in the TUI and as a label on the OTel gauges for `StaleAfter` (10 minutes; operator ruling, Phillip, 2026-11-17), then reports them as unavailable. It falls back to the legacy fold only while the shadow flag is on.
8. Do not interpret any shadow-mode difference until the legacy path's completeness is checked: replicate pa-monitor's exact file set (files with records since the cutoff plus active sessions) and price only that, and check the daemon log for fold, pricing and read-timeout errors (`pricingObs`, `foldErr`, `readTimeout`). The stored current week ($159.34) is below the sum of the stored 2026-11-10 blocks (75.39 + 91.58 + 34.70 + 38.08 = $239.75), which points to an incomplete or differently phased weekly fold in the legacy path.
9. Sequencing: this plan, the pa-monitor discovery plan (extended `process.list`) and the ccpool plan all touch `registerOps`, the sorted capability lists (serve/daemon_integration_test.go:491, client/contract_integration_test.go:31), cancel_ops_test.go and the README. Land the plumbing slices serially, in this order: R (bound `registryQuery`), U1, U2 (`usage.totals`), then the extended `process.list`, then `sessions.facts`.

Questions (a) to (e) of the critique were answered by the operator on 2026-11-17 (see "Operator rulings" above): derive from live records, global dedupe, estimate-only block start accepted, stale hold for 10 minutes, and no separate `limits.readings` op unless measured slow.

## 1. Summary of the decision this plan asks for

- The recorder keeps a small DERIVED table, one row per assistant message (de-duplicated by message id), and serves hourly token totals from it through one new read op, `usage.totals`.
- pa-monitor keeps pricing, the block and week definitions and the plan caps, and builds its 5-hour block and Monday-anchored week from those hourly totals. It stops reading transcript lines to compute cost.
- The authoritative 5-hour and 7-day limit percentages stay with the status-line capture (ADR 0021 ("pa-monitor plan model and rate-limit source")). Limit readings are read through the existing `query` op, grouped on the resets fields with `max` aggregates; no new op carries them.

## 2. What the spikes showed

### 2.1 Duplicates are the rule, not the exception [M]

In the 7 days to 2026-11-10 20:01 UTC the clone holds 120,709 usage rows but only 71,977 distinct message ids (and 71,971 distinct request ids): 40% of rows repeat a message. Copies per message id: 1 copy 34,816; 2 copies 29,050; 3 copies 6,337; the tail runs to 39 copies.

- Of 37,161 messages with more than one copy, 36,700 have every copy in ONE file and 461 span files; 187 span sessions. [M] So almost all repeats are "one assistant line per content block, sharing one message id" (a reviewer confirms the parser fixture says so [V: parser/sizing_test.go ~125]). The rest are resumed or forked sessions copying history [I].
- Token counters across copies are identical in all but one multi-copy message (output_tokens differed for 1 of 37,161; input and cache-read never) [M]. So the choice of "max" or "first copy" does not matter in practice: output tokens summed 35,579,001 with max-per-message against 35,578,365 with first-copy, against 78,143,680 for the raw sum (2.2 times too high).
- A raw sum overcounts about 1.68 times in rows and 2.2 times in output tokens, so the totals MUST be de-duplicated by message id.

### 2.2 The query-time version works but is too slow to poll [M]

A de-duplicated, per-model sum computed from the raw tables (records JOIN ext_assistant_usage JOIN fields for message.id and message.model, grouped by message id): last 5 h 0.31 s; last 24 h 0.78 s; last 7 d 4.5 s. Fine once; too much work to repeat every 5 to 10 seconds (4.5 s of CPU per call).

### 2.3 A de-duplicated per-message table makes it trivial [M]

Building a 14-day table of one row per message id (min time, max counters, model, a subagent flag, session) from the raw tables took 21.6 s once and holds 137,233 rows. Then: 7-day sum by model 0.038 s; 5-hour sum by model and session 0.002 s. The 7-day figures (messages and tokens per model) match the query-time version exactly (sonnet-5-5: 59,667 messages, 29,279,643 output tokens).

### 2.4 pa-monitor's stored cost figures cannot serve as the oracle [M]

Pricing the clone's de-duplicated tokens with pa-monitor's default price table (live config has no pricing override [M]):

| Window                         | pa-monitor stored         | Clone, per-file dedupe (pa-monitor rule)           | Clone, global dedupe by message id |
| ------------------------------ | ------------------------- | -------------------------------------------------- | ---------------------------------- |
| week 2026-11-02 to 11-09 (UTC) | $1,357.21                 | $4,106.76                                          | $3,689.59                          |
| block 01Z-06Z on 11-10         | $75.39                    | $169.59 (main files $75.27, subagent files $94.32) |                                    |
| block 06Z-11Z                  | $91.58                    | $92.52 (main $42.37, subagent $50.14)              |                                    |
| block 10Z-15Z                  | $34.70                    | $58.63 (main $28.50, subagent $30.13)              |                                    |
| block 15Z-20Z                  | $38.08                    | $87.63 (main $52.33, subagent $35.30)              |                                    |
| current week to 20:01 UTC      | $159.34 (as of 23:31 UTC) | $737.53                                            |                                    |

Two blocks agree with one inclusion rule or another (01Z-06Z matches main files within $0.12; 06Z-11Z matches all files within 1%), the other two match neither, and the weekly figures differ by about 3 times. The stored rows are snapshots from a daemon that was restarted (the daemon restarted 2026-11-10 12:01 EST; stored blocks overlap, for example 10Z-15Z and 11Z-16Z). HYPOTHESIS, NOT VERIFIED: pa-monitor's cost fold may be incomplete (files missing from its inventory, or timeouts during bootstrap), which would make its weekly and block costs undercounts. `ccusage` is not installed and I did not fetch it. See open item 2.

- Cross-file de-duplication matters on its own: per-file dedupe (pa-monitor's rule [V: transcript/snapshot.go ~107-108, 271-273]) gives $4,106.76 for last week and global dedupe by message id gives $3,689.59, so global dedupe is about 10.2% lower than the per-file rule on that week (417.17 / 4,106.76) (an inferred cause: resumed or forked sessions that copy history).

### 2.5 What pa-monitor computes today [V, reviewer]

- Record filter: type assistant, not an API-error line, model non-empty, at least one of four counters non-zero. Dedupe key `message.id` within one file, field-wise max, first line's timestamp (snapshot.go 243-310); pinned by `model_tokens_dedup_test.go`.
- Price: exact model string; unknown models use opus pricing (5/25/6.25/0.50 per million tokens); cost = input x P_in + output x P_out + cache-creation x P_cc + cache-read x P_cr; ALL cache creation is priced at the 5-minute rate (the 1-hour split is carried but unused); no long-context tier, no speed or service-tier multiplier [V: usage/types.go:192-198, config.go:289-310].
- Block (`ActiveBlock`, usage/native.go:35-95): sort by timestamp; a record starts a new block when it is 5 h or more after the current block start; start = first record truncated to the hour (UTC); active iff start <= now < start+5h for the last block; no inactivity-gap rule. Week (`CurrentWeekly`): Monday 00:00 UTC, [Mon, Mon+7d). `pricingWindow` = max(12 h, now - Monday anchor), used to pick files, not records (monitor.go:91-97).
- Block and week costs are recomputed every tick from all retained records (a flatten, sort and fold), and bootstrap reads every listed file in full from offset -1.
- Per-session fields: `session_tokens` is the OUTPUT tokens only, once per message id; `context_tokens` is the latest non-error assistant line's input + cache-creation + cache-read; session `cost_usd` is a proportional share of the block cost by output tokens (aggregate.go:73-75); burn rates are deltas of `session_tokens` kept in the daemon.

### 2.6 What the recorder already has [V, reviewer]

- Typed usage columns in `ext_assistant_usage` (input, output, cache creation, cache read, 1h and 5m split, service tier, inference geo, speed); `message.id`, `message.model`, `request_id`, `is_sidechain` are plain `fields` rows (no `store:` tag); `agent_id` is set only for subagent files [V: source/claudetranscript/transcript.go, store/ext.go:68-80].
- The query op's `aggregate` supports `sum` with a `distinct` key and grouping by model or session; ccpool already uses `sum usage.output_tokens distinct message.id` (ccpool/internal/facts/facts.go:285-300; query.go:71, serve_test.go:266). There is no time bucket in `group`, no count-distinct, and the `distinct` dedupe is per group and per aggregate, first record id wins [V: query.go ~560-720].
- Status-line capture is the source `claude-status` (type `status`), with fields five_hour_pct, five_hour_resets_at, seven_day_pct, seven_day_resets_at, hostname; every window is independently optional, and pa-monitor's `limits.Current` elects the account-global window (greatest bounded reset epoch) [V: source/claudestatus/status.go; limits/limits.go ~118].
- There is no existing `usage`/`account`/`window` op (19 capabilities listed at serve/daemon_integration_test.go:491). `ErrUsage` in wire.go:56 is the CLI misuse exit code, not a store error; a bad request should use `ErrBadRequest`.

## 3. Design

### 3.1 Recorder: derived table `usage_messages` (new)

One row per assistant message, maintained at ingest like the other derived tables in internal/store/derived.go (sessions, session_stats, file_facts):

| Column                                                                | Meaning                                                                                                                                                                                                                            |
| --------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| msg_id                                                                | PRIMARY KEY. `message.id`; for a line with no message id, the key `(file_id, off, item)` (encoded as a synthetic string key) so each such line counts once (the record id is reissued on every re-parse and cannot be the key)     |
| time_ns                                                               | the MIN time of any copy (pa-monitor keeps the first line's timestamp)                                                                                                                                                             |
| session_ref, is_subagent, model, owner_file_id, owner_off, owner_item | session; whether the file is a subagent file (agent_id present); `message.model`. All three are taken from the copy with the minimum time, so history copied into a resumed or forked session counts only toward the first session |
| input, output, cache_creation, cache_read, cache_1h, cache_5m         | per-field MAX across copies (equals first copy in practice [M])                                                                                                                                                                    |
| service_tier, speed                                                   | carried but not priced (matches today)                                                                                                                                                                                             |

Indexes: `(time_ns)`, `(session_ref, time_ns)`. Upsert semantics: insert, or on conflict take min time and per-field max, and replace the owner columns (session_ref, is_subagent, model, and the stored owner position owner_file_id, owner_off, owner_item, all written together) whenever the incoming copy has a smaller time; equal times break ties by lowest file_id, then off, then item (compared against the stored owner position, so the tiebreak is evaluable and a recompute after `clearGen` picks the same owner), so ingest order and re-parse (a parser version bump rebuilds records) are idempotent. Rows are excluded for error lines (`is_api_error_message`) to match pa-monitor's filter. Rows with all four counters zero (the `<synthetic>` lines) are not stored, matching required change 2.
Size [M]: about 137,000 rows per 14 days, so a few hundred thousand per month, small. Retention: keep all (open item 3 in section 6). Rows exist only for records that currently exist: a re-parse or truncation recomputes the affected message ids from the remaining records and deletes a row when no copy remains (operator ruling, Phillip, 2026-11-17).
Backfill: a one-off, sliced, niced job from `ext_assistant_usage` (the 14-day build took 21.6 s, so all-time is likely minutes [I: scale from 2,886,157 usage rows]); it MUST run in bounded slices so it never holds the recorder's write lock long (the 2026-11-04 investigation measured a 143 s first `PRAGMA optimize` as the cautionary case). It MUST also work under bulk-rebuild mode (`BulkBegin`).

### 3.2 Recorder: new op `usage.totals` (KindQuery)

Request:

```json
{
  "from": "RFC3339",
  "to": "RFC3339",
  "bucket": "hour",
  "by": ["model"],
  "include_subagents": true,
  "limit": 5000,
  "session_ids": []
}
```

(`[from, to)` in UTC; `bucket` is "hour" or "none"; `by` is `["model"]` or `["model","session"]`; `session_ids` optionally restricts to named sessions; `limit` caps the rows and `more` is true when it truncated.)

Response:

```json
{
  "as_of": "...",
  "degraded": false,
  "seq": 0,
  "rows": [
    {
      "hour": "2026-11-10T01:00:00Z",
      "model": "claude-sonnet-5-5",
      "session": "",
      "messages": 0,
      "input": 0,
      "output": 0,
      "cache_creation": 0,
      "cache_read": 0,
      "cache_1h": 0,
      "cache_5m": 0
    }
  ],
  "more": false
}
```

Why HOURLY buckets are enough: pa-monitor's block start is the first record truncated to the hour and a record belongs to a block when it is within 5 h of that start; because starts are hour-aligned, membership depends only on a record's hour, so hourly (UTC) totals reproduce `ActiveBlock` and `CurrentWeekly` for the same input horizon (block start remains an estimate; the weekly total is exact) (week boundaries are midnight UTC). A week is 168 hours x about 5 models, under 1,000 rows. [I, derived from native.go:35-95; the parity test in slice U3 proves it.]
Rate limits are not part of this op (operator ruling, Phillip, 2026-11-17): pa-monitor reads them through the existing `query` op, grouping the `claude-status` source on `five_hour_resets_at` and on `seven_day_resets_at` with `max` aggregates of the percentage and the reading time, so its account-global election (`limits.windowPeak`) still works unchanged. ADR 0029 (Decision: the reader holds the current window's peak, with a newest-record fallback when there is no `resets_at`) is kept: a record with no `resets_at` falls back to the newest record. A `limits.readings` op is added only if this form is measured slow (slice U6).
Implementation: a plain indexed read of `usage_messages` in one read transaction so `as_of` is consistent. No `idWalk` and no unary plus. A plan-guard test asserts `SEARCH ... USING INDEX` on the time index and no scan.
Plumbing (all required, per the reviewer): registration at serve/daemon.go ~575-592; request and response types; client method in client/lifecycle.go or client/ops.go; an entry in cancel_ops_test.go (`cancelBodies`); the sorted capability lists at serve/daemon_integration_test.go:491 and client/contract_integration_test.go:31; README; a capability check from `hello` in pa-monitor: against a recorder without `usage.totals`, pa-monitor uses the legacy fold only while the shadow flag is on; once the switch slice lands there is no fallback and cost is shown as unavailable.
Per-session facts: the output total per session comes from the same table and is exposed as the `sessions.facts` `output_tokens` field in the ccpool plan, not a second op. Context tokens and last model are NOT exposed by this plan; they stay as transcript folds in pa-monitor.

### 3.3 pa-monitor

- New `UsageSource` interface with a recorder implementation that calls `usage.totals` every period (default 10 s, floor 5 s) and on demand; the 5-hour block and the Monday week are derived from the hourly rows with the existing `ActiveBlock` and `CurrentWeekly` rules and the existing price table. Pricing, window rules and plan caps stay in pa-monitor.
- Keep the legacy transcript fold behind a flag during a shadow period that logs both results and any difference.
- After the switch, delete the transcript-line cost fold, the per-tick flatten and sort of all records, and the whole-file bootstrap read for cost. pa-monitor still folds transcripts for non-cost per-session facts (last error, awaiting input, first prompt, subagent count); those facts stay as transcript folds and are out of scope here.
- Replace the status-file reads (`files.inventory` plus raw reads of every status file) with the grouped `query` read of the `claude-status` source described above.
- Unreachable recorder: the Offline state is shown immediately (operator ruling, Phillip, 2026-11-17), as in the pa-monitor discovery plan; there is no stale hold.
- Reachable but degraded recorder: hold the last good block and week, marked stale in the TUI and as an OTel label, for `StaleAfter` (10 minutes), then report unavailable.

## 4. Slices (sizes S/M/L/XL; no time estimates)

| #   | Size | Needs    | Content and tests                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| --- | ---- | -------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| U0  | S    | none     | This spike (done); the investigation items are listed in section 6                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| U1  | M    | recorder | `usage_messages` table, upsert at ingest, bulk-mode support, sliced backfill. Tests: the `dupModelTranscript` fixture (expects Input 5, Output 147, CC 205, CR 2207, 1h 200 per model_tokens_dedup_test.go); partial-then-final streamed lines; a message copied across two files counts once; a message copied into a later resumed or forked session or a subagent file keeps session_ref, is_subagent and model of the earliest copy, and a re-parse or a different ingest order gives the same owner (including the equal-time tiebreak), and the stored owner position is the one compared; `clearGen` followed by a recompute from the remaining copies selects the same owner; a re-parse is idempotent; a truncation or removal recomputes the affected ids from the remaining records and deletes a row when no copy remains (no orphans); a message copied across files is counted once globally; null message id counted once per record; error lines excluded; a real-store test (not a mock) |
| U2  | M    | recorder | `usage.totals` op plus plumbing (section 3.2). Tests: hourly rows equal a brute-force sum over the same fixture; `more` and `limit`; `degraded` while backfilling; `by:[model,session]` attributes a copied message only to the earliest session; the plan-guard test; cancel-ops and capability tests; README                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| U3  | M    | U2       | pa-monitor `UsageSource` + recorder implementation + shadow mode. Parity tests: feed the same fixture lines through the old fold and the new path and assert equal per-model tokens; run the pinned-dollar tests (`TestActiveBlockCostParity` $9.8115, `TestActiveBlockWindowsByFiveHours`, `TestCurrentWeeklyCost` $40) over hour-bucketed input; capability fallback to the old path on an older recorder; the grouped `query` read of the `claude-status` source reproduces `limits.windowPeak` on the existing limits fixtures, including the no-`resets_at` fallback; the shadow diff expects the cross-file dedupe difference (about 10.2 percent on the clone's last week) and reports it separately from other differences; offline test: with the recorder unreachable the Offline state is shown at once and there is no stale hold; stale-hold test: after the recorder degrades, the last good value shows as stale for `StaleAfter` (10 minutes) and then as unavailable                     |
| U4  | S    | U3       | Switch the default; delete the cost fold and the status-file reads; metrics for call latency and staleness                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| U5  | S    | U3       | Record the global dedupe decision (operator ruling, Phillip, 2026-11-17) in the pinned tests and the docs                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| U6  | S    | U3       | CONDITIONAL: add a `limits.readings` op ONLY if the grouped `query` read of `claude-status` is measured slow on the live store (a measurement, not a default); otherwise this slice is not built                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          |

U1 and U2 are independent of the pa-monitor discovery and ccpool slices and can start first, except that the recorder `registryQuery` bound of the pa-monitor discovery plan lands before them (amendment 9). U1 should land (and run its backfill) before U2 is applied.

## 5. Verification

- Before the switch (shadow period): pa-monitor logs, for each of the last N finished blocks and the current week, old and new cost side by side with per-model token counts; any difference is explained or filed.
- After apply: `usage.totals` latency under 100 ms for a 7-day window by hour (the measured table sum is 0.038 s [M]); recorder log shows pa-monitor-daemon `raw` reads for cost files stop; no transcript re-read after a restart for cost.
- `ccusage` or another independent source is NOT available here; the oracle is the fixture tests plus the shadow diff, so any live difference must be reasoned about, not assumed.

## 6. Open items and decisions for the operator

1. DEDUPE SCOPE: decided (operator, Phillip, 2026-11-17): de-duplicate by message id across ALL files, about 10.2 percent lower than the per-file rule on last week's clone data (417.17 / 4,106.76), because the same message is copied into resumed or forked sessions. This is a deliberate change in reported cost.
2. UNVERIFIED undercount hypothesis (section 2.4): pa-monitor's stored weekly and block costs may be incomplete. Needs a separate investigation (bootstrap/inventory timeouts, files missing from its inventory, daemon restarts re-phasing blocks) before the shadow diff is interpreted.
3. RETENTION of `usage_messages`: keep all rows for records that still exist (simplest; needed if `session_tokens` is the whole-file output total); pruning past 90 days remains an unmade decision. Removed or truncated history is NOT kept (decided 2026-11-17).
4. PRICING SEMANTICS (unchanged by this plan, but now visible): all cache creation is priced at the 5-minute rate; the 1-hour split exists but is unused; service tier and speed are ignored. Change only as its own decision.
5. SUBAGENT usage is included in cost today (subagent files go through the same path [V]). Keep, and expose the flag so it can be split.
6. BLOCK PHASE: the 5-hour block start is an accepted estimate and weekly totals are exact (operator, Phillip, 2026-11-17; ADR 0021 ("pa-monitor plan model and rate-limit source") precedent). PER-MACHINE: the recorder sees only this machine's transcripts; the authoritative account-wide limit is the status-line reading (ADR 0021 ("pa-monitor plan model and rate-limit source")). The cost block is notional.
7. SESSION cost allocation (proportional by output tokens) stays in pa-monitor.
