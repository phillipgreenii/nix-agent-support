# pg-pr feedback #5 — beadsbridge PR-lifecycle event ownership (Decision A)

**Date**: 2026-06-24
**Status**: Draft (revised after adversarial review, 2026-06-24)
**Bead**: `pg2-4c5i.9` (subsumes `pg2-4c5i.14` / follow-up #6)
**Epic**: `pg2-4c5i` — Phase 2 roadmap: `docs/superpowers/plans/2026-06-23-pg-pr-feedback-phase2-roadmap.md`
**Foundation**: `docs/superpowers/specs/2026-06-23-pg-pr-feedback-datastore-design.md` (storage move, "Decision A")

## Context

The storage move shipped an in-process event dispatcher + transactional outbox
and `internal/beadsbridge`, the handler that projects pg-pr's beads. "Decision A"
intended the beads handler to project the **PR (merge-request) bead** from `pr.*`
lifecycle events, with the store `pull_request` row as the authoritative PR state.

That is only half-realized today:

- Only `feedback.created` is ever enqueued (`internal/sync/ingest.go:153,260,299`).
  Its bridge handler (`ensureProcessFeedbackBead`) creates the process-feedback
  cycle bead and **requires the PR bead to already exist**
  (`FindByRepoAndNumber` → errors "no merge-request bead" if absent,
  `bridge.go:88-89`).
- The PR bead is still created/updated/closed by the **inline** path:
  `sync.go:417` (`EnsureMergeRequest`), `sync.go:1000` (`applyFetchedPR`, the
  daemon's create path), `refresh.go:68` (hidden team draft); closed inline at
  `sync.go:513/523`, `sync.go:931/937`, `refresh.go:47/48`.
- The bridge's `pr.opened/updated → EnsureMergeRequest` and
  `pr.closed/merged → cascadeClose` branches are **dead in production**.
  `bridge_test.go` covers only `EventPROpened` + `EventFeedbackCreated`; there is
  **no test** for `EventPRUpdated`, `EventPRClosed`, `EventPRMerged`, or
  `cascadeClose`.
- Evidence the dead handlers were never finished: `PRPayload` carries only
  `{Repo, Number, Title, Ownership, Merged}` and the handler passes only
  `{Repo, PRNumber}` to `EnsureMergeRequest` (`bridge.go:60-62`) — it would create
  a **degraded** bead vs. the inline path, which sets `State, Branch, Base, Author,
URL, Draft, LastSyncedAt`.

The PR bead is the parent of the process-feedback beads, and Phase 2's headline
features (#1 diff-review, #3 teammate-attention) will project **new** beads.
Whichever ownership model we pick is the pattern those features follow — this is
the architectural root of Phase 2.

## Decision

**Realize Decision A fully via targeted event emission (approach "A2").**

The store `pull_request` row is the authoritative PR state. `sync`/`refresh`
**emit** `pr.opened/updated/closed/merged`; the repo-routed `beadsbridge` handler
becomes the **single** code path that creates, updates, and closes the PR bead.
The inline bead-writes are removed; the dead `pr.*` handlers become live.

Rejected: **A1** (pure store-projection reconciler + new `pr_bead_id` column +
migration — more than this step needs; A2 can evolve into it later); **A3**
(half measure — create stays inline — does not realize Decision A).

**Fold follow-up #6 (`Summary.RepliesPosted`)** in: same class of fix (a Summary
count the code never populates), same surface (`reconcileReplies`/`Summary`).
Bead `pg2-4c5i.14` is subsumed.

## Why A2 is tractable (verified findings)

- **The store row already exists.** `ingestFeedbackToStore` (`ingest.go:56`) calls
  `store.UpsertPR` (today gated behind `prBeadID != ""`, `enriched != nil`, and
  `Deps.Store != nil`). A2 makes it unconditional and earlier.
- **The bridge is already repo-routed.** `newBeadsBridgeHandler`
  (`cmd/pg-pr/sync.go:80-102`) reads `payload.repo` → `repoPaths` →
  `beads.NewClientForRepo(path)`. Emitted `pr.*` events land in the right `bd`
  workspace with **no new routing**.
- **The `prBeadID` coupling is light.** `processFeedback` uses `prBeadID` only as a
  boolean gate (`sync.go:1348`); ingestion is keyed by repo/number. `maybePromoteDraft`'s
  only synchronous bead use is `UpdateMergeRequest(prBeadID, {State:"open"})`
  (`sync.go:1418`).
- **`EnsureMergeRequest` and `cascadeClose` are idempotent.** `EnsureMergeRequest`
  finds existing by repo+PR (including closed), returns `(id, alreadyClosed=true)`
  for a closed bead **without reopening it**, else updates, else creates.
  `cascadeClose` re-closing a closed bead is a no-op. Safe under at-least-once
  delivery.
- **No migration needed.** The `pull_request` table already exists
  (`migrate.go`, schemaVersion 1); the only store addition is a `ListOpenPRs`
  **query**.
- **`api.PR` already carries every payload field** (`Title, State, Branch, Base,
Author, URL, Draft, Merged, HeadSHA`) **except `Ownership`**, which sync derives
  (`mineSet`, `sync.go:361`; `isSelfAuthored`, `refresh.go:56`; `ingest.go:48-51`).

## Execution model (corrected — this drove the rework)

There are **two** paths, and they are NOT "concurrent goroutines within one Sync
tick flushed once":

1. **One-shot CLI `pg-pr sync`** (`Engine.Sync`): PRs processed **serially** (the
   per-PR block at `sync.go:384` is an immediately-invoked `func(){…}()`, not a
   goroutine); the outbox is flushed **once** at `sync.go:548`; **no maintenance
   ticker runs**. This is the **only** path that produces a user-facing `Summary`
   (printed at `cmd/pg-pr/sync.go:200`).
2. **Daemon** (`StartDaemon`): **two concurrent workers** (`mineQ`/`teamQ`,
   `daemon.go:208-209`) each run `refreshPR` (per-PR flush at `refresh.go:81`),
   **plus** a `runMaintenance` goroutine that also flushes (`daemon.go:348` →
   `maintenanceCycle`). The daemon's PR-bead writes live in `refresh.go` +
   `applyFetchedPR` (`sync.go:1000`), not in `Sync`. Each `refreshPR` builds a
   **throwaway** `Summary` (`refresh.go:37`) that is discarded — the daemon emits
   **no per-tick Summary**.

Consequence: multiple `RunOutbox` drainers run **concurrently** in the daemon (2
workers + maintenance), all draining the **shared** outbox. The design's
correctness and counting must hold under that, not under a single-flush model.

## Architecture / data flow

Per observed PR (one goroutine handles a given PR end-to-end; mine/team queues are
disjoint, so no PR is processed by two workers):

```
observed PR ─▶ store.UpsertPR  +  enqueue pr.opened|pr.updated    ┐ SAME goroutine,
              (committed BEFORE any feedback for this PR)          │ committed in
            ─▶ processFeedback → ingest → enqueue feedback.created │ this order
            ─▶ maybePromoteDraft (mine) → SetDraft(false)          │
                                        → enqueue pr.updated{State:open}
close phase ─▶ store.ListOpenPRs(healthy repo) − observed
            ─▶ enqueue pr.closed|pr.merged
flush       ─▶ RunOutbox (FIFO by id) → beadsbridge:
                 pr.opened/updated → EnsureMergeRequest (full fields)
                 feedback.created  → ensureProcessFeedbackBead (parent exists ✓)
                 pr.closed/merged  → cascadeClose
```

`pr.opened` vs `pr.updated`: chosen from `repoPreExisting` (`sync.go:432`) —
not-pre-existing → opened, else updated.

### Ordering invariant (load-bearing — fixes review blockers #1/#2)

Removing the inline `EnsureMergeRequest` removes the only thing that currently
guarantees the PR bead exists before `feedback.created` is handled. Under A2 that
guarantee becomes:

> For a given PR, `pr.opened`/`pr.updated` **must be enqueued and committed before
> any of that PR's `feedback.created` events are enqueued.**

This holds naturally because a single goroutine processes a PR sequentially
(`UpsertPR` + `pr.opened` enqueue, _then_ `processFeedback`). Therefore any
`feedback.created` for that PR has a strictly higher outbox id than its
`pr.opened`, and `RunOutbox` (`ORDER BY id`, `outbox.go:67`) always projects the
PR bead first — **even** if a concurrent daemon `RunOutbox` drains mid-cycle (it
either sees both in id order, or sees only `pr.opened`, never only
`feedback.created`). Cross-PR interleaving is irrelevant (each PR is
self-consistent). **Hardening option:** enqueue `pr.opened` + the PR's
`feedback.created` in one `InTx` so they commit atomically; the strict-order
guarantee above is sufficient on its own and is the required invariant. A test
must run ingestion against a **competing concurrent `RunOutbox`** and assert no
"no merge-request bead" error ever occurs.

## Component changes

### 1. `internal/beadsbridge/bridge.go`

- Enrich `PRPayload`: `{Repo, Number, Title, Ownership, Merged, State, Branch,
Base, Author, URL, Draft, LastSyncedAt}`.
- `EventPROpened/Updated` handler passes the **full** `beads.MergeRequestFields`
  to `EnsureMergeRequest` (not just Repo+PRNumber).
- **No-resurrection guard (fixes review #3):** `ensureProcessFeedbackBead` must
  **skip** creating a processing cycle when the resolved PR bead is **closed**
  (so a `feedback.created` for a PR whose bead was manually/cascade-closed does
  not attach a live cycle under a closed parent). `EnsureMergeRequest` already
  refuses to reopen a closed bead (returns `alreadyClosed`); the bridge must
  surface/honor that rather than discard it (`bridge.go:60`). This requires
  `FindByRepoAndNumber` (or a sibling) to expose the bead's open/closed status —
  add it if absent.
- `EventPRClosed/Merged → cascadeClose` (implemented but currently **untested** —
  add coverage incl. `Merged`→`upstream-merged` reason).

### 2. `internal/store` — new `ListOpenPRs`

`ListOpenPRs(ctx, repo)` returns open `pull_request` rows for a repo (state in
`open`/`draft`), with enough fields (repo, number, ownership, state) to build the
`pr.closed`/`pr.merged` payload. Query only — no migration.

### 3. `internal/sync/sync.go` (one-shot `Sync` path)

- **`UpsertPR` unconditional** for every observed PR (hoist out of the
  `prBeadID`-gated path); commit it before processing feedback.
- **Replace inline `EnsureMergeRequest` (`:417`)** with `EnqueueEvent(pr.opened|
pr.updated, fullPayload)`. Drop the `prBeadID`/`alreadyClosed` synchronous
  return.
- **`processFeedback` gate** (`:1348`): replace `prBeadID == ""` with "repo
  configured & PR observed."
- **`maybePromoteDraft`**: keep `SetDraft(false)`; replace `UpdateMergeRequest`
  (`:1418`) with `EnqueueEvent(pr.updated, {…full payload…, State:open})`. Note
  this is a **second** `pr.updated` for the PR — see counting dedupe.
- **Close phase (`:489–525`)**: for each **healthy** repo, `store.ListOpenPRs(repo)`
  minus the observed set → `EnqueueEvent(pr.closed|pr.merged)`. Preserves
  today's "only close beads for repos that synced successfully" guard
  (`sync.go:482-489`).

### 4. `internal/sync/refresh.go` + `applyFetchedPR` (daemon path)

The daemon's PR-bead writes are here, not in `Sync` — convert them too:

- `applyFetchedPR` (`sync.go:1000`, the active-PR create/update) → emit
  `pr.opened|pr.updated` (same ordering invariant: committed before feedback).
- Hidden-team-draft `EnsureMergeRequest` (`refresh.go:68`) → emit `pr.updated`
  with `State:draft`.
- Closed/merged branch (`refresh.go:41-52`) → emit `pr.closed|pr.merged`; **remove
  the inline `CloseMergeRequest`+`cascadeClose`** so the bridge is the sole
  closer (no double-cascade).
- The per-PR `flushOutbox` (`refresh.go:81`) stays; the throwaway `Summary` is
  irrelevant (daemon emits no Summary).

### 5. `internal/replyposter` + `reconcileReplies` (folded #6)

`Reconcile` → `(int, error)` returning replies posted. Update all call sites
(`sync.go:475`, `sync.go:958`, `daemon.go:345` via `reconcileReplies`,
`sync.go:1274-1288`) and `poster_test.go`. `reconcileReplies` propagates the count
into `summary.RepliesPosted`.

## Summary counts — emit-time in the one-shot `Sync` (revised; addresses review #4/#5)

Counting is **scoped to the one-shot serial `Sync`** — the only path that produces
a user-facing `Summary`. That path processes PRs serially and flushes once, so
there is no concurrent-flusher hazard; the disproven "single flush per daemon
tick" model the earlier draft leaned on is irrelevant here.

A flush-time tally was considered and rejected: the dispatcher **swallows handler
errors** (`dispatcher.go:38` returns nil) and `RunOutbox` ignores dispatch
outcomes by design (fire-once, `outbox.go:90-91`), so flush-time cannot observe
whether the bridge actually wrote the bead **either** — it offers no fidelity gain
over emit-time, while additionally miscounting any stale leftover pending rows
from a prior crashed run and requiring a `RunOutbox` signature change.

`Sync` therefore tallies the lifecycle ops **it emits**, deduped per PR:

- `BeadsCreated` ← distinct PRs emitted with `pr.opened` (not pre-existing,
  `repoPreExisting`).
- `BeadsUpdated` ← distinct PRs emitted with `pr.updated`, **minus** those counted
  as created, so `maybePromoteDraft`'s second `pr.updated` does not double-count.
- `BeadsClosed` ← distinct PRs emitted with `pr.closed`/`pr.merged` (PR-level).
- `RepliesPosted` ← `Reconcile`'s new returned count, set in `reconcileReplies`
  (synchronous, before flush).

**Documented semantics:** the count reflects "PRs for which `Sync` emitted a
create/update/close," not a confirmed bridge write. Given fire-once + error-
swallowing dispatch, that is the most faithful count available. Residual
over-count: a `pr.opened` for an already-closed bead that the bridge no-ops via
`alreadyClosed` is still counted — acceptable, and rare. `BeadsClosed` counts
PR-bead closures only; cascade children are implied, not itemized (today they
are, `sync.go:1458`). The daemon emits no Summary; `refreshPR`'s throwaway
`Summary` stays discarded and is not counted.

## Edge cases & behaviors to preserve (test targets)

- **Ordering invariant** (above): `pr.opened` committed before that PR's
  `feedback.created`; verified under a competing concurrent `RunOutbox`.
- **Idempotent re-dispatch**: double-flush leaves exactly one bead; re-closing is
  a no-op.
- **No-resurrection** (review #3): a PR whose bead is **closed** but reappears /
  is force-pushed / reopened must not get a live processing cycle re-parented
  under a closed bead. `EnsureMergeRequest` won't reopen; `ensureProcessFeedbackBead`
  skips when the parent is closed. Tests: close→reappear, force-push, reopen.
- **`feedback.created` with no PR bead**: now structurally prevented by the
  ordering invariant.
- **Store/Dispatch become required for the PR bead.** Today inline
  `EnsureMergeRequest` is unconditional, so a nil-`Store` config still gets a PR
  bead. Under A2 the PR bead is projected from events, so **nil `Store`/`Dispatch`
  produces no PR bead**. Acceptable: production always wires the store
  (`cmd/pg-pr/sync.go:138`); the nil path is test/legacy. `sync`/`refresh` must
  degrade safely (no panic) and skip PR-bead emission when `Store`/`Dispatch` are
  nil. Tests asserting on the PR bead must wire the store.

## Testing strategy

- **Rewrite `integration_test.go`** to drive the **real** ingest→`pr.opened`→flush
  path (the current fake hard-codes `findResult` non-nil, masking the ordering
  dependency — it cannot catch a regression).
- **Concurrency test**: ingestion racing a competing `RunOutbox` ⇒ never "no
  merge-request bead."
- **Ordering test**: `pr.opened` outbox id < that PR's `feedback.created` id.
- **Idempotency test**: double-flush ⇒ one bead.
- **No-resurrection tests**: close→reappear, force-push, reopen.
- **Bridge tests**: enriched `PRPayload` lands all fields; add `EventPRUpdated`,
  `EventPRClosed`, `EventPRMerged`, `cascadeClose`, `Merged`→reason coverage
  (currently untested).
- **`store.ListOpenPRs`** unit test + healthy-repo / observed-set scoping.
- **Counts test**: one-shot `Sync` Summary reflects emitted bead ops, deduped per
  PR (a created PR isn't also counted as updated; `maybePromoteDraft`'s second
  `pr.updated` doesn't double-count).
- **`replyposter.Reconcile`** count test (folded #6 acceptance).

## Non-goals

- No A1 reconciler or `pr_bead_id` column / migration.
- No new event types beyond the existing four `pr.*`.
- No change to feedback-bead / process-cycle semantics, or to
  `feedback.disposed`/`feedback.resolved`.
