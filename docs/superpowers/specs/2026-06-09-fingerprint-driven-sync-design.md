# Fingerprint-Driven Sync for pg-pr

**Status**: Draft
**Date**: 2026-06-09
**Deciders**: Phillip Green II

## Context

`pg-pr sync --daemon` currently does a **full pull** of every watched PR on a
fixed interval (`5m` in the `pg-pr-sync` launchd service). Each iteration calls
`Engine.Sync`, which enumerates PRs per repo via a GraphQL search, enriches each
PR (reviews, comments, CI runs), upserts a merge-request bead, runs the feedback
pipeline, and rebuilds the dashboard snapshot served at `/api/v1/dashboard`.

The pain: after a PR is updated, the dashboard is stale for up to the full
interval. Shrinking the interval is not viable — the full pull is expensive
(enrichment fan-out + bead writes for every PR, every tick).

Research into GitHub's APIs shaped this design:

- **`updated_at` is unreliable** as a change signal: a new commit push, a new
  review, a new review comment, and CI/status changes frequently do **not** bump
  the PR's `updated_at`.
- **Webhooks** give instant push updates at zero polling cost but require a
  publicly reachable receiver. `pg-pr` runs as a localhost-only launchd agent,
  so webhooks are out of scope (see Alternatives).
- The **Events API** runs 30s–6h stale and omits CI; the **Notifications API**
  only covers subscribed threads — neither is a complete, low-latency repo-wide
  signal.
- A **GraphQL "fingerprint" query** can fetch, for ~1 rate-limit point, a small
  per-PR signature (`updatedAt` + last commit `oid` + CI rollup state + review
  /comment/thread counts + `isDraft` + `state`) for all watched PRs in one
  request. The commit `oid` and CI rollup close exactly the gaps `updated_at`
  misses.

> **Design-review note (2026-06-09):** an earlier draft of this spec was reviewed
> against the source. It corrected several "how the code works today" errors —
> most importantly that **no `draft:false` filter exists** in the current search
> (drafts are already fetched; team drafts are hidden only at snapshot-build
> time), and that `SyncPR` does **not** build snapshot data. Those corrections
> are folded in below.

## Problem

Provide near-real-time (~1 minute) dashboard freshness without running the
expensive full pull every minute, on a localhost-only daemon.

## Goals

- Detect _which_ PRs changed cheaply (~1 rate-limit point per query, ~60s
  cadence) and run the expensive per-PR refresh **only** for those PRs.
- Reflect each PR's change on the dashboard within ~1 tick of it happening.
- Detect new PRs, closed/merged PRs, and draft transitions through the same
  per-tick flow — no separate reconciliation cadence.
- Preserve existing bead semantics, the feedback pipeline, and the
  `/api/v1/dashboard` snapshot shape.
- Update emitted metrics and the Grafana dashboards to match the new model.

## Non-goals

- Webhooks / push delivery (requires a public endpoint).
- Changing the one-shot `pg-pr sync` (non-daemon) command — it keeps using the
  full `Engine.Sync`.
- Persisting the dashboard snapshot across restarts (it stays in-memory).
- Persisting fingerprints on beads (change detection stays in-memory; see
  Alternatives).

## Architecture Overview

Replace the daemon's "full `Sync` every interval" loop with **two tiers**:

1. **Fingerprint poll (Tier 1, ~60s)** — cheap, paginated GraphQL searches that
   compute a per-PR fingerprint and diff it to decide _which_ PRs to refresh.
   This tier is a **pure detector: it mutates nothing** (no beads, no snapshot).
   It only enqueues `(repo, number)` keys.
2. **Refresh queues + workers (Tier 2)** — changed PRs are enqueued onto one of
   two dedup FIFO queues (mine / team). Workers drain them one at a time. **The
   worker is the sole authority that closes beads or removes PRs, and it decides
   the action from the PR's actual fetched `GetPR` state** — never from a hint
   carried on the queue.

