# Design — pg-pr #2: PR enrichment (kind / languages / size / urgency)

**Bead:** `pg2-4c5i.10` (epic `pg2-4c5i`, pg-pr feedback Phase 2)
**Date:** 2026-06-24
**Status:** Accepted (design)

## Goal

Compute four deterministic enrichment fields on each observed PR during sync and
persist them on the authoritative `pull_request` store row:

- **kind** — single dominant change type (feature / bugfix / refactor / docs / test / chore / other)
- **languages** — set of languages touched (coarse buckets)
- **size** — XS / S / M / L / XL bucket
- **urgency** — low / medium / high, with a transparent score + recorded reasons

These drive **#1 (diff-review generation)** reviewer selection (kind + languages)
and **#3 (attention signals)** prioritization (urgency). The bead's stated
exposure is **"via store + CLI"** — so this feature persists enrichment on the
store row and surfaces it through `pg-pr pr`. Dashboard rendering and
merge-request-bead projection are explicitly out of scope (see Non-goals).

## Hard constraint

Every signal is computed **deterministically with NO LLM**. Libraries are
permitted (go-enry is used for language detection). Results need not be perfect.
Any signal that genuinely requires an LLM or external integration is split into a
follow-up bead (see below).

## Scope

### In scope (this bead)

- `internal/enrich` — a pure, deterministic, no-I/O package computing the four fields.
- Fetch the extra raw data the signals need (PR body, labels, file paths, commit
  messages) by extending the existing single per-repo GraphQL enrich query, plus
  best-effort population on the REST fallback path.
- Persist enrichment on the `pull_request` row (schema migration v2 + a dedicated,
  non-clobbering write path).
- Surface enrichment through the `pg-pr pr` CLI (read from the store).

### Follow-up beads (created under the epic; NOT in this bead)

These are additional **urgency** signals the user wants but that need more than
local PR data:

1. **Urgency: "referenced project broken on `main`"** — needs a main-branch CI
   lookup per affected project + a definition of "project". No LLM.
2. **Urgency: Jira cross-reference** — linked-ticket priority / incident flag.
   The Jira provider (`pkg/provider/issues/jira`) is currently a stub; needs
   PR→ticket linkage + a real Jira fetch. No LLM.
3. **Urgency: Slack production-incident cross-reference** — look for references to
   the PR in the incident channel and pull context. Requires an **LLM** (Slack
   access) — explicitly deferred.

### Non-goals (this bead)

- **Merge-request-bead projection.** Enrichment is NOT projected onto the
  merge-request bead (`beads.MergeRequestFields` / `store.PRPayload` / the
  beadsbridge handler). The store row is the authority; #1 reads it directly.
  Projecting onto the bead would touch four extra structs for no current consumer
  — deferred as an optional later add.
- **Dashboard rendering.** Surfacing kind/size/urgency on the web dashboard
  (`snapshot` row types + the web template) belongs with **#3** (attention
  signals), which already reworks the dashboard and consumes urgency.

## Why enrichment is decoupled from the lifecycle event (key decision)

The recently-shipped #5/#17 model makes `emitPREvent`/`emitPRClosed` write the
`pull_request` row **and** enqueue the `pr.*` event in one `store.InTx`, so a
non-self-healing lifecycle event (e.g. `pr.closed`) can never be lost.

Enrichment is different: it is **recomputed on every sync tick from current PR
data, so it self-heals**. It therefore does NOT need to be atomic with the
lifecycle event. Folding it into `emitPREvent` would also be invasive (7 call
sites, including `maybePromoteDraft` and the team-draft path that have no file/commit
data) and — critically — would be **clobbered**: `internal/sync/ingest.go` does its
own full-row `UpsertPR` (documented "last-writer-wins"), and `UpsertPR`'s
`ON CONFLICT DO UPDATE` overwrites every column it lists. Any upsert that didn't
carry enrichment would reset those columns to empty.

**Decision:** enrichment lives in columns that **only** a dedicated
`store.SetEnrichment` write touches. Those columns are **not** in `UpsertPR`'s
INSERT / `ON CONFLICT` column list, so neither the lifecycle emit nor `ingest.go`
can clobber them. The row's existence is guaranteed before `SetEnrichment` runs
(the lifecycle emit upserts the row first, in the same per-PR iteration).

## Architecture

### `internal/enrich` (new, pure)

