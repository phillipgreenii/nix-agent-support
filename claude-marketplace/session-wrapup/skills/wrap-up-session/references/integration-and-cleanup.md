# Integration and cleanup mechanics

Detailed commands for the integrate (phase 5) and cleanup (phase 6) steps of
`wrap-up-session`. The SKILL.md decides *which* flow applies per repo; this file is *how* to
execute each one. Everything here is still bound by the session-scope rule — only run these
against repos this session worked on.

## Contents

- [Detecting the flow for a repo](#detecting-the-flow-for-a-repo)
- [Local ff-merge flow](#local-ff-merge-flow)
- [PR flow](#pr-flow)
- [Working directly on main](#working-directly-on-main)
- [Worktree teardown](#worktree-teardown)
- [Conflict handling](#conflict-handling)

## Detecting the flow for a repo

Run inside the repo (or its worktree). Decide in this order:

```bash
branch=$(git rev-parse --abbrev-ref HEAD)
# On main already? -> just push.
[ "$branch" = main ] && echo "flow=main"

# Open PR for this branch?
gh pr view --json number,state 2>/dev/null      # GitHub repos (nix-*)
pg-pr pr show 2>/dev/null                         # repos wired for pg-pr
# Or a pg-pr tracker bead:
bd list --type=merge-request 2>/dev/null | grep -i "$branch"
```

- On `main` → **main** flow.
- Any of the PR signals present → **PR** flow.
- Otherwise → **local ff-merge** flow.

When in doubt between PR and local, prefer the *less* destructive PR flow (push + open PR),
since it doesn't rewrite `main` or delete the branch. Then note the ambiguity in the summary.

## Local ff-merge flow

The branch's history is linear work you own; land it on `main` and retire it.

```bash
# 1. Make local main current. If this repo keeps main synced with a remote, fast-forward it
#    first; harmless to skip if it doesn't.
git fetch origin --quiet 2>/dev/null || true
git rebase main "$branch"        # rebase the branch onto local main; STOP on non-trivial conflict

# 2. Fast-forward main to the rebased branch. THIS ff-merge IS the integration — there is no push.
git checkout main
git merge --ff-only "$branch"    # fails loudly if not a fast-forward — that's a signal to stop

# 3. Retire the branch (cleanup phase). Worktree first if applicable, then:
git branch -d "$branch"          # -d (not -D): refuses if not merged, a safety net
```

Do **not** push `main`. For a merge-to-main repo, the ff-merge is the whole integration;
publishing `main` to a remote isn't part of wrapup (and pushing the default branch is exactly
what a harness guard will block). If `git merge --ff-only` refuses, the branch wasn't a clean
descendant of `main` — re-rebase onto `main`; if it still won't go, stop and hand off.

## PR flow

Push the branch and surface the PR; never merge.

```bash
git push -u origin "$branch"

# Prefer pg-pr where the repo is wired for it:
pg-pr sync                        # refresh merge-request beads
pg-pr pr show <n>                 # confirm state
# Open a PR if none exists (pg-pr or gh):
gh pr create --fill --draft       # only if there's genuinely no PR yet and the user expects one
```

Leave the branch and worktree in place — they're needed until the PR merges. A later wrapup
(after merge) will retire them via the local cleanup path. Record the PR number in the summary
and reference it from the next-session handoff bead.

## Working directly on main

No branch to merge, push, or retire — for a merge-to-main repo, committed work on `main` is
already integrated. Just confirm it's committed and gates passed (SKILL.md phases 3–4). If the
repo is actually PR-based, work shouldn't have landed on `main` directly — note it in the
summary rather than papering over it.

## Worktree teardown

Two kinds of worktree exist here; handle them differently.

### Standalone git worktree

```bash
git worktree remove <path>        # use --force only if you're certain it's clean + in scope
git worktree prune
```

### pn coordinated worktree set

A `pn` set is one branch materialized across *all* workspace repos at once, removed as a unit:

```bash
pn workspace worktree list                 # see the sets and their repos
pn workspace worktree remove <branch>      # removes the set across repos; does NOT delete branches
pn workspace worktree prune                # clear stale admin entries in each canonical repo
```

Critical: `pn workspace worktree remove` deliberately does **not** delete the underlying
branches — delete those per repo with `git branch -d` after the set is removed. And only
remove the set when **every** repo in it is clean and integrated. If one repo in the set still
has unmerged or dirty work, keep the whole set and note which repo blocks teardown — removing
it would strand that work.

## Conflict handling

A non-trivial rebase/merge conflict means the safe-to-automate window has closed:

- Do **not** force-resolve or take one side blindly.
- Abort the in-progress operation (`git rebase --abort`) so the branch is left intact and
  usable.
- Keep the branch + worktree.
- Roll it into the next-session handoff (SKILL.md phase 7): name the repo, the branch, and
  that a rebase onto `main` is pending with conflicts to resolve.

A *trivial* conflict (e.g. both sides added the same import, or a lockfile that regenerates)
may be resolved if you're confident, then continue the rebase. When unsure, treat it as
non-trivial and stop — a stopped wrapup is cheap; a wrong resolution on `main` is not.
