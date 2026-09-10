# pg-pr retirement: removal criterion and SQLite disposition rulings

**Status**: Accepted
**Date**: 2026-09-10
**Deciders**: phillipg

## Context

The unified pluggable connector architecture program (epic `pg2-2j5ac`) retires `packages/pg-pr`
once `pg-connector` (and, downstream of it, `pg-desk`) covers every command group pg-pr exposes
today. That program's design of record states two decisions this repo has not yet recorded
durably anywhere: a removal criterion — the conditions that together mean pg-pr is safe to
delete — and a disposition for every table pg-pr's SQLite store holds. Both were already decided
(the removal criterion originally had six conditions; a later amendment added four more, for ten
total), but only in the program's own ephemeral planning document, which this repo's citation
conventions treat as a one-time extraction source, not a durable reference — anything relying on
it as its only record is expected to dangle once that document is deleted. This ADR is that
durable record: a first-time transcription of decisions already made, not a new decision, and it
does not itself execute any of the ten conditions or any table's disposition. Whichever future
phase or packet actually builds a given destination, or performs the eventual
`packages/pg-pr` deletion, satisfies the relevant condition(s) incrementally; this ADR exists so
that work can cite a durable source instead of re-deriving the criterion from the (by then
possibly deleted) planning document.

`packages/pg-connector`'s `pg-connector-pr-github` backend previously had its own ADR
([0063](0063-pg-connector-pr-github-disposition-store-migration.md)) covering a related but
narrower question — how to migrate pg-pr's `feedback` disposition table into that backend's own
store. That ADR's own header already marks it Superseded (2026-09-10): a later design amendment
decided that dispositions are re-derived rather than migrated, so `feedback` now drops instead of
moving. This ADR's disposition table below reflects that current decision; it does not reopen or
re-decide ADR 0063's historical content.

## Decision

### Removal criterion

`pg-pr` as a standalone binary MUST NOT be deleted until every one of the following ten
conditions holds, with no coexistence period required in between:

1. Every "Destination" cell in the program's verb-to-destination table that isn't already "Ships
   today" has landed and passed its own tests in `pg-connector` — including the `pr review`/
   `pr comment`/`config show`/`pr <write-verb>` group of new verbs.
2. Every real call site found for a retired verb — across `claude-marketplace/pg-pr/**`,
   `flake.nix`'s pinned checks, the tldr page, the capabilities list, the cross-plugin references,
   and pr-pool's own hardcoded `pg-pr` sites — has been rewritten to call `pg-connector` instead,
   and a repo-wide grep for a literal `pg-pr` invocation (excluding the design doc, ADRs, and other
   historical/prose references) returns zero hits.
3. Every table in pg-pr's SQLite store has a stated and executed disposition (migrate under the
   PR GitHub backend, drop, or otherwise) — not just the feedback-disposition table already
   covered.
4. The two open dispositions for `pg-pr open` and the local dashboard it reads are resolved one
   way or the other (kept with a stated pg-connector-backed replacement, or dropped) rather than
   left open.
5. The cross-backend test guards (`TestGHExecChokePoint`, `TestNoGHStackMutatingArgv`) have been
   relocated out of pr-github's own package, and still pass from their new home.
6. `pg-pr`'s own retiring test suites (`pkg/beads`, `internal/beadsbridge`) have an explicit
   disposition recorded — moved, rewritten against the new seam, or accepted as a coverage loss —
   rather than silently deleted with the binary.
7. pr-pool runs as a daemon at ZR and `pg-desk serve` has served the dashboard in `sync.mode: plan`
   for at least one working day, operator-tuned, before the phase 11 flip.
8. The flip has run in `sync.mode: apply` with `pg-pr-sync` disabled for at least one working day,
   operator-tuned, before any phase 12 deletion.
9. The `annotation` import has run.
10. `store.db` is deleted only with the module.

Conditions 1-6 are the criterion's original six; conditions 7-10 were added by a later design
amendment once items 4 and 3 (the dashboard/`open` disposition and the per-table SQLite
dispositions) were fully resolved. Only once all ten hold does the `packages/pg-pr` module itself
get deleted. This is intentionally a condition-based criterion rather than a calendar date: each
condition is independently checkable at any time, so the dual-maintenance window ends exactly
when the work is actually done, not on a schedule that can slip unnoticed.

