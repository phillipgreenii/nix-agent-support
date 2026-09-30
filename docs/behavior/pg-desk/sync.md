# pg-desk — sync

Sync is stage 3 of the pipeline (see [`pipeline-run.md`](pipeline-run.md)), running after
interpret for every `pr`-typed entity. It writes only agent signals, only through `pg-connector
issue` pinned to `agent_tracker_backend`, and it MUST record every write it makes (or would make)
in the `ledger` table (see [`store-schema.md`](store-schema.md)). The dedup key for every bead
kind lives IN THE BEAD, never only in the ledger: `metadata.repo` plus `metadata.pr_number` for
anchors and review requests, and the exact title `process-feedback: <repo>#<n>` for cycles. A
crash between `issue create` and the ledger write MUST NOT mint a duplicate on the next run: sync
consults the ledger first, then the work beads gather already fetched this run, before creating
anything.

## Modes

`sync.mode` is `off`, `plan`, or `apply`:

- **`off`** — sync does not run at all: no `pg-connector issue` call, no ledger write.
- **`plan`** — sync runs the full adoption and rule logic below and records every write it would
  make — kind, dedup key, and fields — as a planned row in the `ledger` table. It calls no
  `pg-connector issue` write. `pg-desk status` and `pg-desk show` print these planned rows (see
  [`operator-commands.md`](operator-commands.md)), so an operator can compare them against what
  the beads-writing sync path actually mints — the Phase 10 sync-parity check.
- **`apply`** — performs the writes for real.

A planned row is distinguished from an applied one by an empty `bead_id` in its ledger row — never
a separate column, since the design pins the ledger table's existing columns (see
[`store-schema.md`](store-schema.md) for how this packet reuses `last_synced_content_hash` as a
deterministic hash of the write's own field content, identical in `plan` and `apply` mode for the
same input, which is what makes the parity check mechanical rather than a manual read-through).

## Adoption

On every run (not just literally the first), before creating anything, sync classifies the work
beads gather already fetched this run (`issue list --query work-beads`, matched to this PR) by
title/metadata shape (anchor / feedback cycle / review request / none of those). A pre-existing
anchor or review request is adopted by its `repo`/`pr_number` metadata; a pre-existing cycle is
adopted by its exact title; a merge-request bead titled `<repo>#<n>: ...` with no `repo`/
`pr_number` metadata yet is adopted by its title prefix, and gains both metadata keys on its first
`apply` run (never in `plan`, which calls no `pg-connector issue` write at all). This is the SAME
bead-shape classifier [`run-issue.md`](run-issue.md)'s own reverse (bead -> linked PR) lookup
uses — one parser, not two.

## Bead shapes

Reproduced verbatim from the design of record; any later change MUST land with the sources and
prompts that read these shapes, in the same change:

| Kind           | bd type         | Title                          | Labels                                               | Metadata                                                                                   | Parent |
| -------------- | --------------- | ------------------------------ | ---------------------------------------------------- | ------------------------------------------------------------------------------------------ | ------ |
| anchor         | `merge-request` | `<repo>#<n>: <pr title>`       | `co-owned` when applicable, `pbase:<n>` while nudged | `repo`, `pr_number`, `state`, `branch`, `base`, `author`, `url`, `draft`, `last_synced_at` | none   |
| feedback cycle | `task`          | `process-feedback: <repo>#<n>` | `mine`, `fbsum:<digest>`                             | `repo`, `pr_number`, `branch`; description is the rendered summary of unaddressed items    | anchor |
| review request | `task`          | `review-pr: <repo>#<n>`        | as today                                             | `repo`, `pr_number`, `branch`, `head_sha`, `ownership`                                     | anchor |

## Rules

- **Anchor** — exactly one per `(repo, number)`, created lazily only when a cycle or review
  request first needs a parent (D8), never eagerly. The conflict-priority nudge (a port of
  pg-pr's existing `pbase` mechanism: stash the pre-conflict priority on the first conflicting
  tick, nudge mine/co-owned toward higher priority and team toward lower, no-op on a repeated
  conflicting tick, restore the baseline once the conflict clears) is applied via `issue update`.
  Closed, with its open cycles — BOTH the feedback cycle and the review request, symmetrically —
  only on a CONFIRMED closure — a `--change removed` re-read (or an ordinary/sweep `pr show`)
  reporting `merged` or `closed`, or a removed re-read's own `not_found` (a closure with reason
  `gone`) — never because the PR merely left a query. An already-closed anchor is never reopened.
  The cascade is type-blind, like pg-pr's own cascade-close
  (`beadsbridge.CascadeCloseMergeRequest`): every open direct child of the anchor (a bead
  filed with `--parent <anchor>`) that the work-beads query returned is closed too, whatever its
  title or type (`pg2-kftf9.7`; e.g. an improvised "Human: unblock ..." bead). It reaches only
  children present in the work-beads results, and does not walk grandchildren. An earlier
  revision closed only the feedback cycle, leaving review-pr beads open at a far higher stale
  rate (`pg2-ryexi`).
