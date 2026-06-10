# pg-pr Per-PR Refresh Throughput — Design

**Status:** Draft
**Date:** 2026-06-10
**Topic:** Fix the daemon's per-PR refresh throughput collapse so `/api/v1/dashboard` populates.

---

## Problem

The fingerprint-driven daemon (`pg-pr sync --daemon`) detects changed PRs cheaply
(one GraphQL fingerprint poll per group), enqueues them on two dedup FIFO queues
(mine / team), and two worker goroutines drain the queues by calling
`Engine.refreshPR` per PR. **The dashboard never populates** because the per-PR
refresh is too slow: the queue drains at roughly **2 PRs / 14 min**, so a cold-start
backlog of ~24 PRs never clears.

### Evidence (live)

- `pg_pr_refresh_enqueued_total` mine=9 / team=30 vs `pg_pr_sync_pr_duration_seconds_count` = 2.
- `pg_pr_refresh_queue_depth` mine=8 / team=12, not draining.
- `generated_at` frozen; daemon children observed running live `bd` calls.
- `bd --version` alone ≈ 1.6s baseline; worse under the concurrent dolt load from the
  gascity background agents, which hammer the same bd/dolt backend.

### Root cause

`refreshPR` (`internal/sync/refresh.go`) handles the active-PR case via
`applyFetchedPR` + `buildPRInput`, and **both are called with `nil` cache and
`nil` enriched** (`sync.go:906`, `sync.go:77`). That `nil` forces every helper down
its live-call fallback, so each PR pays a fan-out of ~8 sequential `bd` calls plus
several `gh` calls. Two categories of waste dominate:

1. **Workspace-wide work re-run per PR.** Calls that return the same answer for
   every PR are re-issued once per PR:
   - `buildPRInput` → `HumanLabeledBeads(ctx)` (`sync.go:731`) — a whole-workspace
     `bd query "label=human"` scan, identical result for every PR.
   - `applyFetchedPR` → `processReplyDrafts` → `ListFeedbackPendingReply`
     (`sync.go:1510`) — a whole-workspace pending-reply scan, run **once per PR**
     even though it takes no PR argument and processes the entire repo's queue each
     time.

2. **Redundant work inside one refresh.**
   - `refreshPR` discards the bead id returned by `EnsureMergeRequest`
     (`refresh.go:74` uses `_`), then `buildPRInput` re-fetches the same bead via
     `FindByRepoAndNumber` (`sync.go:700`).
   - `ListComments` is fetched twice for the same PR — once in `processFeedback`
     (`sync.go:1234`) and again in `buildPRInput` (`sync.go:668`).

Each `bd` invocation is the unit that gets slow under contention, so the lever is
**fewer `bd` round-trips per PR**, plus moving the genuinely workspace-wide scans
off the per-PR path entirely.

## Non-goals / settled decisions

These were considered and explicitly rejected or deferred:

- **No batched / multi-PR refresh.** We are _not_ reintroducing the bulk
  `EnrichedPRs` GraphQL fetch or the `TickCache` bulk bd-load that `Engine.Sync`
  uses. Per-PR is the correct granularity: most PRs are not changing at any given
  tick, so the changed set is normally tiny; small, focused per-PR queries are the
  right shape, and we should pull only what a specific PR needs.
- **Keep the two-tier model unchanged in structure:** fingerprint detector → dedup
  queues (mine / team) → two workers → snapshot owner.
- **`openBeadsForGroup` stays synchronous** in the detector for now
  (`detector.go:121`). A slow bd can still stall the detector via that call; making
  the open-bead set async is a possible future change but is out of scope here.
- **GitHub labels are not involved.** `api.PR` carries no labels field; `watch_labels`
  (`config.go:58`) is an unhonored stub (`sync.go:922`). The only label in play is the
  **bd** `human` label.

## Design