```go
package enrich

type Input struct {
    PR      api.PR      // title, body, branch, base, additions, deletions, changedFiles, draft
    Files   []string    // changed-file paths (may be empty → languages empty)
    Commits []string    // commit messages (may be empty → kind falls back to title/branch)
    Labels  []string    // PR label names (may be empty)
    CIRuns  []api.CIRun // PR's own CI runs (for urgency)
}

type Result struct {
    Kind           string   // feature|bugfix|refactor|docs|test|chore|other
    Languages      []string // sorted, deduped, ranked by file count
    Size           string   // XS|S|M|L|XL
    Urgency        string   // low|medium|high
    UrgencyScore   int      // additive score behind Urgency (debug / ordering)
    UrgencyReasons []string // short strings naming the signals that fired
}

func Compute(in Input) Result
```

Composed of independently-unit-tested helpers: `classifyKind`, `detectLanguages`
(go-enry), `bucketSize`, `scoreUrgency`. No I/O, no clock, no network — fully
table-testable.

### Signal rules

- **size** — buckets on `additions + deletions`: `XS < 10`, `S < 30`, `M < 100`,
  `L < 500`, `XL ≥ 500`. Always computable (counts are fetched on both GraphQL
  and REST paths).
- **kind** (single dominant) — precedence: PR-title conventional-commit prefix
  (`type(scope): …`) → branch prefix (`fix/`, `feat/`, `feature/`, `refactor/`,
  `docs/`, `test/`, `chore/`) → commit-type majority (from fetched commit
  messages) → `other`. Title/branch tiers work on every path; the commit-majority
  tier only has data on the GraphQL path (the REST fallback does not fetch commit
  messages) — acceptable per "need not be perfect".
- **languages** — `enry.GetLanguage(path, nil)` per changed-file path (path-only,
  no blob contents), tally by count, return sorted+deduped (ranked by count).
  Empty when no file paths were fetched (REST fallback, or a truncated `files`
  connection on a very large PR).
- **urgency** — additive transparent score → low/medium/high, with each firing
  signal appended to `UrgencyReasons`:
  - urgency-label allowlist present (`urgent`, `p0`, `p1`, `hotfix`, `security`,
    `incident`, `critical`)
  - title+body keyword scan (`production incident`, `outage`, `hotfix`, `sev1`/`sev2`,
    `regression`, `revert`, `asap`, …)
  - any **bugfix** commit in the commit-type mix (all-feature / all-test ⇒ no bump)
  - the PR's own CI is failing

### Data fetching

Extend the **single** per-repo GraphQL enrich query
(`pkg/provider/vcs/github/enrich.go`) — no extra round-trips:

- `body`
- `labels(first: 20) { nodes { name } }`
- `files(first: 100) { nodes { path } }`
- `commits(last: 20) { nodes { commit { message } } }`

Map onto:

- `api.PR` — add `Body string`, `Labels []string`
- `vcs.EnrichedPR` — add `Files []string` (paths), `Commits []string` (messages)

Extend `truncationFlags` to flag truncated `files`/`commits` connections (a PR
with more than 100 files truncates `files`, so languages would under-count; flag
it rather than silently mislead).

REST fallback (`gh pr list` / `gh pr view`): add `body` and `labels` to
`prListFields` so the cheap signals still work; do **not** add per-PR `files`/`commits`
fetches on this path (extra round-trips / quota) — languages and the commit-majority
kind tier simply degrade to empty there.

**Quota:** the new `files`/`commits`/`labels` connections increase the per-query
`rateLimit.cost`. This interacts with the open quota concern `pg2-w977` (GitHub's
5000-points/hour limit). The enrich query already returns `rateLimit { cost remaining }`;
implementation will record/observe the post-change cost and note it on `pg2-w977`.

### Storage

Migration **v2** (bump `schemaVersion` 1→2; append one migration string). SQLite
allows one column per `ALTER TABLE`, so six statements, each with a default so
existing rows backfill and `Scan` never hits NULL:

```sql
ALTER TABLE pull_request ADD COLUMN kind            TEXT    NOT NULL DEFAULT '';
ALTER TABLE pull_request ADD COLUMN languages       TEXT    NOT NULL DEFAULT '[]';
ALTER TABLE pull_request ADD COLUMN size            TEXT    NOT NULL DEFAULT '';
ALTER TABLE pull_request ADD COLUMN urgency         TEXT    NOT NULL DEFAULT '';
ALTER TABLE pull_request ADD COLUMN urgency_score   INTEGER NOT NULL DEFAULT 0;
ALTER TABLE pull_request ADD COLUMN urgency_reasons TEXT    NOT NULL DEFAULT '[]';
```

