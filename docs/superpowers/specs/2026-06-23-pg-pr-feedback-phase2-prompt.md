# Prompt — pg-pr feedback: Phase 2 (deferred workflow + follow-ups)

> Paste this to kick off the next session. The **storage move is done and merged to `main`**.

## Context (what's already shipped)

The pg-pr feedback-datastore **storage move** is complete (see
`docs/superpowers/specs/2026-06-23-pg-pr-feedback-datastore-design.md` and
`docs/superpowers/plans/2026-06-23-pg-pr-feedback-datastore.md`). On `main`:

- PR feedback lives in a pg-pr-owned **SQLite store** (`internal/store`), not beads. Beads hold only actionable work (the PR bead + the process-feedback bead).
- In-process **event dispatcher** + **transactional outbox**; `internal/beadsbridge` projects beads from events.
- Ingestion (`internal/sync/ingest.go`) writes feedback rows, groups review-thread comments into one row with `code_comment_message` context, and records ownership (mine/team), author classification (human/agent/`is_ours`-by-marker), per-revision CI history + staleness.
- Reply delivery via `internal/replyposter` (store-backed, idempotent, ownership-gated).
- Agent CLI: `pg-pr feedback list/show/disposition`; `pg-pr migrate-feedback` (closes legacy feedback beads).
- HTML marker + dual-match `IsOurs`; config-driven `internal/agentregistry` classifier (ZR bot entries generated in `phillipg-nix-ziprecruiter`).

These primitives (store API, event/outbox, classifier, marker, CLI) are the foundation to build on.

## Phase 2: deferred workflow features (the original spec's Non-Goals)

These are the substantive workflow layer — **start with `superpowers:brainstorming`** to design them, then spec → plan → implement.

1. **Diff-review generation.** Agents review the PR diff and produce a _draft review_ (same process for mine + teammate). Teammate PRs → stage a GitHub **pending review** for the human to submit, plus a human attention bead + dashboard signal. My PRs → review findings feed the merge loop internally (not posted). Entry point: a per-PR `draft-review` bead. Enrichment (below) selects which reviewer agents run.
2. **PR enrichment.** Compute `kind` (bugfix/feature/refactor/…), `languages`, `size`, `urgency` on the `pull_request` row; use it to pick reviewer agents.
3. **Mine-vs-teammate split + teammate attention signals.** A human-facing bead per teammate PR that OPENS when action is needed (draft review ready & nobody approved; or new changes landed after my approval → re-review) and CLOSES when not (someone approved / I submitted), plus a dashboard signal. Keys off approval state + `reviewed_at_sha` vs `head_sha`.
4. **`revision` table.** Timeline of observed head SHAs per PR with per-revision CI summary + "did I review this SHA," to drive re-review-after-approval cleanly (the storage move deferred this; `head_sha`/`subject_sha`/`is_outdated` cover the current scope).

## Engineering follow-ups (surfaced by the storage-move reviews)

Bounded tasks; can be a short plan or folded into the above.

5. **beadsbridge PR-lifecycle handlers are dead in production.** Only `feedback.created` is emitted; `pr.opened/updated/closed/merged` are never enqueued, so the bridge's `EnsureMergeRequest`/`cascadeClose` paths run only in tests — the merge-request (PR) bead is still created/closed by the **inline** path in `internal/sync/sync.go`. Spec "Decision A" (the beads handler projects the PR bead from events) is only half-realized. Decide: emit PR-lifecycle events from sync into the outbox so the bridge owns the PR bead, OR remove the unused handler branches. (Touches `internal/sync` + `internal/beadsbridge`.)
6. **`Summary.RepliesPosted` is never incremented.** `replyposter.Reconcile` returns only an error, no count, so the summary line never reflects posted replies. Wire a count (changes `Reconcile`'s signature).
7. **Dead feedback-bead readers in `pkg/beads`.** `PRFeedbackInSubtree` (`deptree.go`), `FeedbackUnder`/`FindFeedbackForPR` (`tickcache.go`) have only test callers now that `processFeedback` no longer reads feedback beads. Remove safely — non-trivial because the feedback parse is interleaved with the live merge-request/cycle `LoadTickCache` pass.
8. **`code_comment_message.posted_at` is empty.** `api.Comment` has no timestamp, so message ordering is by insertion id. If a timestamp is added to `api.Comment` (GraphQL `createdAt` is available in the thread-comments selection), wire `PostedAt` through ingestion.

## Notes

- Branch off `main` in `phillipgreenii-nix-agent-support`; simple branch name (personal repo convention).
- No beads track this effort yet — create them (`bd`) for Phase 2 if you want session-spanning tracking.
- The companion ZR config lives in `phillipg-nix-ziprecruiter` `machines/phillipg-mbp-02/default.nix` (the `agents:` block).