Keep the detector → queue → worker → owner structure. Fix throughput by (A) lifting
the genuinely workspace-wide work off the per-PR path onto an asynchronous
maintenance goroutine, and (B) making each per-PR refresh fetch only that PR's own
data, once, and reuse it.

### A. Asynchronous maintenance goroutine (workspace-wide work)

A single long-lived **maintenance goroutine**, started in `Engine.Daemon` alongside
the workers and snapshot owner, with its own ticker at the daemon interval. Each
cycle it:

1. **Refreshes the `human` label set per repo** (`bd query "label=human"` via the
   per-repo client) and `Store`s the result into an
   `atomic.Pointer[map[string]map[string]bool]` (repo → set of bead IDs) on the
   `Engine`.
2. **Drains reply drafts per repo** (`processReplyDrafts`), moved out of
   `applyFetchedPR`.

Rationale and properties:

- **Off the critical path.** A slow or hung bd only delays label freshness and
  reply draining — both non-critical and self-correcting. It never stalls the
  fingerprint poll, the workers, or SIGHUP/shutdown.
- **Single-flight by construction.** One sequential goroutine, so a slow pull cannot
  pile up overlapping bd calls.
- **One extra bd accessor, not two.** Labels and reply-drain share one goroutine
  deliberately — the core problem is bd contention, so we do not multiply concurrent
  bd hitters. A slow label pull delaying the reply drain is harmless (both off the
  critical path).
- **Lifecycle.** Started before the loop; stops on `ctx.Done()`. It pulls once
  immediately on start so the label set is populated as soon as possible, then ticks.
- **Reply-drain in daemon mode** has no aggregate `Summary` to return; it logs
  errors/warnings via the daemon logger (and existing reply telemetry) instead of
  accumulating into a returned summary.

### Workers read the label set

`buildPRInput`'s label-overlay (`sync.go:728`) keeps its current shape, but the
cache-less branch reads the engine's atomic label set instead of issuing a per-PR
`HumanLabeledBeads` call:

- `cache != nil` (full-sync path) → unchanged: `ApplyHumanLabels(deps, cache.HumanLabeled)`.
- `cache == nil` (daemon per-PR path) → `ApplyHumanLabels(deps, e.humanLabelsFor(pr.Repo))`,
  reading the atomic pointer.

`ApplyHumanLabels` is a no-op on an empty/nil set (`deptree.go:110`), so an
unpopulated set (before the first maintenance pull) is safe — `WaitingOnMe` reads
false and self-corrects on the first pull. Staleness is cosmetic only: the `human`
label feeds exactly one read-only dashboard field, `WaitingOnMe`
(`builder.go:65` via `AllNonClosedHumanLabeled`), and no write path reads it. (Note:
`human` labels live on bd beads, not on the GitHub PR, so they are not in the
fingerprint — even today a label change alone does not re-enqueue a PR; the cache
does not regress this.)

### B. Per-PR refresh fetches only that PR's own data, once

In `refreshPR`'s active branch:

1. **Fetch this PR's enrichment once** — reviews, comments, and CI runs via focused
   per-PR `gh` calls (`ListReviews`, `ListComments`, `ListRunsByBranch`/`ListRuns`) —
   and bundle them into a per-PR `vcs.EnrichedPR{PR, Reviews, Comments, CIRuns}`.
2. **Pass that `enriched` bundle to both** `applyFetchedPR` (which forwards it to
   `processFeedback`) **and** `buildPRInput`. Both already accept a non-nil
   `enriched` and use it instead of issuing their own fetches — this removes the
   duplicated `ListComments` and the separate per-PR fetches inside the helpers.
3. **Thread the bead id** returned by `EnsureMergeRequest` into `buildPRInput` so it
   uses the known id directly and skips `FindByRepoAndNumber`. `buildPRInput` gains
   an optional `knownMRID string` parameter: when non-empty it is used as `mrID`
   (skipping both the cache lookup and the live `FindByRepoAndNumber`); the full-sync
   caller passes `""` and behaves exactly as today.

