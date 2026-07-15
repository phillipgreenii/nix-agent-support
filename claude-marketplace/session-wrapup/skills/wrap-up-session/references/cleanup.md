# Cleanup mechanics

Detailed commands for the cleanup step (phase 6) of `wrap-up-session`. Integration
itself — detecting the method, rebasing, fast-forward merging, pushing,
opening/updating PRs, and retiring a _standalone_ worktree + branch on a local
ff-merge — is **not** here: it belongs to the `integrate-branch` skill that phase 5
invokes (its `ff-merge-to-main` / `pull-request` handlers). This file covers only the
cleanup `integrate-branch` does **not** own — chiefly the `pn` coordinated workforest
_set_ (a cross-repo unit) and session stashes. Everything here is still bound by the
session-scope rule — only act on what this session worked on.

## What `integrate-branch` already cleaned up

Phase 5 delegates landing to `integrate-branch`, so by the time you reach cleanup
each in-scope repo already got the retirement its outcome implies — do not repeat it:

- **`landed` (local ff-merge):** the `ff-merge-to-main` handler already removed that
  repo's standalone worktree, deleted the merged branch, and pruned. Nothing more per
  repo.
- **`pr-opened` / `pr-updated`:** the `pull-request` handler deliberately **kept** the
  branch and worktree — the work isn't merged. Leave them; a later wrapup retires them
  after the PR lands.
- **`stopped:<reason>`:** the branch and worktree are intact by design. Leave them and
  roll the reason into the handoff (phase 7).

## `pn` coordinated workforest set

A `pn` set is one branch materialized across _all_ workspace repos at once — a
cross-repo unit `integrate-branch` (which acts one repo at a time) does not manage, so
wrap-up coordinates its teardown. Tear a set down **only when every repo in it landed**
(`integrate-branch` reported `landed` for each); if any repo is a PR
(`pr-opened`/`pr-updated`) or `stopped:*`, keep the whole set and note which repo
blocks teardown — removing it would strand that work.

```bash
pn workspace workforest list                 # see the sets and their repos
pn workspace workforest remove <branch>      # remove the set across repos (if still listed)
pn workspace workforest prune                # clear stale set admin in each canonical repo
```

A `landed` ff-merge already retired that repo's worktree and merged branch (via
`integrate-branch`'s `ff-merge-to-main` handler), so set teardown is mostly clearing
leftover admin: run `pn workspace workforest prune`, and
`pn workspace workforest remove <branch>` if the set is still listed. Only if a per-repo
branch was somehow left behind (it shouldn't be) delete it defensively with
`git branch -d <branch>` — don't undo or second-guess `integrate-branch`'s retirement.

## Stashes

Clear only stashes **this session** created. Pre-existing stashes are out of scope —
leave them and list them in the summary.
