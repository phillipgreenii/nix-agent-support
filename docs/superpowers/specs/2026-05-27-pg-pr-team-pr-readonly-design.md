# pg-pr: scope team-mate PRs to read-only in sync

**Status**: Draft
**Date**: 2026-05-27
**Tracking**: beads_pg2-r3t0

## Problem

`pg-pr sync` is modifying PRs owned by team-mates. Reported symptoms on
team-mate PRs:

- Draft mode removed (promoted to ready-for-review).
- Labels added.
- Reviewers requested.

pg-pr should only ever modify PRs authored by the configured `SelfLogin`.
For team-mate PRs it should gather information (upstream → local beads)
and stage review drafts locally, never write upstream.

## Root Cause

In `packages/pg-pr/internal/sync/sync.go`:

- `enumerate()` (line 706) pulls both `ListMyPRs` and `ListTeamPRs` into a
  single `observed` pool.
- The pool is iterated in `Sync()` (line 316) and `maybePromoteDraft` is
  called on every PR (line 376) with no author check.
- `maybePromoteDraft` (line 1067) calls `SetDraft(ctx, repo, pr.Number,
false)` whenever CI is green and the PR is draft — regardless of
  ownership.
- `SyncPR()` (line 615) — the single-PR path used by `pg-pr sync --pr N`
  and `/check-my-pr` — has the same unguarded call (line 692).
- `processReplyDrafts()` (line 1129) iterates pending `ReplyDraft`
  feedback beads per repo and posts each via `ReplyToThread` upstream
  with no ownership guard on the parent merge-request.

The label-add and reviewer-request behavior is a **GitHub-side cascade**
triggered by `gh pr ready` (typical setups have a `pull_request:
ready_for_review` workflow that applies labels, plus CODEOWNERS
auto-requesting reviewers once a PR leaves draft). No direct
`AddLabels` / `RequestReviewers` calls exist in pg-pr's Go code. Fixing
the unconditional `SetDraft(false)` removes the trigger and the
cascades stop.

`ReplyToThread` is the more dangerous of the two writes: it uses
GitHub's `addPullRequestReviewThreadReply` GraphQL mutation, which
publishes immediately (no draft / pending state). A stray `ReplyDraft`
on a team-mate PR's feedback bead would post a live, public reply on
the next sync.

## Design

### Principle

Split the observed PR pool into `mine` and `team` immediately after
enumeration. The two paths have different rules; downstream code never
sees a mixed pool.

### Ownership predicate

```go
// isSelfAuthoredLogin reports whether the given GitHub login matches the
// configured SelfLogin. Empty self or empty login → false (assume
// team-mate; do not modify upstream).
func (e *Engine) isSelfAuthoredLogin(author string) bool {
    self := e.deps.Cfg.SelfLogin
    return self != "" && author != "" && author == self
}
```

Any uncertainty (missing config, empty author, lookup failure) resolves
to `false`. The conservative default is "do not modify."

### Per-PR phase matrix

| Phase                                                       | Side               | Mine | Team      |
| ----------------------------------------------------------- | ------------------ | ---- | --------- |
| `EnsureMergeRequest`                                        | local bead         | ✅   | ✅        |
| `processFeedback` (ingest comments + CI events)             | local bead         | ✅   | ✅        |
| `maybePromoteDraft` (`SetDraft(false)`)                     | **upstream write** | ✅   | ❌        |
| `processReplyDrafts` (`ReplyToThread`)                      | **upstream write** | ✅   | ❌ (warn) |
| `cascadeClose` / `CloseMergeRequest` (upstream-not-watched) | local bead         | ✅   | ✅        |

### Guard sites

1. **`Sync()` loop**, sync.go:316–385

   After the existing `observed` map is populated by enumerate, partition
   it into `mine` and `team` using `isSelfAuthoredLogin(pr.Author)`. Both
   subsets run `EnsureMergeRequest` and `processFeedback`. Only `mine`
   runs `maybePromoteDraft`.

   Structural separation, not just a guard inside `maybePromoteDraft`,
   so future write-side phases added to the loop must consciously
   decide which subset they apply to.

2. **`SyncPR()`**, sync.go:615–703

   Add the ownership check before the `maybePromoteDraft` call at line 692. The bead is still upserted, `processFeedback` still runs, the
   single-PR command still returns a useful summary. Only the upstream
   write is suppressed.

