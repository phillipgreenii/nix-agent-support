# Slack TTL read-through: verification finding (do not adopt now)

- **Date**: 2026-10-07
- **Bead**: `pg2-cw6b3.7` (item `D4` of the refresh cache bead breakdown)
- **Spec under verification**: `2026-10-05-pg-desk-attention-evaluator-and-connector-refresh-cache-design.md`,
  section 6.7 ("Jira and Slack", Slack bullet) and section 14, item `D4`. The spec made the Slack bead
  conditional on a verification task and said to close with the finding if the verification does not
  support adoption.
- **Prerequisite**: `pg2-cw6b3.2` (the umbrella membership and refresh cache policy, behavior rules
  `INV-CACHE-2` to `INV-CACHE-8`) is landed and was read as the baseline.
- **Verdict**: **do not adopt TTL read-through for `thread show` or `thread list` now.** No code change.
  The finding below records why, what evidence was read, and what would reverse the decision.
- **Method**: static reading of the code and docs named in each row. No live Slack call and no live LLM
  call was made (neither was authorized for this bead). Where a conclusion would need a live probe,
  that is stated.

Requirement words (MUST, SHOULD, MAY) are RFC 2119.

## What was verified

| ID  | Question                                                          | Answer                                                                                                                                                                                                                                                                                                                                                                                                                   | Evidence                                                                                                                                                                                                                        |
| --- | ----------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| V-1 | What does the Slack backend call for `list` and for `show`?       | Both are `claude -p` LLM calls against the Slack MCP. `list` makes one call per query expression, unions the items by id, and ALWAYS answers `Truncated: true` with `Cursor: nil`. `show` makes one call per id.                                                                                                                                                                                                         | `packages/pg-connector/cmd/pg-connector-thread-slack/internal/backend.go` lines 280 to 302 (`Show`) and 317 to 353 (`List`, `Truncated: true` at line 348)                                                                      |
| V-2 | Can `list` supply the membership the cache policy depends on?     | No. The refresher and removal confirmation need a COMPLETE ids-only membership answer, and a truncated answer reports no removals. Slack `list` can never be complete, so the refresher mode (`cache_refresh_after`) and `INV-CACHE-7` cannot run for `thread`.                                                                                                                                                          | `packages/pg-connector/docs/behavior/invariants.md` (`INV-CACHE-6`, `INV-CACHE-7`); `cmd/pg-connector/changes.go` lines 57 to 63 (thread reports no `removed`); the spec section 6.7                                            |
| V-3 | Does anyone READ `thread list` where a TTL could serve it?        | No. The only programmatic caller of the thread list op is `changes thread` (the pg-router `thread-me` source), and `changes` MUST bypass `read_ttl` (`INV-CACHE-6`; spec section 6.2). A TTL on `list` would also need a new query-keyed RESULT cache: the entity cache is keyed by entity id, and the policy has no list read-through for `pr` or `issue` either (their lists come from membership plus the refresher). | `packages/pg-connector/cmd/pg-connector/thread.go` lines 108 to 140 (`fanOutThreadList`, no cache wiring); `cache_policy.go` (`dispatchShow` is the only read-through entry point); spec section 6.2 line 736                   |
| V-4 | Who reads `thread show`, and how often?                           | Two event-driven callers, one call per event: pg-desk gather (`threadGatherAdapter`) and `run thread`. Neither polls on a period. No evidence of two reads of one id inside the 120 s `read_ttl`.                                                                                                                                                                                                                        | `packages/pg-desk/internal/gather/entity.go` lines 121 to 140; `packages/pg-desk/cmd/pg-desk/run.go` line 412                                                                                                                   |
| V-5 | Would a hit be SAFE for those readers?                            | Not without a code change in the readers. Gather deliberately passes `--fresh` for `pr` (and `issue`) because it runs after a change was detected and MUST NOT see a TTL hit. `thread show` has no `--fresh` flag, and the thread gather call passes none, so a read-through would silently serve pre-change data to the hydrate that follows a detected change.                                                         | `packages/pg-desk/internal/gather/gather.go` lines 330 to 334 (the `--fresh` rationale comment); `packages/pg-desk/internal/gather/entity.go` line 128; `cmd/pg-connector/thread.go` `newThreadShowCmd` (flag `--backend` only) |
| V-6 | Is there demonstrated demand (call volume or cost) for the cache? | No. `thread-me` has failed 48 times since 2026-09-19 (27 killed at the exec deadline, 12 schema errors, 4 unknown flag), and `desk-thread` dispatched zero times in the retained `events.jsonl`. A cache stores only successes, so it cannot help the dominant failure (the kill at the 30 second exec deadline), and there is no observed repeated successful read to save.                                             | `2026-10-05-fast-per-type-change-check-design.md` finding F-5 (line 129) and its type table (line 300); `packages/pg-connector/pkg/scriptout/limits.go` (`DefaultExecTimeout`)                                                  |
| V-7 | Is the cache machinery itself an obstacle?                        | No. `dispatchShow` names no backend and `cacheEnabled` fails open, so wiring `thread show` to it is a small change (replace `DispatchTargeted` with `dispatchShow` plus a `--fresh` flag). The obstacle is value and safety (V-3 to V-6), not effort.                                                                                                                                                                    | `cmd/pg-connector/cache_policy.go` lines 246 to 294; `cmd/pg-connector/cache.go` lines 485 to 503                                                                                                                               |
| V-8 | Is the LLM transport the long-term source for Slack threads?      | Probably not. `2026-10-07-deterministic-slack-thread-list-design.md` (bead `pg2-ynxy2`, conditional on a token) proposes a Web API transport and deletes the `claude -p` transport after a shadow run. Cache wiring built now for the LLM transport would be rebuilt or discarded.                                                                                                                                       | `2026-10-07-deterministic-slack-thread-list-design.md` sections 0 and 3 (Q4: "the `claude -p` transport is the default until the API transport passes a shadow comparison, then is deleted")                                    |
| V-9 | Is the id keyspace safe for an id-keyed cache?                    | Only partly. The thread `id` is a bare Slack timestamp, unique within a channel but NOT proven unique across channels (`[U-9]` in the Slack design, unverified). An id-keyed entry could in principle conflate two threads. This needs a live probe and is not a reason on its own, but it would be a precondition to adopting.                                                                                          | `packages/pg-connector/pkg/schema/thread.go` (`Thread.ID`); the Slack design register `[U-9]` and E-6                                                                                                                           |