Two primitives already exist: `Engine.SyncPR(repo, number)` (targeted single-PR
bead refresh that already handles close-on-merge + cascade) and the GraphQL
search (`Provider.EnrichedPRs`). This design adds: a slim **paginated**
fingerprint query, the queue/worker layer, a **`refreshPR`** path that produces a
`snapshot.PRInput` (because `SyncPR` does not), an incrementally-maintained
snapshot owned by one goroutine, an explicit concurrency model, and the
metric/dashboard changes.

## Detailed Design

### 1. Fingerprint queries

A new slim GraphQL query — a trimmed sibling of the existing `enrichedPRsQuery`
in `pkg/provider/vcs/github/enrich.go`. Same search input, but selecting **only**
fingerprint fields, with **no node bodies**:

```graphql
number
updatedAt
isDraft
state
author { login __typename }
repository { nameWithOwner }
commits(last: 1) { nodes { commit { oid statusCheckRollup { state } } } }
reviews { totalCount }
comments { totalCount }
reviewThreads { totalCount }
```

Exposed via a new optional provider capability:

```go
type FingerprintProvider interface {
    // No repo arg: the search may span repos; each node carries its own
    // repository.nameWithOwner. (Asymmetric with EnrichedPRsProvider, which
    // keeps a repo arg only for error-message context — keep it that way.)
    FingerprintPRs(ctx context.Context, searchQuery string) (FingerprintResult, error)
}

type FingerprintResult struct {
    PRs       []PRFingerprint
    Truncated bool // a page cap was hit and pagination did not complete
    RateCost  int  // rateLimit.cost from the envelope
    RateLeft  int  // rateLimit.remaining
}
```

`PRFingerprint` carries `{Repo, Number, Author, IsDraft, State, UpdatedAt,
HeadOID, StatusRollup, ReviewCount, CommentCount, ReviewThreadCount}`. Author
login MUST be normalized with the same `canonicalLogin` bot-suffix logic the
enrich parser uses (`enrich.go`), so self/team classification matches
bead-stored authors.

**Pagination (required — does not exist today):** `EnrichedPRs` sends one
`first: 50` query and ignores `pageInfo`. `FingerprintPRs` MUST loop on
`pageInfo.endCursor` until `hasNextPage` is false, accumulating nodes. This means
the fingerprint query (unlike the hardcoded constant `enrichedPRsQuery`) declares
an `$after: String` variable (and optionally `$first: Int`) and threads `after:`
into `search(...)`; `gh api graphql` passes it via an extra `-F after=<cursor>`
(the same `-F` plumbing the multi-author search already uses). If a hard page cap
is reached without completing, it returns `Truncated: true`. Truncation is
**not** an error but is a correctness hazard (see §3 / §7).

**Two query kinds, by group:**

- **Mine** — one cross-repo search: `is:pr is:open author:<self>` constrained
  with a `repo:<owner/name>` qualifier for each configured repo. Cross-repo node
  parsing already works (the parser keys off each node's
  `repository.nameWithOwner`).
- **Team** — one search **per repo**: `is:pr is:open repo:<owner/name>
author:<m1> author:<m2> …` using that repo's `team_members`. Team stays
  per-repo because `team_members` is configured per repo.

> Both queries include drafts. **There is no `draft:false` filter today** — the
> current search fetches drafts for everyone, and team drafts are excluded only
> at snapshot-build time (`builder.go`, `... && !p.PR.Draft`). So keeping drafts
> in the roster is the status quo, not a change. The actual behavior change is in
> §5 (stop running the feedback pipeline on team drafts).

Cost: 1 point for mine + 1 point per repo for team = `1 + R` points per tick
(more if a query paginates). At 60s cadence against 5,000/hour this is
negligible. For a single monorepo it is literally two queries per tick.

The per-PR **fingerprint** is a hash of: `UpdatedAt`, `HeadOID`, `StatusRollup`,
`ReviewCount`, `CommentCount`, `ReviewThreadCount`, `IsDraft`, `State`.
`HeadOID` + `StatusRollup` catch the silent commit-push and CI-change cases
`updated_at` misses.

### 2. Classification

Each roster entry is classified from its fingerprint:

- **group**: `isSelfAuthored(author)` (exact match on `self_login`,
  `sync.go`) → **mine**; otherwise → **team**.
- **active vs dormant**:
  - **mine** (any draft state) → **active**.
  - **team** and `!isDraft` → **active**.
  - **team** and `isDraft` → **dormant**.

