# PR-Scoped Feedback Dedup — Design

**Status:** Draft
**Date:** 2026-06-11
**Topic:** Make the daemon's per-PR feedback dedup query the PR's own dep subtree in one `bd` call, instead of scanning the whole workspace's feedback per cycle.

---

## Problem

`processFeedback` dedups each upstream event (comment / CI run) against the PR's existing feedback before creating a new feedback bead. The cache-less (daemon) path resolves "does a feedback with this fingerprint already exist for this PR?" via a chain that scans the **entire workspace's feedback**:

- `ListFeedback(cycleID, true)` runs `bd list --type=feedback --all` (every feedback bead in the workspace) and then filters each one with a per-bead `isChildOf` call (`bd dep list <fb>` — `processingcycle.go:130`). So one `ListFeedback(cycleID)` is `O(F)` bd subprocesses, where `F` = total workspace feedback.
- The earlier fix ([2026-06-10 spec](2026-06-10-per-pr-refresh-throughput-design.md)) hoisted dedup out of the per-event loop (built the set once per refresh), cutting an `O(events)` multiplier. But the per-refresh set-build still calls `ListFeedback(cycle)` per cycle, so it remains `O(cycles × F)`.

**Measured:** `F = 475` feedback beads in the live monorepo workspace. So even the hoisted set-build issues ~475 × cycles `bd dep list` calls per PR — still slow enough that refreshes don't complete a tick.

The waste is structural: dedup needs only **this PR's** feedback, but the query walks all feedback and tests each for membership.

## Key finding (probe)

`bd dep tree <prBeadID> --direction=up --json` returns the PR's **recursive parent-child subtree** (`MR → processing-cycle → feedback`) in **one** bd call, and each node carries its full `metadata`. Verified live on `zr-wzy2`: filtering the tree to `issue_type == "feedback"` yielded the feedback nodes with `metadata.fingerprint` populated (e.g. `0fb5486967d665f3`). The tree includes nodes of all statuses, so open+closed feedback are both covered — matching the existing dedup semantics (`FindFeedbackByFingerprint` used `includeClosed=true`).

So the PR's feedback fingerprint set is obtainable in **one scoped bd call**, with no `isChildOf` fan-out and no whole-workspace feedback fetch.

## Non-goals / settled decisions

- **PR-scoped dedup is preserved (not global).** Event fingerprints are NOT globally unique — `commentEvent` hashes `(author, path, line, body)` and `ciRunEvent` hashes `(provider, name, conclusion)`, so e.g. a `check-gradle-jdk17` failure yields the same fingerprint on every PR. Deduping against a global set would wrongly suppress feedback on all but one PR. The dep-tree approach stays correctly PR-scoped (same recursive up-tree the `TickCache` uses).
- **Cache / full-`Sync` path unchanged.** It already dedups in-memory via `TickCache.FindFeedbackForPR`.
- **No change to fingerprint computation or feedback creation.**

## Design

### Component 1 — `beads`: `PRFeedbackFingerprints`

Add a method to `*beads.Client` (the package that owns bd-JSON parsing):

```go
// PRFeedbackFingerprints returns the set of feedback fingerprints in prBeadID's
// recursive parent-child subtree, from a single `bd dep tree <pr> --direction=up
// --json` call. It avoids the per-bead isChildOf scan that ListFeedback(cycleID)
// incurs, so cost is O(1) bd calls per PR regardless of workspace feedback count.
func (c *Client) PRFeedbackFingerprints(ctx context.Context, prBeadID string) (map[string]bool, error)
```

- Runs `bd dep tree <prBeadID> --direction=up --json`.
- Parses each node's `issue_type` and `metadata`; for nodes with `issue_type == "feedback"`, extracts the fingerprint via the existing `feedbackFieldsFromMetadata` helper (same parser `ListFeedback` uses) and adds non-empty fingerprints to the set.
- Empty / whitespace `prBeadID` → error. Empty tree → empty set. A bd error → returned to the caller.

This lives alongside `DepTreeUp` in `deptree.go` (both consume the same `bd dep tree` output; `DepTreeUp` already parses `id/title/status`, this additionally reads `issue_type` + `metadata.fingerprint`).

### Component 2 — `sync`: consume it via a narrow interface

`processFeedback` already calls `e.existingFeedbackFingerprints(ctx, bdc, prBeadID)` (from the prior fix). Rewrite that helper to use the new beads method:

- New narrow capability interface in `sync` (mirroring `humanLabelReader`):
  ```go
  type feedbackFingerprinter interface {
      PRFeedbackFingerprints(ctx context.Context, prBeadID string) (map[string]bool, error)
  }
  ```
- `existingFeedbackFingerprints` type-asserts `bdc` to `feedbackFingerprinter`:
  - implements it (production `*beads.Client`) → return `PRFeedbackFingerprints(...)`.
  - does not (test fakes that don't opt in) → return an empty set, nil (no dedup; safe for unit tests).
- The old `ListChildrenOfPR` + per-cycle `ListFeedback` body is removed.

### Data flow

`refreshPR` → `applyFetchedPR` → `processFeedback` → (once, cache-less) `existingFeedbackFingerprints` → `bdc.PRFeedbackFingerprints` → **one** `bd dep tree --direction=up`. Per-event dedup is an in-memory `seen[fingerprint]` lookup (unchanged from the prior fix).

### Cost

|                           | bd calls per PR for dedup   |
| ------------------------- | --------------------------- |
| Original (per-event)      | `O(events × cycles × F)`    |
| Prior hoist (per-refresh) | `O(cycles × F)`             |
| **This design**           | **`O(1)`** (one `dep tree`) |

## Error handling

- `PRFeedbackFingerprints` bd error → `existingFeedbackFingerprints` returns the error → `processFeedback` skips creation this tick (matches the prior skip-on-error behavior, avoiding duplicate beads when the PR's feedback can't be read).
- Malformed/again-empty metadata on a node → that node contributes nothing (skip); never fatal.

## Testing

- **`beads` unit test** (fake `CLIRunner`): feed a sample `bd dep tree --json` array containing a merge-request node, a processing-cycle/task node, and two feedback nodes (one with a fingerprint, one missing) plus an action node. Assert `PRFeedbackFingerprints` returns exactly the populated feedback fingerprint(s) and ignores non-feedback nodes. Assert the runner was invoked once with `dep tree <id> --direction=up --json`.
- **`sync` unit test:** update the existing `TestProcessFeedback_DedupIsHoistedOutOfEventLoop` — the fake implements `PRFeedbackFingerprints` (counting calls) instead of `ListChildrenOfPR`/`ListFeedback`. Assert it is called **once** per refresh (not per event/cycle) and that the duplicate event is skipped while net-new events are created.
- **Live verification (post-rebuild):** refreshes complete; `pg_pr_sync_pr_duration_count` climbs; `mine`/`team` populate; `generated_at` advances.

## Affected code

- `packages/pg-pr/pkg/beads/deptree.go` — add `PRFeedbackFingerprints` (+ unit test in `deptree_test.go`).
- `packages/pg-pr/internal/sync/sync.go` — `existingFeedbackFingerprints` rewritten to use the new method via the `feedbackFingerprinter` type-assert; add the interface.
- `packages/pg-pr/internal/sync/feedback_dedup_test.go` — update the fake + assertions for the one-call contract.

## Out of scope

- The `isChildOf`-per-bead inefficiency inside `ListFeedback(cycleID)` itself (still used by the full-`Sync`/`TickCache` build and elsewhere) — not touched here; this design routes the daemon dedup around it.
