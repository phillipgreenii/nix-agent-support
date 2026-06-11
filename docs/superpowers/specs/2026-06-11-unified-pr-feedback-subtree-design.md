# Unified PR-Feedback Subtree — Design

**Status:** Draft
**Date:** 2026-06-11
**Topic:** Serve _both_ of `processFeedback`'s cache-less passes (the CI-success resolver and the dedup) from the single `bd dep tree <pr> --direction=up --json` call already made for dedup, eliminating the remaining `O(F)` `ListFeedback(cycleID)` `isChildOf` scan per refresh.

**Follow-up to:** [`2026-06-11-pr-scoped-feedback-dedup-design.md`](2026-06-11-pr-scoped-feedback-dedup-design.md) (which routed only the _second_ pass through the dep tree). Bead: `pg2-wyt8`.

---

## Problem

The prior fix made `processFeedback`'s **second pass** (dedup → create) cost one scoped `bd dep tree` call via `PRFeedbackFingerprints`. But the **first pass** (the CI-failure→success resolver) still calls the expensive primitive directly:

```go
// internal/sync/sync.go ~line 1398, cache == nil branch
open, err = bdc.ListFeedback(ctx, cycleID, false)
```

`ListFeedback(cycleID, …)` with a non-empty `cycleID` runs `bd list --type=feedback --limit=0` (every feedback bead in the workspace) and then filters each with a per-bead `isChildOf` = `bd dep list <fb>` (`feedback.go:223`, `processingcycle.go:130`). So one such call is **`O(F)`** bd subprocesses, where `F` = total workspace feedback.

**Measured:** `F ≈ 475` in the live monorepo workspace. The first pass runs **unconditionally whenever an open processing-cycle exists** (`found == true`), which is the common case for any PR under active review. So every such refresh still pays one ~475-call `bd dep list` fan-out — even after the dedup fix. The team worker grinds this per team PR, so `pg_pr_sync_pr_duration_seconds_count` plateaus (~40) while the change detector keeps enqueuing → the team queue grows instead of draining (3→5+). Not hung — glacial.

> The cost is the `O(F)` **call count**, not any per-call latency. The earlier "slow `/Volumes` mount" theory was never measured; ignore it.

The waste is structural and now redundant: the first pass needs the PR's _open_ feedback, the second pass needs the PR's feedback _fingerprints_, and **both are already present** in the one dep-tree the second pass fetches.

## Key finding

`bd dep tree <prBeadID> --direction=up --json` returns the PR's recursive parent-child subtree (`MR → processing-cycle → feedback`) in **one** call, each node carrying its full `metadata` (`fingerprint`, `external_id`, `kind`) and `status`. The prior spec verified this live (`zr-wzy2`) and shipped `PRFeedbackFingerprints` on top of it. The first pass's data needs — open feedback (`status != "closed"`) with `external_id` for the CI-success ExternalID match — are all present in that same tree. So **one dep-tree call can serve both passes**.

### Behavior change: first-pass scope widens from one cycle to all the PR's cycles

This is a deliberate, called-out change, not a "strict subset." Today the first pass sources open feedback from `ListFeedback(ctx, cycleID, false)` — scoped to the **single open cycle** (`isChildOf(cycleID)` filter, `feedback.go:223`). The dep-tree subtree spans **all** of the PR's processing-cycles, including closed ones, and closing a cycle does **not** close its feedback children — so the new first pass would also consider open `ci-failure` feedback under old/closed cycles. This is safe and arguably more correct:

- The match is `fb.Fields.ExternalID == ev.externalID`, and a CI event's `externalID` is the run's unique `r.ID` (`ciRunEvent`, `sync.go:1831`) — _not_ the check name (the `sync.go:1413` "carried as external_id … name" comment is misleading). A fresh success event's `r.ID` rarely equals an old cycle's failure run id, so cross-cycle matches are uncommon in practice.
- When one _does_ match, `MarkFeedbackResolvedUpstream` → `CloseFeedback` is idempotent on already-closed beads (`feedback.go:185`), and closing a genuinely-still-open stale `ci-failure` because CI now succeeds is the correct outcome.

So the widening cannot wrongly suppress live feedback; at worst it resolves a stale failure under a prior cycle, which is desirable.

## Non-goals / settled decisions

- **PR-scoped semantics preserved.** Same recursive up-tree; no move to a global feedback set (fingerprints are not globally unique — see prior spec).
- **Cache / full-`Sync` path unchanged.** It already serves both passes in-memory from `TickCache` (`FeedbackUnder` / `FindFeedbackForPR`). This design touches only the `cache == nil` (daemon per-PR) branch.
- **`ListFeedback` itself is not removed.** It stays for `FindFeedbackByFingerprint`, `ListFeedbackPendingReply`, the full-`Sync`/`TickCache` build, and its tests. This design only stops the hot path from calling it.
- **No change to fingerprint computation, ExternalID matching, or feedback creation.**