`languages` and `urgency_reasons` are JSON-encoded `[]string` (default `'[]'` so
reads never unmarshal an empty string).

Store changes:

- `store.PullRequest` gains `Kind, Languages []string, Size, Urgency string, UrgencyScore int, UrgencyReasons []string`.
- Extend the three hardcoded SELECT column lists + `Scan` targets in lockstep:
  `GetPR`, `ListOpenPRs`, `GetPRByID` (`internal/store/pull_request.go`).
- `UpsertPR` (and `(*Tx).UpsertPR`) are **unchanged** — they do NOT list the
  enrichment columns, so they cannot clobber them.
- New `func (db *DB) SetEnrichment(ctx, repo string, number int, r enrich.Result) error`
  (and a `(*Tx)` variant if needed) doing a targeted
  `UPDATE pull_request SET kind=?, languages=?, size=?, urgency=?, urgency_score=?, urgency_reasons=? WHERE repo=? AND number=?`.
  No-op-safe if the row doesn't exist (0 rows affected); the lifecycle emit always
  creates the row first.

### Flow

In the sync per-PR path (full-sync loop and `applyFetchedPR`), AFTER the existing
`emitPREvent` (which upserts the row + enqueues the lifecycle event):

1. Build `enrich.Input` from the `api.PR` plus the `vcs.EnrichedPR` when present
   (`Files`, `Commits`, `Labels`, `CIRuns`); fall back to whatever the api.PR
   carries when enrichment wasn't bulk-fetched.
2. `r := enrich.Compute(input)`
3. `store.SetEnrichment(ctx, repo, pr.Number, r)` — errors recorded into
   `summary.Errors` (non-fatal; recomputed next tick).

A small engine helper `enrichAndStore(ctx, repo, pr, enriched)` encapsulates
steps 1–3. The closed/merged and team-draft/draft-promote emit paths do NOT call
it (no file/commit data; enrichment left intact from the prior tick).

### CLI exposure

`pg-pr pr` surfaces the persisted enrichment. The `pr` command does not currently
open the store; this feature adds a read-only `store.Open` + `GetPR` in the
`pr show` / `pr info` path (`cmd/pg-pr/pr.go`) and renders kind/languages/size/urgency
(+ urgency reasons) in both the human and `--json` output. When the store has no
row yet (PR not synced), the fields are omitted/empty.

## Error handling & degradation

- Missing bulk enrichment (REST path / GraphQL failure) ⇒ partial Result: size
  and title/branch-derived kind always populate; languages empty; urgency uses
  whatever signals are present (labels via REST, CI, title/body). Documented, not
  an error.
- Truncated `files` connection ⇒ languages computed from the first 100 paths;
  truncation flagged.
- `SetEnrichment` failure is non-fatal (recorded in `summary.Errors`); enrichment
  is recomputed on the next sync tick.

## Testing (TDD)

- `internal/enrich` table tests per helper: `classifyKind` (title/branch/commit
  precedence + fallback), `detectLanguages` (go-enry over sample path sets,
  empty-input case), `bucketSize` (boundary values), `scoreUrgency` (each signal
  in isolation + combinations + reasons + degradation with empty inputs).
- `store` migration v2 test (user_version == 2, idempotent re-run) and an
  `UpsertPR` → `SetEnrichment` → `GetPR` round-trip asserting enrichment persists
  and that a subsequent plain `UpsertPR` (and an `ingest`-style upsert) does NOT
  clobber it.
- One sync integration test: a PR with files/labels/commits flows through to a
  populated store row; a no-files PR degrades to empty languages with size+kind
  still set.
- go-enry build: confirm the default pure-Go (no-CGO) path so it does not conflict
  with the `modernc.org/sqlite` no-CGO posture.

## Dependencies

Add `github.com/go-enry/go-enry/v2`: `go get` + `go mod tidy` +
`nix run github:nix-community/gomod2nix -- generate` (regenerates `gomod2nix.toml`).
Verify the build does not pull the optional `oniguruma` CGO path.
