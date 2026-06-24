# Prompt — pg-pr feedback: Phase 2 continuation (after #5 / #6)

> Paste this to kick off the next session. Phase 2 item **#5 (beadsbridge event
> ownership)** and follow-up **#6 (replies count)** are **DONE**, merged to local
> `main`, deployed, and verified live in the running daemon.

## Context (shipped since the last prompt)

**#5 — beadsbridge PR-lifecycle event ownership (Decision A, approach A2):**
The store `pull_request` row is now authoritative and `internal/beadsbridge` is
the **sole** writer of the PR (merge-request) bead. `sync`/`refresh` **emit**
`pr.opened/updated/closed/merged`; the bridge projects the bead at outbox flush.
All inline `EnsureMergeRequest`/`CloseMergeRequest`/`cascadeClose` were removed
from the engine. Specifics:

- Enriched `store.PRPayload` (bridge writes full `MergeRequestFields`, not the old
  degraded `{Repo, PRNumber}`); emit helpers in `internal/sync/prevents.go`
  (`emitPREvent`, `emitPRClosed`, `prToStoreRow`).
- `store.ListOpenPRs` drives close-detection from the store (marks the row closed
  before emitting `pr.closed`, so it can't re-fire every tick).
- No-resurrection guard: the feedback handler skips a cycle when the parent PR
  bead is closed.
- **Load-bearing invariant:** a PR's `pr.opened`/`pr.updated` is committed before
  that PR's `feedback.created` (same goroutine, sequential), so the FIFO outbox
  always projects the PR bead before the feedback handler runs. Proven by a
  concurrent-`RunOutbox` test.
- Counting is emit-time, scoped to the one-shot `Sync` (the only Summary
  producer); daemon emits no per-tick Summary. Store/Dispatch are now required
  for the PR bead (nil path is test/legacy, degrades safely).

**#6 (folded into #5):** `replyposter.Reconcile` returns `(int, error)`;
`Summary.RepliesPosted` is wired through every `reconcileReplies` call site.

**Merge / deploy / verification:**

- Merged to **local `main`** (commit `33e4d89`, now an ancestor of `main`); not
  pushed to origin, per repo convention.
- Deployed: the launchd daemon `org.nixos.pg-pr-sync` runs the new binary
  (`pg-pr 0.0.0-4feea9fa`). Verified live — 15 `merge-request` beads projected
  into the ZR workspace with full fields, process-feedback cycles attached under
  them, **0 sync errors**, and the `"no merge-request bead"` ordering-failure mode
  **never fired**.

**Docs:**

- Design: `docs/superpowers/specs/2026-06-24-pg-pr-feedback-beadsbridge-event-ownership-design.md`
- Plan: `docs/superpowers/plans/2026-06-24-pg-pr-feedback-beadsbridge-event-ownership.md`
- Roadmap + dependency order: `docs/superpowers/plans/2026-06-23-pg-pr-feedback-phase2-roadmap.md`
- Original Phase 2 prompt: `docs/superpowers/specs/2026-06-23-pg-pr-feedback-phase2-prompt.md`

## Epic state — `pg2-4c5i`

Run `bd show pg2-4c5i`, `bd ready`, and `bd prime` first. **Closed:** `pg2-4c5i.9`
(#5), `pg2-4c5i.14` (#6).

### Remaining workflow features (P2) — each gets its own brainstorm → spec → plan → implement cycle

Build order: **`{#2, #4} → #1 → #3`**.

| Bead          | Feature                                                               | Status            |
| ------------- | --------------------------------------------------------------------- | ----------------- |
| `pg2-4c5i.10` | **#2 PR enrichment** (kind/languages/size/urgency; picks reviewers)   | **ready** (root)  |
| `pg2-4c5i.11` | **#4 revision table** (head-SHA timeline + per-rev CI + did-I-review) | **ready** (root)  |
| `pg2-4c5i.12` | **#1 diff-review generation** (agents review diff → draft review)     | blocked by #2     |
| `pg2-4c5i.13` | **#3 mine-vs-teammate split + attention signals**                     | blocked by #4, #1 |

### Independent engineering cleanups (P3, ready, bounded)

| Bead          | Work                                                                      |
| ------------- | ------------------------------------------------------------------------- |
| `pg2-4c5i.15` | **#7** remove dead feedback-bead readers in `pkg/beads`                   |
| `pg2-4c5i.16` | **#8** populate `code_comment_message.posted_at` from GraphQL `createdAt` |

### #5 follow-ups surfaced in review (P3, ready, small)

| Bead          | Work                                                                                                                                                                    |
| ------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `pg2-4c5i.17` | make PR state-change + lifecycle event **atomic** (single `InTx`) — narrow correctness gap (close-path event can be lost on crash; does not self-heal). **Prioritize.** |
| `pg2-4c5i.18` | slim the now-mostly-unused engine `BeadClient` interface                                                                                                                |
| `pg2-4c5i.19` | `noopBeadClient` base to cut 6-fake duplication in `bridge_test.go`                                                                                                     |
| `pg2-4c5i.20` | `ListOpenPRs` test should cover `draft` state                                                                                                                           |
| `pg2-4c5i.21` | `ListOpenPRs` scan error should include repo context                                                                                                                    |

## Recommended start

Begin **#2 (enrichment, `pg2-4c5i.10`)** or **#4 (revision table, `pg2-4c5i.11`)** —
both are ready roots that unblock the headline diff-review/attention features.
Of the small items, do **`pg2-4c5i.17`** early (it's a real, if narrow,
correctness gap, not just polish).

## Notes / conventions

- **Branch off `main`** in `phillipgreenii-nix-agent-support`; simple branch name
  (personal-repo convention, not the ZR `phillipg.TICKET.desc` format).
- **`bd` for all tracking** — run `bd prime`. Dolt auto-commit is **off**:
  `bd dolt commit` at checkpoints to persist bead changes.
- Go module: `packages/pg-pr`. Verify with `go test ./...`; the repo's formal gate
  is `nix flake check` (+ `prek run --all-files`). Pre-commit `treefmt` reformats
  markdown — if a commit aborts on reformat, `git add -A` the same files and
  re-commit.
- **Pattern to follow:** the bridge is now the sole PR-bead writer. New workflow
  beads (#1's per-PR `draft-review` bead, #3's teammate-attention bead) should
  follow the same **emit → bridge-project** model — emit a new event from
  `sync`/`refresh` (see `internal/sync/prevents.go`) and add a handler branch in
  `internal/beadsbridge/bridge.go`. Respect the ordering invariant (project the
  parent bead before any child event for the same PR).
- The companion ZR config (bot identities / `agents:` block) lives in
  `phillipg-nix-ziprecruiter` `machines/phillipg-mbp-02/default.nix`.