When the same `(repo, number)` appears in both rosters (self also listed in a
repo's `team_members`), the **union keeps the mine entry** (mine classification
wins), matching `snapshot.Build`'s self-before-team check.

### 3. Per-tick diff and enqueue

Each tick the detector builds the current **roster** (union of mine + team
results) and diffs it against two sources:

- **`prev`** — the previous tick's roster fingerprints, in memory, keyed by
  `(repo, number)`. Empty on the first tick.
- **open beads** — the set of open merge-request beads, enumerated **per repo bd
  workspace** (each repo has its own `.beads/` workspace; one per-repo bd client
  per repo, as `Engine.Sync`'s close-stale pass does today). Each bead's group is
  derived from its stored `Author` via `isSelfAuthored` (legacy beads with empty
  author route to team).

The detector enqueues only `(repo, number)` keys (no job kind — see §4). It also
records the change _reason_ for metrics (`added` / `changed` / `dormant` /
`disappeared`).

Diff rules, per group (the detector already holds the open-bead set from the
disappeared check, so it can split added vs changed cheaply):

1. **Added / changed** — roster PR that is **active** and is either absent from
   `prev` or whose fingerprint differs → enqueue on its group's queue. Reason for
   metrics: **`added`** when **no open bead** exists for the key, **`changed`**
   otherwise. (On a cold start `prev` is empty, so every active PR is enqueued;
   those with an existing bead count as `changed`, producing a one-time restart
   burst — `added` stays reserved for genuinely new PRs.)
2. **Went dormant** — roster PR that is a **team draft** and is newly draft
   (absent from `prev`, or `prev` had it non-draft) → enqueue on the team queue
   (reason `dormant`). A team draft already draft in `prev` is **skipped** (no
   re-enqueue, no churn). A team PR observed **draft from first sight with no
   existing bead** is intentionally left alone — nothing is shown and no bead is
   created until it flips ready (the desired hidden state).
3. **Disappeared** — open bead whose `(repo, number)` is **not in the roster** →
   enqueue (reason `disappeared`). Because drafts are in the roster, a
   disappeared PR is closed/merged; the worker confirms via `GetPR`.

Enumerating open beads per repo workspace each tick is one `bd list` per repo per
60s (vs every 5m today) — tolerable for the expected repo count; throttle to
every N ticks if bd load becomes a concern.

Then set `prev := roster`.

**Completeness guard (§7) is per `(repo, group)`:** rule 3 is skipped for any
`(repo, group)` whose contributing fingerprint query **failed or returned
`Truncated`**. Mine is one cross-repo query — if it fails/truncates, skip
`disappeared` for mine in **all** repos. Each team query is per-repo — skip only
that repo's team beads. This prevents both transient errors **and** silent
pagination truncation from mass-closing beads.

### 4. Refresh queues and workers

Two **dedup FIFO** queues: `mineQueue` and `teamQueue`. A queue entry is just
`(repo, number)`. Enqueuing a key already present is a no-op that keeps the
existing entry; because the worker reads current state at processing time, the
deferred entry still acts on the latest state — and since the queue carries **no
job kind**, there is no stale-kind hazard (a PR that flips draft↔ready between
enqueue and processing is handled by the worker re-deriving the action).

Each queue has a **single worker goroutine** draining it serially. Two queues
give isolation (a team backlog never delays your own PRs) and headroom for
per-group divergence later. Routing: mine → `mineQueue`, team → `teamQueue`;
disappeared beads route by the bead's stored `Author`.

### 5. Worker behavior — derived from actual `GetPR` state

The worker is the **only** mutator of beads/snapshot. It calls `GetPR(repo,
number)` and dispatches on real state + config-derived classification:

| PR (actual state)                | Bead                         | Refresh work                                            | Snapshot            |
| -------------------------------- | ---------------------------- | ------------------------------------------------------- | ------------------- |
| **Mine**, open (any draft state) | upsert open                  | `refreshPR` (bead + feedback + self draft auto-promote) | upsert (shown)      |
| **Team**, open, non-draft        | upsert open                  | `refreshPR` (bead + feedback)                           | upsert (shown)      |
| **Team**, open, draft            | upsert open, `state="draft"` | **dormant-mark**: skip feedback/enrich/dep-tree         | **delete** (hidden) |
| **Any**, closed/merged           | close + cascade              | confirm via `GetPR`, then close                         | delete              |

**`refreshPR` (new — `SyncPR` does not build snapshot data):** `SyncPR` only
touches the bead + feedback + draft-promote and returns a `Summary`; it never
references the snapshot, and producing a `snapshot.PRInput` requires the
enrichment fan-out **and the bd dep-tree** (`DepTreeUp`, which drives the
`Beads`/`WaitingOnMe` columns) that today live only in `buildAndStoreSnapshot`.
So:

- Extract `buildAndStoreSnapshot`'s per-PR body into a reusable
  `buildPRInput(ctx, pr, …) (snapshot.PRInput, error)` (gathers reviews/comments
  /CI — per-PR REST, since the bulk `EnrichedPR` cache doesn't exist on this
  path — plus the bd dep tree via `DepTreeUp`). **Critical:** today the
  `human`-label overlay (`ApplyHumanLabels`) is gated on the per-tick `TickCache`
  being present (`buildAndStoreSnapshot`, `cache != nil`), which the per-PR path
  lacks. `buildPRInput` MUST therefore also fetch `HumanLabeledBeads` and apply
  `ApplyHumanLabels` itself on the cache-less path, or `WaitingOnMe` (which reads
  the `human` label) regresses to `false` on every daemon-refreshed PR. The
  legacy full `Sync` is refactored to call the same helper (passing its cache
  when it has one).
- `refreshPR(ctx, repo, number)` performs `SyncPR`'s bead/feedback/draft work,
  then calls `buildPRInput`, and **returns `(*snapshot.PRInput, *Summary,
error)`**. The worker forwards the `PRInput` (or a delete) to the snapshot
  owner (§6).

This means each active refresh now costs per-PR REST for reviews/comments/CI +
one `bd dep` shell-out (when uncached) — the real cost model, stated here so the
plan budgets for it.

**Dormant-mark (team draft):** a **separate** worker path, not a `SyncPR` mode
(`SyncPR` always runs `processFeedback` for open PRs). It:

- upserts the merge-request bead with `Draft=true` **and** `State="draft"` (both,
  to stay consistent with `stateForPR`; `encodeMetadata` only writes `draft` when
  true);
- **skips** feedback, enrichment, and dep-tree;
- sends a **delete** to the snapshot owner (hidden, as today);
- **leaves existing open feedback/processing-cycle children untouched** — they
  represent real unresolved feedback and resume when the PR returns to ready.
  Only _new_ feedback creation pauses. ("Pause new, leave open" — decided.)

**Counting authority:** `added` vs `changed` is owned by the detector (`added` =
no open bead for the key; `changed` = bead exists and is new-to-`prev` or differs)
and emitted as `pg_pr_fingerprint_changes_total`. The worker does **not**
re-derive added/changed (`SyncPR`'s `BeadsUpdated` counter can't distinguish
create vs update on its own), avoiding double-counting.

### 6. Incremental snapshot

The snapshot **is** the dashboard. Today `buildAndStoreSnapshot` rebuilds it from
the full observed set each `Sync`; there is no full `Sync` in the daemon anymore,
so the snapshot becomes **incrementally maintained, rebuilt per PR** by a single
owner goroutine:

- The owner holds an authoritative `map[prKey]snapshot.PRInput`.
- Workers send it an **upsert** (`PRInput`) or **delete** (`prKey`) after each PR.
- On each message the owner mutates the map, builds a **deterministically sorted
  slice** of the values (by repo, then number), and calls
  `snapshot.Build(BuilderInput{Self: cfg.SelfLogin, TeamMembers:
allTeamMembers(), Registry: …, PRs: sorted, …})`, then `Store.Set`.

The **sorted** slice is mandatory: `snapshot.Build` preserves input order and the
owner's map iterates randomly, so building from the raw map would reshuffle
dashboard rows on every per-PR rebuild (visible flicker). Sorting makes rebuilds
stable.

Rebuilding **per PR** (not debounced per batch) is deliberate — debouncing
starves the dashboard during a long drain. `snapshot.Build` itself is pure/cheap
and `Store.Set` is a cheap mutex swap; the cost is in producing each `PRInput`
(§5), which the worker does, not the owner. `snapshot.Build` classifies each PR
independently (no cross-PR global state), so a partial/incremental map yields a
valid dashboard.

### 7. Concurrency, config reload, and lifecycle

The current daemon is a single goroutine and `ReplaceCfg` mutates `e.deps.Cfg` in
place (explicitly "safe only from the daemon loop"). Adding a detector + two
workers + a snapshot owner makes that an unsynchronized data race. Model:

- **Config** lives behind an `atomic.Pointer[config.Config]`. The detector and
  workers **load** it atomically each use; SIGHUP **swaps** the pointer
  (replacing the in-place `e.deps.Cfg = cfg` mutation). This is **not** a
  one-line change: `e.deps.Cfg` is read directly in `Sync`, `repoConfig`,
  `isSelfAuthored`, `allTeamMembers`, `tryEnumerateEnriched`, and
  `buildAndStoreSnapshot` — **all** must route through an `e.cfg.Load()`
  accessor, **including the one-shot (non-daemon) `Engine.Sync` path**, which
  shares the same `Engine` and would otherwise read a stale field. Budget this as
  an engine-wide accessor migration, not a local edit. Removing a repo mid-flight
  is benign: an in-flight `refreshPR` for it gets a `repoConfig` error and the
  worker logs + drops it.
- **Channels/queues**: the detector is the only producer; each queue has one
  consumer; the snapshot owner has one consumer with workers as producers.
- **Shutdown** on `ctx` cancel: the detector stops enqueuing and closes the queue
  channels; workers finish the in-flight PR, drain-or-exit, and signal a
  `WaitGroup`; the snapshot owner exits after its input channel closes. The
  daemon returns only after the `WaitGroup` completes. No worker blocks
  permanently on a send because the owner drains until close.
- **Query-failure / truncation guard**: §3's completeness guard is enforced in
  the detector before enqueuing `disappeared` keys.

**Startup**: `prev` is empty, so every **active** roster PR is enqueued (populates
the in-memory snapshot), and in the same diff every open bead absent from the
first roster is closed (catches PRs closed while the daemon was down) — provided
its `(repo, group)` query completed and was not truncated. This is the orphan
sweep; there is **no separate reconciliation cadence**.

## Lifecycle Transition Reference

| Transition                                | Detected by                        | Action                                                                                    |
| ----------------------------------------- | ---------------------------------- | ----------------------------------------------------------------------------------------- |
| New PR (mine or team non-draft)           | in roster, not in `prev`, no bead  | `refreshPR` → create bead, show                                                           |
| Content change (commit/review/comment/CI) | fingerprint ≠ `prev`               | `refreshPR` → update bead, update snapshot                                                |
| Mine PR → draft                           | in roster (`isDraft`), fp change   | `refreshPR` (still active) → bead draft, still shown                                      |
| Team PR ready → draft                     | in roster (`isDraft`), fp change   | dormant-mark → bead `state=draft`, hide, pause new feedback (existing children left open) |
| Team PR draft → ready                     | fp change (`isDraft` false)        | `refreshPR` → full refresh, show                                                          |
| PR closed / merged                        | open bead, absent from roster      | confirm `GetPR`, close bead + cascade, hide                                               |
| Closed/merged while daemon down           | open bead absent from first roster | close bead (orphan sweep)                                                                 |
| Transient query error or truncation       | query error / `Truncated`          | skip `disappeared` for that `(repo, group)` this tick                                     |

## Metrics Changes

Defined in `internal/telemetry/metrics.go`; emitted as `pg_pr_*`.

**Retire:**

- `pg_pr_last_sync_success_timestamp_seconds{repo}` — no whole-repo sync in the
  daemon. Its one consumer (`pg-pr-ops.json` headline stat) MUST be re-pointed in
  the same change (it degrades to "No data" otherwise).

**Add:**

- `pg_pr_fingerprint_poll_duration_seconds{group}` (histogram)
- `pg_pr_fingerprint_poll_errors_total{group}` (counter)
- `pg_pr_fingerprint_poll_truncated_total{group}` (counter — surfaces silent
  truncation)
- `pg_pr_fingerprint_poll_success_timestamp_seconds{group}` (gauge — new
  freshness signal)
- `pg_pr_fingerprint_changes_total{group,kind}` (counter; `kind ∈
{added,changed,dormant,disappeared}`)
- `pg_pr_refresh_queue_depth{group}` (gauge)
- `pg_pr_refresh_enqueued_total{group}` (counter)
- `pg_pr_graphql_cost{group}` (gauge), `pg_pr_graphql_rate_remaining` (gauge)

**Keep:**

- `pg_pr_sync_pr_duration_seconds` — `refreshPR` is the unit of work. **Keep the
  `repo` label and add `group`** (labels become `{repo,group}`); this is a
  **breaking series change** (resets the existing `{repo}`-only series),
  acceptable here but flagged.
- `pg_pr_feedback_created_total`, `pg_pr_ci_only_attempts`,
  `pg_pr_snapshot_present`.

## Grafana Dashboard Changes

Dashboards: `phillipgreenii-nix-support-apps/darwin/modules/observability/dashboards/`.

- **`pg-pr-ops.json`** (Ops, PromQL): replace the freshness stat
  `time() - max(pg_pr_last_sync_success_timestamp_seconds)` with
  `time() - max by (group)(pg_pr_fingerprint_poll_success_timestamp_seconds)`
  (**same change** as retiring the metric). Add panels: refresh queue depth,
  fingerprint poll rate + p50/p95 latency, change rate by kind, truncation rate,
  GraphQL rate remaining. Keep the per-PR refresh-duration histogram, sync
  errors, and feedback panels.
- **`pg-pr.json`** (My Work, Infinity datasource): **unchanged** — it reads only
  `mine`/`team` selectors + `agent_approved` and does not consume
  `sync_interval_seconds`/`generated_at`, so the preserved snapshot shape and the
  interval-semantics change don't affect it.

## Config and Service Changes

- `daemon_interval` (config) and `--interval` (flag) now mean the **fingerprint
  poll cadence**, default **60s**. Update **all three** current defaults so the
  daemon doesn't silently stay at 10m:
  - `DefaultDaemonInterval` (`internal/sync/daemon.go`) 10m → 60s,
  - the flag default (`cmd/pg-pr/sync.go`) 10m → 60s,
  - the launchd service `interval` (`pg-pr-sync/default.nix`) 5m → 1m.
- SIGHUP reload preserved via the atomic config pointer (§7).
- One-shot `pg-pr sync` keeps `Engine.Sync`.

## Edge Cases

- **Pagination/truncation** — handled in §1/§3/§7; truncation is gated out of the
  disappeared-check and counted via `pg_pr_fingerprint_poll_truncated_total`.
- **Per-repo bd workspaces** — open-bead enumeration is per workspace (§3).
- **Authorship change** — bead's stored `Author` may route a disappeared PR to
  the "wrong" queue; harmless (worker re-derives from state).
- **Self in a repo's team set** — deduped by key, mine classification wins (§2).
- **Team PR draft from first sight (no bead)** — left alone (hidden, no bead
  created) until it flips ready; see §3 rule 2.
- **PR opened and closed within one tick** — never observed; acceptable.
- **Fingerprint algorithm change across versions** — `prev` is in-memory, so a
  restart re-enqueues all active PRs once (needed for snapshot population
  anyway); self-healing.

## Testing Strategy

- **Fingerprint query/parse**: recorded-fixture test for the parser (mirror
  `enrich_test.go`); assert cross-repo node mapping, bot-login canonicalization,
  `rateLimit` extraction, and **multi-page pagination + truncation flag**.
- **Diff/enqueue**: table-driven over `(roster, prev, open-beads, query-status)`
  → expected enqueues + reasons, covering every Lifecycle row, the per-`(repo,
group)` completeness guard (error **and** truncation), dedup, and the
  team-draft no-re-enqueue case.
- **Worker state machine**: team-draft → dormant-mark (bead `state=draft`, not
  closed, children untouched, snapshot delete); closed/merged → close + cascade;
  active → `refreshPR` + snapshot upsert. Reuse injected `BeadClient`/VCS fakes.
- **`buildPRInput` extraction**: legacy `Sync` and `refreshPR` produce equivalent
  `PRInput` for the same PR (guards the refactor).
- **Snapshot owner**: concurrent upsert/delete from two workers → deterministic,
  sorted `Set` sequence; per-PR rebuild (not debounced).
- **Concurrency**: SIGHUP config swap under load (race detector); clean shutdown
  on ctx cancel with in-flight jobs; lock still single-instance.

## Files Affected

- `pkg/provider/vcs/iface.go` — `FingerprintProvider`, `PRFingerprint`,
  `FingerprintResult`.
- `pkg/provider/vcs/github/` (new `fingerprint.go`) — slim **paginated** query,
  `FingerprintPRs`, parser (+ reuse `canonicalLogin`).
- `internal/sync/` — detector + diff/enqueue, two dedup queues, two workers, the
  snapshot owner, `atomic.Pointer` config, daemon-loop rewrite (`daemon.go`),
  `refreshPR`, extracted `buildPRInput` (refactor `buildAndStoreSnapshot` + the
  legacy `Sync` to share it), dormant-mark path.
- `internal/snapshot/` — incremental upsert/delete owner around `Store` (sorted
  rebuild).
- `internal/telemetry/metrics.go` — metric changes above.
- `internal/sync/daemon.go` (`DefaultDaemonInterval`) + `cmd/pg-pr/sync.go` (flag
  default) — interval default 10m → 60s. (`config.DaemonInterval` is parsed but
  only echoed by `config show`; it is not wired into the daemon, so no default
  lives there.)
- `internal/sync/` + helpers — atomic config accessor migration (§7).
- `phillipg-nix-ziprecruiter/darwin/services/pg-pr-sync/default.nix` — `interval`
  5m → 1m.
- `phillipgreenii-nix-support-apps/.../dashboards/pg-pr-ops.json` — panels.
- New ADR in `phillipgreenii-nix-agent-support/docs/adr/`.

## Implementation Ordering (single epic, ordered)

1. Provider: `FingerprintPRs` + pagination + parser + tests.
2. `buildPRInput` extraction (refactor, behavior-preserving) + tests.
3. `refreshPR` + dormant-mark worker logic + tests.
4. Detector + diff/enqueue + queues + completeness guard + tests.
5. Snapshot owner (sorted, per-PR) + concurrency/shutdown + atomic config.
6. Daemon-loop rewrite wiring 1–5 together.
7. Metrics + `pg-pr-ops.json` (same change) + interval defaults + service.
8. ADR.

Steps 2–3 are the load-bearing seam (the snapshot/`refreshPR` shape); everything
after depends on it.

## Alternatives Considered

- **Webhooks** — instant, zero polling cost, complete coverage. Rejected:
  requires a public receiver; `pg-pr` is localhost-only.
- **`updated_at` polling** — misses commits/reviews/review-comments/CI. The
  fingerprint includes commit `oid` + CI rollup specifically to cover these.
- **Events / Notifications APIs** — stale (30s–6h) + no CI; subscribed-threads
  only. Not a complete low-latency repo-wide signal.
- **Conditional REST (ETag → 304)** — free on 304, but a sub-resource change
  doesn't reliably bump the parent PR's ETag → many per-sub-resource polls. The
  single GraphQL fingerprint is simpler and complete.
- **Fingerprints stored on beads** — fully restart-stateless detection. Rejected
  for now: the in-memory snapshot must repopulate on startup regardless (forcing
  a first-tick full enqueue), so persisting fingerprints adds a bead-schema
  change for little gain.

## Related Decisions

- See also: phillipgreenii-nix-agent-support docs/adr/0009-pg-pr-bead-schema.md
- See also: phillipgreenii-nix-agent-support docs/superpowers/specs/2026-05-26-pg-pr-dashboard-design.md
- See also: phillipgreenii-nix-agent-support docs/superpowers/specs/2026-05-27-pg-pr-team-pr-readonly-design.md
