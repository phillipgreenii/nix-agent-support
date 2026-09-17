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
bead-shape classifier this docket's sibling "run issue for the beads backend" packet uses for its
own reverse (bead -> linked PR) lookup — one parser, not two.

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
  Closed, with its open feedback cycle, only on a CONFIRMED closure — a `--change removed` re-read
  (or an ordinary/sweep `pr show`) reporting `merged` or `closed`, or a removed re-read's own
  `not_found` (a closure with reason `gone`) — never because the PR merely left a query. An
  already-closed anchor is never reopened.
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
interpretation writer) and does NOT fail the `run` invocation — `run`'s own exit-code contract
(`0` on success or a degraded run, `1` only for a triggering-entity fetch or store failure — see
[`pipeline-run.md`](pipeline-run.md)) is unaffected by a sync failure. `sync_error` surfaces on the
dashboard and counts into `runs_failed_24h`, and is retried by the next sweep.

## Telemetry and logs (D24)

Sync emits nothing over OpenTelemetry or Prometheus of its own in this phase — export is a later
observability item, alongside pg-router's own metrics sink (the design's `pr-pool` naming predates
the pg-router rename, bead `pg2-myc6y`). Sync's own outcome (whether it ran, and any `sync_error`)
is folded into `run`'s existing structured JSON log line to stderr — it does not add a separate log
line or stream.

## Out of scope (Phase 10)

- The bead-shape classifier's OTHER consumer — reverse (bead -> linked PR) resolution for `pg-desk
run issue` against the beads backend — is this docket's sibling packet, not this one.
- Jira/Slack sync, the cross-reference step, and layered urgency are Phase 13.
- `daemon.enable` is this docket's last packet.
- The store-wide sweep that re-verifies every ledger row whose entity has left every gathered
  query (as opposed to the single triggering entity a `sweep` change re-verifies) needs a
  store-wide driver this packet's single-entity `Sync` call does not have; it is not implemented
  here.
