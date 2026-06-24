# pg-pr feedback #5 — beadsbridge PR-lifecycle event ownership (Decision A)

**Date**: 2026-06-24
**Status**: Draft
**Bead**: `pg2-4c5i.9` (subsumes `pg2-4c5i.14` / follow-up #6)
**Epic**: `pg2-4c5i` — Phase 2 roadmap: `docs/superpowers/plans/2026-06-23-pg-pr-feedback-phase2-roadmap.md`
**Foundation**: `docs/superpowers/specs/2026-06-23-pg-pr-feedback-datastore-design.md` (storage move, "Decision A")

## Context

The storage move shipped an in-process event dispatcher + transactional outbox
and `internal/beadsbridge`, the handler that projects pg-pr's beads. The
original design ("Decision A") intended the beads handler to project the **PR
(merge-request) bead** from `pr.*` lifecycle events, with the store
`pull_request` row as the authoritative PR state.

That is only half-realized today:

- Only `feedback.created` is ever enqueued (`internal/sync/ingest.go`, 3 sites).
  Its bridge handler (`ensureProcessFeedbackBead`) creates the process-feedback
  cycle bead and **requires the PR bead to already exist**
  (`FindByRepoAndNumber` → errors if absent).
- The PR bead is still created/updated/closed by the **inline** path in
  `internal/sync/sync.go` and `internal/sync/refresh.go`, calling the `bd`
  client directly.
- The bridge's `pr.opened/updated → EnsureMergeRequest` and
  `pr.closed/merged → cascadeClose` branches are **dead in production** —
  exercised only in `bridge_test.go` / `dispatcher_test.go`.
- Evidence the dead handlers were never finished: their `PRPayload` carries only
  `{Repo, Number, Title, Ownership, Merged}` and the handler passes only
  `{Repo, PRNumber}` to `EnsureMergeRequest` — it would create a **degraded**
  bead vs. the inline path, which sets `State, Branch, Base, Author, URL, Draft,
LastSyncedAt`.

The PR bead is the parent of the process-feedback beads, and Phase 2's headline
features (#1 diff-review, #3 teammate-attention) will project **new** beads.
Whichever ownership model we pick here is the pattern those features follow, so
this is the architectural root of Phase 2.

## Decision

**Realize Decision A fully via targeted event emission (approach "A2").**

The store `pull_request` row is the authoritative PR state. `sync`/`refresh`
**emit** `pr.opened/updated/closed/merged`; the repo-routed `beadsbridge` handler
becomes the **single** code path that creates, updates, and closes the PR bead.
The inline bead-writes are removed; the dead `pr.*` handlers become live.

Rejected alternatives:

- **A1 (pure store-projection / reconciler).** `sync` writes only store rows; a
  new reconciler diffs store state vs. projected beads, tracked by a new
  `pr_bead_id` column (migration). Most faithful to "DB authoritative" and fully
  decouples projection from sync's control flow, but adds a new component +
  schema change + more tests. A2 can evolve into A1 later if projection grows;
  it is more than this step needs. **Out of scope.**
- **A3 (half measure).** Keep bead creation inline; route only close/draft
  through events. Does not realize Decision A. **Rejected.**

**Fold follow-up #6 (`Summary.RepliesPosted`) into this work.** It is the same
class of fix — a Summary count that the current code never populates — and lives
in the same `reconcileReplies`/`Summary` area this change already touches. Doing
it separately would touch the Summary-counts surface twice. Bead `pg2-4c5i.14`
is closed as subsumed.

## Why A2 is tractable (key findings)

- **The store row already exists.** `ingestFeedbackToStore` (`ingest.go:56`)
  already calls `store.UpsertPR`. The authoritative row is written today; A2
  only makes it unconditional and earlier.
- **The bridge is already repo-routed.** `newBeadsBridgeHandler`
  (`cmd/pg-pr/sync.go:80`) reads `payload.repo` and constructs the correct
  per-repo `bd` client (`beads.NewClientForRepo(path)`). Emitted `pr.*` events
  land in the right `bd` workspace with **no new routing**.
- **The `prBeadID` coupling is light.** `processFeedback` uses `prBeadID` only as
  a boolean gate (`if prBeadID == "" { return nil }`, `sync.go:1348`); the actual
  ingestion is keyed by repo/number and never uses the id. `maybePromoteDraft`'s
  only synchronous use is `UpdateMergeRequest(prBeadID, {State:"open"})`
  (`sync.go:1418`) — a bead write that becomes a `pr.updated` event.
- **The outbox is FIFO and flushed once per Sync** (`sync.go:548`). Ordering is
  preserved as long as `pr.opened` is enqueued before that PR's
  `feedback.created`.

## Architecture / data flow

Per observed PR, within one sync tick (per-PR goroutine):

```
observed PR ─▶ store.UpsertPR              (authoritative state; unconditional)
            ─▶ enqueue pr.opened | pr.updated   (full payload)
            ─▶ processFeedback → ingest → enqueue feedback.created
            ─▶ maybePromoteDraft (mine only) → SetDraft(false)
                                            → enqueue pr.updated {State: open}

close phase ─▶ store open-PRs NOT in observed set
            ─▶ enqueue pr.closed | pr.merged

end of Sync ─▶ flushOutbox → dispatcher → beadsbridge:
                 pr.opened/updated → EnsureMergeRequest (full fields)
                 feedback.created  → ensureProcessFeedbackBead (PR bead exists ✓)
                 pr.closed/merged  → cascadeClose
```

`pr.opened` vs `pr.updated` is chosen by `sync` from `repoPreExisting` (the
pre-existing-bead set it already computes at `sync.go:432`): not-pre-existing →
`pr.opened`, pre-existing → `pr.updated`.

## Component changes

### 1. `internal/beadsbridge/bridge.go`

- Enrich `PRPayload` to carry the full bead fields:
  `{Repo, Number, Title, Ownership, Merged, State, Branch, Base, Author, URL,
Draft, LastSyncedAt}`.
- `EventPROpened/Updated` handler passes the full `beads.MergeRequestFields`
  (not just Repo+PRNumber) to `EnsureMergeRequest`.
- `EventPRClosed/Merged` → `cascadeClose` (already implemented) — `Merged`
  selects reason `upstream-merged` vs `pr-closed`.
- No interface methods are removed (this is realization, not removal); the
  bridge's `BeadClient` keeps `EnsureMergeRequest`, `cascadeClose` deps, etc.

### 2. `internal/store` — new `ListOpenPRs`

Add a query returning open `pull_request` rows (optionally filtered by repo) so
close-detection reads the **store** rather than `bd ListMergeRequests`. Fields
needed: repo, number, ownership, state, head info — enough to build the
`pr.closed`/`pr.merged` payload.

### 3. `internal/sync/sync.go`

- **UpsertPR unconditional**: write the authoritative row for every observed PR,
  independent of any bead id (move/hoist out of the `prBeadID`-gated path).
- **Replace inline `EnsureMergeRequest` (`:417`)** with
  `EnqueueEvent(pr.opened|pr.updated, fullPayload)`. Remove the synchronous
  `prBeadID`/`alreadyClosed` return dependence (see Edge cases).
- **`processFeedback` gate**: replace `prBeadID == ""` with "repo is configured
  & PR observed" (it no longer needs an id).
- **`maybePromoteDraft`**: keep the external `SetDraft(false)` write; replace
  `UpdateMergeRequest(prBeadID, …)` with `EnqueueEvent(pr.updated, {State:open})`.
- **Close phase (`:489–525`)**: replace `ListMergeRequests` iteration with
  `store.ListOpenPRs` minus the observed set → `EnqueueEvent(pr.closed|pr.merged)`.

### 4. `internal/sync/refresh.go`

Single-PR refresh (`:47–68`) gets the same transformation: emit `pr.*` events
instead of inline `EnsureMergeRequest`/`CloseMergeRequest`/`cascadeClose`.

### 5. `internal/replyposter` + `reconcileReplies` (folded #6)

`Reconcile` returns `(int, error)` — the count of replies posted.
`reconcileReplies` (`sync.go:475`) propagates the count into
`summary.RepliesPosted`.

## Summary counts (emit-time, not flush-time)

Counting at flush-time is **wrong in daemon mode**: the maintenance ticker
(`daemon.go:348`) can flush the outbox before the per-Sync flush (`sync.go:548`),
so a Sync would under-report. Instead, **count at emit-time** — `sync` already
knows created-vs-updated (`repoPreExisting`) and which PRs it is closing:

- `BeadsCreated` ← count of `pr.opened` emitted (PR not pre-existing).
- `BeadsUpdated` ← count of `pr.updated` emitted for the PR row.
- `BeadsClosed` ← count of `pr.closed`/`pr.merged` emitted (PR-level).
- `RepliesPosted` ← `Reconcile`'s returned count.

The existing inline increments (`:433/:435/:520`) **relocate** to the emit sites;
the counting logic is essentially unchanged.

**Decided simplification:** `BeadsClosed` counts PR-bead closures only;
cascade-child closures execute in the bridge at flush and are **not**
individually itemized (today they are, at `sync.go:1458`). The child closure is
implied by the parent close, so the user-facing "closed N PR beads" line stays
meaningful. Documented fallback if exact child counts are ever required: have
`flushOutbox`/`RunOutbox` return a per-type tally for the closes the bridge
performs — acceptable for cascade children specifically, since they only ever
close (never create), so the daemon flush-timing caveat does not distort a
"created" count.

## Edge cases & behaviors to preserve (test targets)

- **Outbox ordering**: a PR's `pr.opened` must precede its `feedback.created` so
  the feedback handler finds the bead. Assert FIFO ordering end-to-end.
- **Idempotent re-dispatch**: at-least-once delivery must not duplicate beads
  (`EnsureMergeRequest` upsert, `cascadeClose` close-already-closed are no-ops).
- **`alreadyClosed` skip**: today `EnsureMergeRequest` returning `alreadyClosed`
  skips downstream processing for a PR whose bead is already closed. Re-express
  as: do **not** emit `pr.opened` when the store row is already closed/merged and
  the PR is not re-observed-as-active; rely on `observed = active upstream` +
  idempotent upsert otherwise. Cover with a test (closed bead must not be
  resurrected by a stale observation).
- **`feedback.created` with no PR bead**: still an error, but now structurally
  prevented by ordering rather than the inline create.
- **Store/dispatch become required for the PR bead.** Today the inline
  `EnsureMergeRequest` (`:417`) is unconditional, so a nil-`Store` config still
  gets a PR bead; only feedback ingestion is store-gated. Under A2 the PR bead is
  projected from events, so **a nil `Store`/`Dispatch` no longer produces a PR
  bead**. This is acceptable: the production CLI path always wires the store
  (`cmd/pg-pr/sync.go:138` `SetStoreAndDispatch`); the nil path was only ever
  test/legacy. Decision: A2 makes the store the required backing for the PR bead;
  `sync`/`refresh` emit events unconditionally and skip PR-bead work (degrade
  safely, no panic) when `Store`/`Dispatch` are nil. Tests that assert on the PR
  bead must wire the store (or drive the bridge directly).

## Testing strategy

- Bridge unit tests already cover the `pr.*` handlers; extend them for the
  enriched `PRPayload` (all fields land on the bead).
- `store.ListOpenPRs` unit test.
- `sync` integration test (in-memory `bd` + store): one tick creates the PR bead
  **via the outbox**, not inline; `feedback.created` finds it; counts match;
  a disappeared PR closes via `pr.closed`.
- Ordering test: `pr.opened` id < `feedback.created` id for the same PR.
- Idempotency test: double-flush leaves one bead.
- `alreadyClosed`/no-resurrection test.
- `replyposter.Reconcile` count test (folded #6 acceptance).

## Non-goals

- No A1 reconciler or `pr_bead_id` column.
- No new event types beyond the existing four `pr.*`.
- No change to feedback-bead or process-cycle semantics.
- No change to the `feedback.disposed`/`feedback.resolved` events.