## Design

### Component 1 — `beads`: generalize `PRFeedbackFingerprints` → `PRFeedbackInSubtree`

Replace the fingerprint-set method with one that returns the full feedback objects, in `deptree.go`:

```go
// PRFeedbackInSubtree returns every feedback bead in prBeadID's recursive
// parent-child subtree (MR -> processing-cycle -> feedback), from a single
// `bd dep tree <pr> --direction=up --json` call. It avoids the per-bead
// isChildOf scan that ListFeedback(cycleID) incurs, so cost is O(1) bd calls
// per PR regardless of workspace feedback count. Includes feedback of all
// statuses (open + closed); callers filter by Status as needed.
func (c *Client) PRFeedbackInSubtree(ctx context.Context, prBeadID string) ([]Feedback, error)
```

- Runs `bd dep tree <prBeadID> --direction=up --json` (identical to `PRFeedbackFingerprints` today).
- Decode with the existing `parseBDList(out)` (dep-tree node JSON is byte-identical to `bd list --json`). For each `iss` with `iss.Type == TypeFeedback`, build a `beads.Feedback{ID, Title, Status, Fields: feedbackFieldsFromMetadata(iss.Metadata)}` — structurally `ListFeedback`'s decode loop, sourced from the tree instead of `bd list` + `isChildOf`. Non-feedback nodes (MR root, cycle, action `task`/`bug`) are filtered by the `Type` check.
- Empty / whitespace `prBeadID` → error. Empty tree → `nil, nil`. A bd or decode error → returned to the caller.
- `PRFeedbackFingerprints` is **removed** (its only production caller and tests migrate below).

### Component 2 — `sync`: rename capability + helper

- Interface `feedbackFingerprinter` → `feedbackSubtreeReader`:
  ```go
  type feedbackSubtreeReader interface {
      PRFeedbackInSubtree(ctx context.Context, prBeadID string) ([]beads.Feedback, error)
  }
  ```
- `existingFeedbackFingerprints` → `prFeedbackSubtree(ctx, bdc, prBeadID) ([]beads.Feedback, error)`: type-asserts `bdc` to `feedbackSubtreeReader`; implementers (production `*beads.Client`) return `PRFeedbackInSubtree(...)`; non-implementers (test fakes that don't opt in) return `nil, nil` (both passes become no-ops — safe only for fakes that don't assert feedback behavior).

### Component 3 — `processFeedback` (cache == nil branch only): one fetch, both passes

Fetch the subtree once, immediately after resolving `cycleID`/`found`, before the first pass:

```go
var subtreeFeedback []beads.Feedback
if cache == nil {
    var err error
    subtreeFeedback, err = e.prFeedbackSubtree(ctx, bdc, prBeadID)
    if err != nil {
        // Can't read the PR's feedback — skip this tick rather than risk
        // duplicate beads (second-pass concern) or mis-resolving CI feedback.
        return nil
    }
}
```

- **First pass** (still gated on `found`): replace the `bdc.ListFeedback(ctx, cycleID, false)` call with an in-memory filter of `subtreeFeedback` to `Status != "closed"`. The CI-success resolver loop (match by `ExternalID`, `MarkFeedbackResolvedUpstream`) is unchanged.
- **Second pass**: build `seen` from the same `subtreeFeedback` slice:
  ```go
  seen = map[string]bool{}
  for _, fb := range subtreeFeedback {
      if fb.Fields.Fingerprint != "" {
          seen[fb.Fields.Fingerprint] = true
      }
  }
  ```
  (Identical contents to the old `PRFeedbackFingerprints` map — all statuses, non-empty fingerprints.) Per-event dedup stays an in-memory `seen[fingerprint]` lookup. The dedup-creation loop is unchanged.

The `cache != nil` branch of both passes is untouched.

### Data flow

`refreshPR` → `applyFetchedPR` → `processFeedback` → (once, cache-less) `prFeedbackSubtree` → `bdc.PRFeedbackInSubtree` → **one** `bd dep tree --direction=up`. Both passes read the returned slice in memory.

### Cost

|                              | `O(F)` `ListFeedback` scans per refresh | `bd dep tree` calls per refresh |
| ---------------------------- | --------------------------------------- | ------------------------------- |
| Prior dedup fix (this entry) | **1** (first pass)                      | 1 (second pass)                 |
| **This design**              | **0**                                   | **1** (both passes)             |

## Error handling

One deliberate consolidation: today the two reads fail differently — a first-pass `ListFeedback` error is swallowed (`open = nil`, creation still proceeds), while a second-pass `existingFeedbackFingerprints` error does `return nil` (skip the tick to avoid duplicate beads). Since they collapse into one call, on error this design takes the **conservative** path — `return nil` (skip the whole tick). Erring toward "don't create duplicates" is the safer of the two behaviors; it is commented at the call site. A subsequent tick retries once the dep tree is readable again.