`cache` stays `nil` on the daemon path. The per-PR `enriched` bundle plus the
per-tick label set together cover what `cache` used to provide.

### Reply-draft processing moves out of `applyFetchedPR`

`applyFetchedPR` no longer calls `processReplyDrafts`. Its two callers adapt:

- **Daemon** (`refreshPR`): does not post replies — the maintenance goroutine drains
  them once per cycle.
- **One-shot CLI** (`SyncPR`): calls `processReplyDrafts` explicitly after
  `applyFetchedPR`, preserving today's one-shot behavior.

This is also a latency improvement: queued replies now drain every maintenance cycle
rather than only when their PR's fingerprint happens to change.

## Net effect

Per-PR `bd` round-trips for an active PR with no new feedback drop from ~8 to ~4:

|                            | before (per-PR, nil cache) | after                            |
| -------------------------- | -------------------------- | -------------------------------- |
| `EnsureMergeRequest`       | 1                          | 1                                |
| `FindOpenProcessingCycle`  | 1                          | 1                                |
| `ListFeedback`             | 1                          | 1                                |
| `DepTreeUp`                | 1                          | 1                                |
| `FindByRepoAndNumber`      | 1                          | **0** (threaded id)              |
| `HumanLabeledBeads`        | 1                          | **0** (async, per repo per tick) |
| `ListFeedbackPendingReply` | 1 (per PR)                 | **0** (async, per repo per tick) |

The two workspace-wide scans leave the per-PR path entirely (moved to one-per-repo
per maintenance cycle), and the redundant `FindByRepoAndNumber` and the duplicate
`ListComments` are eliminated.

## Affected code

- `internal/sync/daemon.go` — start the maintenance goroutine in `Daemon`; wait for
  it on shutdown.
- `internal/sync/refresh.go` — `refreshPR` builds the per-PR `enriched` bundle,
  threads the bead id, passes both into `applyFetchedPR` and `buildPRInput`.
- `internal/sync/sync.go` — `applyFetchedPR` accepts/forwards `enriched` and no
  longer calls `processReplyDrafts`; `buildPRInput` gains the optional `knownMRID`
  and reads the engine's atomic label set on the cache-less branch; `SyncPR` calls
  `processReplyDrafts` explicitly. New maintenance helper(s) and the
  `atomic.Pointer` label field on `Engine`.

## Testing

- **Unit (fast, mocked providers + in-memory bd):**
  - per-tick label cache: maintenance pull populates the atomic pointer;
    `buildPRInput` overlays from it; empty set is a safe no-op.
  - threaded id: `refreshPR` active path issues no `FindByRepoAndNumber` when the id
    is known.
  - reuse-enrichment: `ListComments` is fetched once per refresh; `processFeedback`
    and `buildPRInput` consume the same bundle.
  - reply-drain relocation: `SyncPR` still posts replies; the daemon path does not
    (the maintenance goroutine does).
- **Integration (slow, real bd + dolt):** run the full `internal/sync` suite once
  before merge; iterate with targeted `-run` tests + `go build ./...` + `go vet ./...`.
  Trust `go build`/`go test` over editor/LSP diagnostics (often stale after edits).
- **Live verification** on `org.nixos.pg-pr-sync` (`127.0.0.1:9818`): queue depth
  drains, `pg_pr_sync_pr_duration_seconds_count` climbs, `mine`/`team` populate, and
  `generated_at` advances.

## Out of scope (tracked separately)

- Rebuild to pick up the `1m` interval (commit `057a803`); verify `sync_interval_seconds=60`.
- `pg_pr_snapshot_present` always-0 one-liner in the snapshot owner.
- `generated_at` advancing on _idle_ ticks (no changes) — a pre-existing cosmetic; a
  small owner heartbeat could fix it. Left out unless requested.
- Making the detector's `openBeadsForGroup` open-bead set async.
