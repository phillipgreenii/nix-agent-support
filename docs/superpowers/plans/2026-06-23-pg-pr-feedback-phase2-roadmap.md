# pg-pr feedback Phase 2 — Roadmap & Dependency Order

**Date**: 2026-06-23
**Epic**: `pg2-4c5i`
**Source spec**: `docs/superpowers/specs/2026-06-23-pg-pr-feedback-phase2-prompt.md`
**Storage-move foundation (shipped on `main`)**: `docs/superpowers/specs/2026-06-23-pg-pr-feedback-datastore-design.md`

## Purpose

Phase 2 is too large for a single spec. This roadmap decomposes it into the
Phase 2 epic's eight child beads, records the dependency order, and names the
sequence in which they should be brainstormed → spec'd → planned → implemented.
Each **feature** bead gets its own full `superpowers:brainstorming` →
`writing-plans` → implementation cycle; the bounded follow-ups can be a short
plan or folded into the feature they touch.

## What's already shipped (foundation)

The storage move is complete and merged to `main`:

- PR feedback lives in a pg-pr-owned **SQLite store** (`internal/store`), not
  beads. Beads hold only actionable work (the PR bead + the process-feedback
  bead).
- In-process **event dispatcher** + **transactional outbox**;
  `internal/beadsbridge` projects beads from events.
- Ingestion (`internal/sync/ingest.go`) writes feedback rows, groups
  review-thread comments into one row, records ownership, author
  classification, per-revision CI history + staleness.
- Reply delivery via `internal/replyposter` (store-backed, idempotent,
  ownership-gated).
- Agent CLI: `pg-pr feedback list/show/disposition`; `pg-pr migrate-feedback`.
- HTML marker + dual-match `IsOurs`; config-driven `internal/agentregistry`
  classifier.

These primitives (store API, event/outbox, classifier, marker, CLI) are the
foundation Phase 2 builds on.

## The eight beads

| Bead          | Item | Title                                               | Type    | Priority |
| ------------- | ---- | --------------------------------------------------- | ------- | -------- |
| `pg2-4c5i.9`  | #5   | beadsbridge PR-lifecycle event ownership (decide)   | task    | P2       |
| `pg2-4c5i.10` | #2   | PR enrichment (kind/languages/size/urgency)         | feature | P2       |
| `pg2-4c5i.11` | #4   | revision table (head-SHA timeline + per-rev CI)     | feature | P2       |
| `pg2-4c5i.12` | #1   | diff-review generation (agents review diff → draft) | feature | P2       |
| `pg2-4c5i.13` | #3   | mine-vs-teammate split + teammate attention signals | feature | P2       |
| `pg2-4c5i.14` | #6   | wire `Summary.RepliesPosted` count                  | bug     | P3       |
| `pg2-4c5i.15` | #7   | remove dead feedback-bead readers in `pkg/beads`    | chore   | P3       |
| `pg2-4c5i.16` | #8   | populate `code_comment_message.posted_at`           | bug     | P3       |

## Dependency graph

```
            ┌──────────────────────────────────────────────────┐
            │  #5 beadsbridge event ownership  (pg2-4c5i.9)      │  architectural root:
            │  decide: emit events vs remove dead handlers       │  settles how new
            └───────────────┬────────────────────────────────────┘  workflow beads project
                            │ (sets bead-emission pattern)
        ┌───────────────────┼─────────────────────────┐
        ▼                   ▼                          │
 ┌────────────────┐  ┌───────────────────┐             │
 │ #2 enrichment  │  │ #4 revision table │             │
 │ (pg2-4c5i.10)  │  │ (pg2-4c5i.11)     │             │
 └───────┬────────┘  └────────┬──────────┘             │
         ▼                    │ relates_to (per-rev CI)│
 ┌───────────────────────┐    │                        │
 │ #1 diff-review gen     │◄───┘ (soft)                 │
 │ (pg2-4c5i.12)          │◄───────────────────────────┘
 └──────────┬────────────┘
            ▼
 ┌─────────────────────────────────────┐
 │ #3 mine-vs-teammate attention        │ ◄── #4 (reviewed_sha vs head_sha)
 │ (pg2-4c5i.13)                        │
 └─────────────────────────────────────┘

 Independent quick wins (no blockers, do any time / ideally early):
   #6 RepliesPosted (.14)   #7 dead readers (.15)   #8 posted_at (.16)
```

**Hard blocking edges** (in beads):

- `#5 → #1`, `#5 → #3` — settle the bead-projection pattern before features emit
  new beads (the draft-review bead and the teammate-attention bead).
- `#2 → #1` — enrichment selects which reviewer agents run.
- `#4 → #3` — re-review-after-approval keys off `reviewed_at_sha` vs `head_sha`.
- `#1 → #3` — attention signals react to the draft review being ready.

**Soft edge** (`relates_to`, not blocking): `#4 ↔ #1` — the revision table's
per-revision CI summary informs diff-review but does not block it.

## Build order

```
#5  →  { #2, #4 }  →  #1  →  #3
```

with `#6`, `#7`, `#8` runnable in parallel at any point (do them early to keep
the foundation clean before layering workflow logic on top).

**First up: #5 (`pg2-4c5i.9`)** — the architectural root. It is a genuine design
decision ("emit `pr.opened/updated/closed/merged` into the outbox so the bridge
owns the PR bead" **vs** "remove the dead handler branches"), and it sets the
pattern the headline features (#1, #3) follow when they project their own beads.
Brainstormed first.

## Per-feature cycle

For each feature bead, in order:

1. `superpowers:brainstorming` → design doc in `docs/superpowers/specs/`.
2. `writing-plans` → implementation plan in `docs/superpowers/plans/`, with
   implementation sub-beads under the feature bead.
3. Implement (TDD), review, merge to `main`.

The follow-ups (#6/#7/#8) are bounded enough to skip straight to a short plan or
fold into the feature they touch.