### Per-table SQLite disposition rulings

pg-pr's SQLite store holds several tables. Every one now has a final, decided disposition:

| Table(s)                                                  | Disposition                                                                                                                                                                                                        |
| --------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `feedback`, `code_comment_message`                        | **DROP.** Dispositions are re-derived by `pg-desk`'s interpreter on every run rather than migrated; Phase 3 already executed this table's own cutover and migrated zero real rows.                                 |
| `pull_request`, `pr_revision`, `pr_approval`              | **DROP.** Re-derived into `pg-desk`'s `entity`/`interpretation` tables on the first sweep; revision history is an accepted loss.                                                                                   |
| `pull_request.user_hidden`, `.user_hidden_reason`, `.wip` | **MIGRATE**, via a future `pg-desk import-pg-pr-annotations` tool, into `pg-desk`'s `annotation` table — the one part of this data that is a client/dashboard-layer concept, never a pg-connector wire concept.    |
| `outbox`, `repo_sync_state`, `data_migration`             | **DROP**, with no data carried over — these back the retiring `sync` daemon and `changes` machinery, which have no rewrite target.                                                                                 |
| `store.db` (the file itself)                              | Deleted only with the `packages/pg-pr` module — the phase 11 flip disables the daemon and leaves the file and its command groups in place; phase 12 deletes the retiring command groups and still leaves the file. |

These rulings follow from the same program's own operator decisions: the backend-local-store
rejection that makes categorization and feedback disposition re-derived data rather than migrated
state, the destination decisions for `pg-pr open` and the local dashboard, and the decision that
human annotations (the hide/WIP columns) get their own durable home in `pg-desk`'s `annotation`
table rather than dropping with the rest of `pull_request`. Only the `feedback`/
`code_comment_message` cutover above has actually executed (Phase 3, `pg2-2j5ac.19`); every other
row in this table is decided but not yet executed — execution is a future phase's or packet's job,
gated on each row's own named destination actually shipping.

## Consequences

### Positive

- The removal criterion and the per-table SQLite dispositions are now durably recorded in this
  repo's own decision log, independent of the program's ephemeral planning document. A future
  phase or packet that builds a destination, or performs the eventual `packages/pg-pr` deletion,
  can cite this ADR by number instead of re-deriving the criterion or re-locating the disposition
  rulings from a document slated for deletion.
- Recording all ten conditions (rather than the original six) in one place removes any ambiguity
  about which version of the criterion is current — a reader landing on this ADR after the design
  document is gone still gets the complete, amended set.

### Negative

- This ADR is a decision record, not an implementation: none of the ten conditions is any closer
  to being satisfied, and none of the disposition rulings is any closer to being executed, as a
  result of writing it down. That work remains spread across future phases of the same program.
- The removal criterion's conditions reference other in-flight work (the `pg-desk` design's own
  phases and tables) that has not shipped yet. Should that downstream design change materially,
  this ADR's numbered list could itself need a superseding amendment — the same fate ADR 0063 saw.

### Neutral

- Cross-referencing ADR 0063 as historical/superseded context, rather than restating or re-deciding
  its content, keeps this repo's decision log consistent: the disposition-store migration
  mechanism ADR 0063 built is retired in favor of re-derivation, and this ADR is the place that
  says so without duplicating ADR 0063's own now-historical Decision section.

## Related Decisions

- Supersedes no prior ADR's decision — this is the first formal decision-log record of the removal
  criterion and the per-table SQLite dispositions; the program's design document is their original
  source, and this ADR is its durable decision-log copy.
- Cross-references ADR [0063](0063-pg-connector-pr-github-disposition-store-migration.md) (already
  marked Superseded in its own header) as prior/historical context for the `feedback` table's
  disposition, without re-deciding its content.
- Builds on the self-contained-module rule recorded in ADR
  [0062](0062-pg-connector-tier1-tier2-connector-architecture.md).