## Decision

**Not adopted.** The bead is conditional, and the verification does not support adoption:

- For `thread list` there is no reader to serve (V-3), the result cannot be a complete membership
  (V-2), and a TTL would need a new kind of cache the policy does not define.
- For `thread show` the cache would be cheap to wire (V-7) but has no demonstrated hit (V-4, V-6), would
  be unsafe for its only change-driven reader unless that reader and the CLI first gain a `--fresh`
  path (V-5), and targets a transport that the approved follow-on design plans to delete (V-8).

The spec itself anticipated this outcome ("at most, TTL read-through", "otherwise close with the
finding"), so closing with this finding is within the approved spec and is not a deviation.

## What would reverse this decision

Adoption for `thread show` SHOULD be reopened when ALL of these hold:

1. A repeated reader exists: the Slack backend event log (`eventlog.RecordClaudeCall`) shows the same
   thread id fetched more than once inside `read_ttl` by non-change-driven callers.
2. `thread show` gains `--fresh`, and the pg-desk thread gather passes it, mirroring `pr` and `issue`.
3. The id keyspace question `[U-9]` is settled by a live probe, or the entity key includes the channel.
4. `INV-CACHE-1` to `INV-CACHE-8` are widened from "`pr` and `issue` only" to include `thread`, with the
   tests that mirror `pr` and `issue` (`cache_policy` suites).

Adoption for `thread list` MUST NOT be considered until the deterministic transport (`pg2-ynxy2`) can
report `truncated: false`, because only then does a membership index exist for the refresher to use.

## Not changed

No Go code, behavior document, or test was changed by this bead. The invariants in
`packages/pg-connector/docs/behavior/invariants.md` still scope the policy to `pr` and `issue`, and that
stays correct.
