# pg-pr: unify per-PR enrichment on GraphQL

- **Date:** 2026-06-25
- **Bead:** `pg2-re7e` (child of epic `pg2-4c5i`, "pg-pr feedback Phase 2")
- **Status:** design approved, implementing

## Problem

Per-PR enrichment diverges from the daemon's bulk GraphQL path, producing
duplicate, `posted_at`-less `feedback` / `code_comment_message` rows.

Two ingestion paths feed the same store:

- **Bulk GraphQL** (`Provider.EnrichedPRs` → `commentsFromGHNode`, the daemon's
  path): inline review-thread comments get `ThreadID` = the review-thread node
  id (`PRRT_…`) and `CreatedAt` from the GraphQL `createdAt`. Ingest
  (`internal/sync/ingest.go`) groups by `ThreadID` → **one** feedback row per
  thread, and `code_comment_message.posted_at` is populated.
- **Per-PR REST fallback** (`Provider.ListComments`, `github.go:508`), used by
  `enrichOnePR` (`sync.go:673`) for `pg-pr sync --pr` and as the per-PR
  fallback: sets `ThreadID` = the comment node id (`PRRC_…`) and omits
  `CreatedAt`. Ingest then treats **each comment as its own pseudo-thread** →
  N duplicate feedback rows, and `posted_at` is empty.

Because the two paths key threads differently (`PRRT_` vs `PRRC_`), a manual
`pg-pr sync --pr` on a watched PR creates rows that do not converge with the
daemon's rows. Observed during the `pg2-5njr` verification: PR 88413 went 38 →
69 `code_comment_message` rows once a full GraphQL sync ran alongside the
REST-era rows, and only the GraphQL-sourced rows carried `posted_at`.

This is the concrete manifestation of the `github.go:563` TODO (refine the
thread id "when resolveThread mutation requires the actual review-thread node
id") and is governed by the node-id policy in
`docs/superpowers/specs/2026-06-23-pg-pr-feedback-datastore-design.md`: use
upstream node ids uniformly (GraphQL and REST both expose `node_id`), not the
REST `databaseId`, for `external_id` / `code_comment_message.external_id`.

## Goal

One ingestion path (GraphQL, real `PRRT_` thread ids + `createdAt`) so that
`pg-pr sync --pr` / `enrichOnePR` stop producing divergent, `posted_at`-less
duplicate rows. **No behavior change** to the already-correct bulk daemon path.
Cleanup of the existing duplicate/empty rows is explicitly deferred.

## Non-goals (deferred)

- Migrating / deduping the **existing** store rows (separate later task).
- Reworking the REST `gh pr view` review path beyond `CreatedAt`.
- GraphQL quota work (tracked separately as `pg2-w977`).

## Design

### 1. Single-PR GraphQL fetch — `pkg/provider/vcs/github/enrich.go`

- `enrichedPRByNumberQuery`: the **identical** `... on PullRequest { … }` field
  selection used by `enrichedPRsQuery`, wrapped in
  `repository(owner, name) { pullRequest(number) { … } }`.
- `parseEnrichedPR(raw []byte, repo string) (*vcs.EnrichedPR, error)`:
  unmarshals `data.repository.pullRequest` into the **same** `ghPRNode` and
  calls the **same** `prFromGHNode` / `reviewsFromGHNode` / `commentsFromGHNode`
  / `ciRunsFromGHNode` helpers. Reusing these guarantees byte-identical
  `ThreadID` (`PRRT_`) and `CreatedAt` mapping as the bulk path.
- `(*Provider).EnrichPR(ctx, repo string, number int) (*vcs.EnrichedPR, error)`.

**Completeness (pagination).** The bulk query uses `first: 30` and does not
paginate — it sets `Truncated` flags and lets the engine fall back to REST for
overflowing PRs (`truncationFlags`). That fallback is exactly what reintroduces
the divergence for busy PRs (88413 has ~38 thread comments, exceeding 30). So
the single-PR path must capture complete thread data:

- Use `first: 100` on the thread-bearing connections (`reviewThreads`, nested
  thread `comments`, top-level `comments`, `reviews`).
- For any of those connections reporting `hasNextPage`, paginate with `after`
  cursors until exhausted, merging nodes into the single `ghPRNode` before
  parsing.
- Files / commits / labels keep flag-and-fallback behavior (they don't affect
  thread identity or `posted_at`).

### 2. Route `enrichOnePR` through it — `internal/sync/sync.go:673`

- New local capability interface:
  `SinglePREnricher interface { EnrichPR(ctx, repo string, number int) (*vcs.EnrichedPR, error) }`.
- `enrichOnePR`: if the configured provider implements `SinglePREnricher`, call
  `EnrichPR`; on success return its result. On **error or unimplemented**, fall
  back to the existing REST per-method path (`ListReviews` / `ListComments` /
  CI), logged at `WARN` so any divergence remains observable.

### 3. Fix REST `ListComments` — `github.go:508`

Populate `CreatedAt` from the REST `created_at` field for both issue comments
and review comments. This keeps the fallback correct on `posted_at` even though
REST cannot supply `PRRT_` thread ids. The `ghIssueComment` and
`ghReviewComment` structs gain a `CreatedAt` field mapped from the REST
`created_at` JSON key.

## Data flow (after change)

```
sync --pr N        daemon (bulk)
     │                   │
enrichOnePR          enumerate → EnrichedPRs (search)
     │                   │
EnrichPR (GraphQL)       │            ── both produce vcs.EnrichedPR with
 repository.pullRequest  │               PRRT_ ThreadID + createdAt via the
     │                   │               SAME *FromGHNode parsers
     └─────────┬─────────┘
               ▼
   ingestFeedbackToStore  → 1 feedback per PRRT thread, posted_at set
               │
        (REST fallback only on GraphQL error → WARN logged)
```

## Error handling

- `EnrichPR` GraphQL/transport error → `enrichOnePR` logs WARN and falls back to
  REST (no hard failure; matches today's resilience).
- A GraphQL `errors` array in the response → returned as an error (same as
  `parseEnrichedPRs`).
- Pagination guarded by a sane cap to avoid unbounded loops on pathological PRs;
  if the cap is hit, set/return a truncation signal and WARN.

## Testing (TDD)

- `parseEnrichedPR`: golden single-PR GraphQL response → `EnrichedPR` with
  `PRRT_` `ThreadID` and RFC3339 `CreatedAt`; a multi-page fixture asserts
  merged completeness.
- `EnrichPR`: fake `gh` runner returning canned JSON, including a paginated
  case (two pages of thread comments merged).
- `enrichOnePR` routing: a provider implementing `SinglePREnricher` is used; on
  `EnrichPR` error and on a provider lacking the capability, the REST path runs.
- `ListComments`: asserts `CreatedAt` populated from `created_at`.
- Ingest parity: feeding the single-PR `EnrichedPR` through ingest yields **one**
  feedback per thread with non-empty `posted_at` (parity with the bulk path).
- `go test ./...` in `packages/pg-pr` stays green; the bulk-path tests are
  unchanged.

## Verification (manual, after merge/deploy)

- `pg-pr sync --pr <open watched PR>` does not grow `code_comment_message` row
  counts with duplicates, and re-ingested messages carry `posted_at`.