## Testing

- **`beads` unit test (`deptree_test.go`):** migrate `TestPRFeedbackFingerprints_FromDepTree` → `TestPRFeedbackInSubtree_FromDepTree` and `TestPRFeedbackFingerprints_EmptyID` → `TestPRFeedbackInSubtree_EmptyID`. Reuse the existing `cannedRunner`. The fixture (already present) has a `merge-request` root, a `task` cycle, feedback nodes of mixed status (one open w/ fingerprint, one closed w/ fingerprint, one open w/o fingerprint), and an action `task`. Add `external_id` + `kind` to at least the open feedback node. Assert: returns exactly the feedback nodes (count = 3 for the current fixture), each with correct `Status` and parsed `Fields` (Fingerprint, ExternalID, Kind); non-feedback nodes excluded; exactly one bd call with args `dep tree <id> --direction=up --json`.
- **`sync` unit test (`feedback_dedup_test.go`):** rename `fpCountBeads.PRFeedbackFingerprints` → `PRFeedbackInSubtree` returning `[]beads.Feedback` (the seeded existing feedback), counting calls. Keep `TestProcessFeedback_DedupIsHoistedOutOfEventLoop` asserting the subtree is read **once** per refresh and the dup is skipped while net-new events are created (its `fingerprints map[string]bool` field becomes a `[]beads.Feedback` seed; build the dup feedback with the dup event's fingerprint).
- **New `sync` unit test (the unified-path contract):** seed `fpCountBeads` with an _open_ `ci-failure` feedback carrying `ExternalID == "run-x"` (status `hooked`), plus a duplicate-fingerprint comment feedback. Drive `processFeedback` with a CI-success run and a duplicate comment event. **The success run must set `ID: "run-x"`** (`api.CIRun.ID`, `api/types.go:29`) — `ciRunEvent` carries `r.ID` (not the run _name_) as `externalID` (`sync.go:1831`), despite the misleading "carried as external*id … name" comment at `sync.go:1413`; wiring `Name` instead would make the close half silently never fire. Assert: (a) `PRFeedbackInSubtree` called exactly once; (b) the open `ci-failure` feedback is closed via `MarkFeedbackResolvedUpstream` (first pass, drawn from the subtree); (c) the duplicate comment is \_not* recreated (second pass dedup from the same slice). This proves both passes are served by the single read. Fake plumbing: `fpCountBeads.FindOpenProcessingCycle` already returns `("cycle-1", true, nil)` (`feedback_dedup_test.go:23`); separately, add a `MarkFeedbackResolvedUpstream` override that records the closed IDs (the embedded `noopBeads` version is a no-op, `sync_test.go:609`, so it records nothing without the override).
- **No other test changes behavior:** production daemon `bdc` is always `*beads.Client` (`refreshPR` → `bdClientFor` → `NewClientForRepo`); `Sync`/`SyncPR` tests use a real `*beads.Client`; the `noopBeads`-derived fakes (`internal/sync`) and the independent `stubBeads` fake (`cmd/pg-pr/sync_test.go:40`) don't implement `PRFeedbackInSubtree`, so the `nil, nil` fallback preserves their prior empty-set behavior (none assert feedback creation).
- **Real-bd regression:** `go test ./pkg/beads/ -run Feedback` (the ~3-min real-bd subset) must stay green — it exercises `CreateFeedback`/`FindFeedbackByFingerprint`/`ListFeedback`, none of which this design alters.
- **Live verification (post-rebuild, `darwin-rebuild switch` → re-bootstrap `org.nixos.pg-pr-sync` if needed):** team queue (`pg_pr_refresh_queue_depth{group="team"}`) drains to ~0; `pg_pr_sync_pr_duration_seconds_count` climbs steadily; `/api/v1/dashboard` `generated_at` advances; `mine`/`team` stay populated.

## Affected code

- `packages/pg-pr/pkg/beads/deptree.go` — `PRFeedbackFingerprints` → `PRFeedbackInSubtree` (`[]Feedback`).
- `packages/pg-pr/pkg/beads/deptree_test.go` — migrate the two tests; reuse `cannedRunner`.
- `packages/pg-pr/internal/sync/sync.go` — rename `feedbackFingerprinter` → `feedbackSubtreeReader`; `existingFeedbackFingerprints` → `prFeedbackSubtree`; rewrite `processFeedback` cache-less branch to one fetch + two in-memory passes.
- `packages/pg-pr/internal/sync/feedback_dedup_test.go` — migrate `fpCountBeads`; keep the once-per-refresh test; add the unified-path test.

## Out of scope

- The `isChildOf`-per-bead inefficiency inside `ListFeedback(cycleID)` itself (still used off the hot path) — routed around, not fixed.
- Any change to the cache / full-`Sync` path.