- **Feedback cycle** — for every PR with unaddressed feedback (a disposition that is still
  `open`), ensure one open cycle keyed by title and deduplicated by the `fbsum` digest (a port of
  pg-pr's existing digest computation), carrying the `mine` label only when the PR's ownership
  acts as mine.
- **Review request** — ported verbatim from pg-router's ACL: every PR with ownership `mine` or
  `co-owned` (draft included), and every non-draft team PR, gets one `review-pr` bead. When the
  head advances past the ledger's last-reviewed SHA, a completed bead is reopened
  (`issue transition <id> open`) and its metadata refreshed. No gate.
- **Recorded losses (D15)** — draft auto-promotion, `wip on`'s upstream draft conversion, and
  pending reply posting are not performed by sync.

## Failure handling

A sync failure for one PR sets that PR's `interpretation.sync_error` (via the store's existing
interpretation writer) AND fails the `run` invocation with exit `1` (see
[`pipeline-run.md`](pipeline-run.md)), so pg-router retries the event with backoff and counts it as
a failure. A failed run MUST NOT lose the diagnostic: `sync_error` is still recorded and surfaces
on the dashboard and counts into `runs_failed_24h`; a later successful run clears it. A `sweep`
that hits a sync failure for any entity still attempts every entity, then exits `1`.

Sync MUST be safe to retry. Closure is guarded by the ledger: an anchor or cycle already recorded
as closed is not closed again, and a retry after a partial failure (for example the anchor closed
but a child close failed) MUST finish the remaining closes without repeating the finished ones.

### Automatic retry

A recorded `sync_error` MUST be retried automatically when its failure is transient, and MUST NOT
be retried automatically when the failure needs a person (operator ruling, 2026-09-30, bead
`pg2-xb6fs`):

- **Transient** — the environment, not the request, is at fault: a missing path or an unmounted
  volume, a network blip, a tracker that cannot currently be reached. `pg-connector`'s
  `unavailable` code, a targeted `not_found`, a failure to start `pg-connector` at all, a timeout,
  and every failure not named in the next bullet count as transient.
- **Needs a person (non-transient)** — authentication (`unauthenticated`), validation
  (`invalid_argument`), and config or version problems (`unknown_op`, `query_not_recognized`,
  `version_mismatch`, an unknown `sync.mode`).

Every failed run of a PR that has a recorded `sync_error` counts one attempt, whichever stage
failed, and the latest failure decides the class. A transient row is retried with exponential
backoff — by default 1m, 2m, 4m, 8m, 16m, then 30m for every later retry — and MUST stop after a
bounded number of automatic retries (default 10). After that the row is **exhausted**: it stays a
`sync_error` and is not retried again automatically. A successful run clears both the
`sync_error` and its retry state. Automatic retries are performed by `pg-desk reconcile` (see
[`operator-commands.md`](operator-commands.md)), so the backoff is the earliest a retry may run;
how often the scheduler runs `reconcile` also bounds it. The bound governs only these automatic
retries: a new event for the PR still runs the whole pipeline, sync included, whatever the row's
retry state, and its failure counts as one more attempt. `pg-desk reconcile --retry-all` is the
operator's manual repair once the cause is fixed: it re-drives every recorded `sync_error` now,
ignoring backoff, the bound and the class.

The bounds are configuration: `sync.retry.max_retries` (default `10`; `0` disables automatic
retry), `sync.retry.initial_backoff` (default `1m`, doubled for each later retry) and
`sync.retry.max_backoff` (default `30m`, and not below `initial_backoff`). An invalid value fails
config load.

Every `sync_error` row carries a retry indicator — attempts so far, the retry bound, and either
the next retry time or why there will be none (exhausted, or non-transient) — kept in the store
(see [`store-schema.md`](store-schema.md)) and shown by `pg-desk status`, `pg-desk doctor` and
`serve`'s `/metrics` (see [`serve.md`](serve.md)), so an operator can tell a row that is still
being retried from one that no longer will be.

## Telemetry and logs (D24)

Sync emits nothing over OpenTelemetry or Prometheus of its own in this phase — export is a later
observability item, alongside pg-router's own metrics sink (the design's `pr-pool` naming predates
the pg-router rename, bead `pg2-myc6y`). Sync's own outcome (whether it ran, and any `sync_error`)
is folded into `run`'s existing structured JSON log line to stderr — it does not add a separate log
line or stream.

## Out of scope (Phase 10)

- The bead-shape classifier's OTHER consumer — reverse (bead -> linked PR) resolution for `pg-desk
run issue` against the beads backend — is [`run-issue.md`](run-issue.md), not this doc.
- Jira/Slack sync, the cross-reference step, and layered urgency are Phase 13.
- `daemon.enable` is this docket's last packet.
- The store-wide re-verification of ledger rows whose entity has left every gathered query is
  `pg-desk reconcile` (see [`operator-commands.md`](operator-commands.md)), not `Sync` itself:
  `Sync` stays single-entity, and `reconcile` is the store-wide driver that calls the same
  `run` path per entity.