3. **`processReplyDrafts()`**, sync.go:1129–1213

   Inside the per-bead loop, after the existing `FindMergeRequestForFeedback`
   error check, the `mr == nil` orphan check, and the `mr.Fields.Repo !=
rcfg.Remote` cross-repo check (line 1161), add an ownership check on
   the parent merge-request. Skip any bead whose parent PR is not
   self-authored. **Emit a warning** when this happens (see Warnings
   below) — a `ReplyDraft` on a team-mate PR's feedback bead is a bug
   class, not a normal state, and the user needs to know about it.

   Ordering matters: orphans and cross-repo beads continue to use their
   existing handling (silent skip / not-our-repo). The ownership
   warning only fires when we have a valid parent merge-request for
   this repo and its Author is not self.

   The local `ReplyDraft` field stays put. The user can inspect, copy
   elsewhere, retarget, or delete it via existing bd / pg-pr verbs.

### Warnings channel

Add a new field on the existing `Summary` struct:

```go
type Summary struct {
    // ... existing fields ...
    Errors   []SummaryError
    Warnings []SummaryError   // new
}
```

`Warnings` reuses the `SummaryError` shape (`Repo`, `Message`) for
consistency. Semantic separation from `Errors`:

- `Errors`: sync attempted a documented action and it failed. Counts
  toward `telemetry.SyncErrorsTotal`, surfaces in
  `repoStates[].LastError`.
- `Warnings`: sync found local state that should not exist (per the
  ownership rules) and refused to act on it. Does NOT count toward
  error metrics or `LastError`. Surfaces in the summary output only.

Warning text format:

```
reply <feedback-bead-id> skipped: parent PR #<N> authored by "<author>"
(not self) — ReplyDraft should not have been staged
```

Includes feedback bead ID, parent PR number, parent PR author, and
(via the surrounding `SummaryError.Repo`) the repo. Enough for the
user to track down which agent or run staged the draft and clean up.

**Dedup behavior**: warning fires every sync until the offending
`ReplyDraft` is cleared. No state stored on the bead. Rationale: this
should be a rare event (an upstream bug). If it isn't rare, the
recurring noise is itself the signal that something needs fixing.

### Tests

In `internal/sync/sync_test.go`:

1. `Sync` with a mixed pool: configure `SelfLogin = "phillipg"`, list two
   draft + all-CI-green PRs (one authored by `phillipg`, one by
   `coworker`). Assert `SetDraft` is called exactly once with the self
   PR's number, and that both beads are upserted.

2. `SyncPR` on a team-mate's PR: configure `SelfLogin = "phillipg"`,
   call `SyncPR(ctx, "foo/bar", 99)` where the upstream PR is
   authored by `coworker` and draft+green. Assert bead is upserted with
   `Author=coworker`, and `SetDraft` is NOT called.

3. `processReplyDrafts` with one self + one team feedback bead pending:
   set `ReplyDraft` on both. Self PR is authored by `phillipg`, team PR
   by `coworker`. Assert `ReplyToThread` called exactly once (for the
   self bead), assert `SetResponseID` called exactly once (for the self
   bead), assert `summary.Warnings` contains one entry referencing the
   team bead, assert team bead's `ReplyDraft` and `ResponseID` are
   unchanged.

4. Empty `pr.Author`: a PR with empty author field is treated as team
   (no write). Assert no `SetDraft` call.

5. Empty `cfg.SelfLogin`: with no configured self-login, every PR is
   treated as team. Assert no `SetDraft` calls at all even if PRs are
   draft+green.

## Out of Scope

- Labels and reviewer-requests: GitHub-side cascades from undrafting.
  Removing the unconditional `SetDraft(false)` removes the trigger.
- The `pg-pr-review-team-pr` SKILL.md prompt rule (never call
  `pg-pr review post|submit` on a team-mate's PR) stays as a
  belt-and-suspenders guard at the agent layer. This spec adds the
  code-layer guard underneath it.
- `watch_labels` and other future enumeration modes — out of scope.
  The split happens by author against `SelfLogin`, not by membership in
  `TeamMembers`. A PR authored by anyone other than `SelfLogin` (team
  member, outside contributor, bot) is classified as "team" and lands
  in the read-only path.

## Risks

- **Empty-author / missing-self default**: defaulting to "team" means a
  PR whose `Author` field somehow fails to populate would never
  auto-promote. Acceptable trade-off — the conservative direction is
  not-modify.
- **Warning noise on persistent bug**: if a buggy agent keeps staging
  `ReplyDraft` on team-mate beads, the warning fires every sync. By
  design — the noise is the signal.
- **Existing team-mate beads with stale `ReplyDraft`**: any
  pre-existing `ReplyDraft` on a team-mate feedback bead will start
  warning immediately on first sync after deploy. The
  `pg-pr-review-team-pr` skill rule has been in place, so this should
  be rare, but worth checking before deploy.
