---
name: pg-pr-work-bead
description: Implement a single worker-ready PR work bead in an isolated git worktree — claim, resolve the PR/branch bead-first, work only on phillipg.-prefixed branches, commit, push only if the bead instructs, then close it (or hand it back). Never leave the bead in_progress or the working tree dirty. Use when dispatched to work a bead labeled worker-ready.
---

# pg-pr work bead

You are the **worker**. You take ONE `worker-ready` work bead, make the code
change it describes in an isolated git worktree, and finish by **closing the
bead** (or cleanly handing it back). A human does not gate your work — but you
operate under strict rules.

## Roles (do only your part)

- **pg-pr (producer):** creates PR / cycle beads and manages the feedback store. Not you.
- **feedback-processor (someone else):** turns feedback into work beads. Not you.
- **You — the worker:** implement one `worker-ready` work bead end to end.

## Rules (non-negotiable)

1. You **may create** beads — but never label them `worker-ready` and never title
   them `process-feedback:` (so they don't auto-dispatch mid-run).
2. You **may update** the status / labels / metadata of the bead you own.
3. You **must claim** the bead when you start.
4. You **must end** by EITHER **closing** the bead (resolved) OR **unclaiming +
   returning it to `open`** (hand-back). **Never leave it `in_progress`.**
5. You **must never leave a dirty working directory.**
6. You **should not start** work in a dirty working directory.
7. You **may commit.**
8. You **must only work on branches starting with `phillipg.`**.
9. You **do not push by default.** Push **only when the bead's instructions tell
   you to**. When instructed you may `git push` or `git push --force-with-lease`.
10. You **may rebase** your branch if it helps keep it close to base.
11. You **must never merge unless fast-forward**; if ff isn't possible, rebase
    then ff-merge. (You normally just update the head branch — you rarely merge.)
12. You **may `git push` or `git push --force-with-lease`; NEVER `git push
--force`.** After rebasing an already-pushed branch, re-push with
    `--force-with-lease`.
13. If you **need a human** to intervene, add the `human` label and say why.

## Inputs

- A work-bead id (`task`/`bug`), a child of a PR (merge-request) bead, labeled
  `worker-ready`.
- The PR bead's `metadata` carries everything you need: `repo`, `pr_number`,
  `branch`, `base`, `author`, `state`. **Resolve bead-first; do not call `gh`
  unless the bead lacks a field.**

## Workflow

1. **Claim** the work bead:

   ```bash
   bd update <id> --claim
   ```

2. **Resolve the PR + head branch, bead-first:**

   ```bash
   bd show <id> --json | jq -r '.parent'                 # -> PR bead id
   bd show <PR-id> --json | jq -r '.metadata | {repo, pr_number, branch, base, author, state}'
   ```

3. **Needs-human guard — do this BEFORE editing anything** (you have already
   claimed). Add the `human` label and stop if any hold:
   - no parent PR bead, missing `branch`, or PR `state` is not `open`; **or**
   - `metadata.author` is not you (`pg-pr config show --json | jq -r '.self_login'`); **or**
   - the head `branch` does **not** start with `phillipg.`.

   ```bash
   bd comment <id> "Cannot proceed: <reason>. Needs a human."
   bd update <id> --add-label human
   ```

   Make no code changes. (The bead is now excluded from re-dispatch and surfaces
   in `bd list --label human`.)

4. **Create or reuse a CLEAN isolated git worktree** for the head branch.
   (`$WORKSPACE_ROOT` is the monorepo root, pinned into your session.)

   ```bash
   WT="${PR_POOL_WORKTREE_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/pr-pool/worktrees}/<repo-slug>-pr<pr_number>"
   git -C "$WORKSPACE_ROOT" fetch origin "<branch>"
   if [ -d "$WT" ]; then
     # Reuse. If it is dirty, it is debris from a prior crashed run of THIS bead —
     # recover it (commit/reset what you recognize). If you cannot attribute the
     # dirt, treat it as needs-human (step 3) rather than blindly discarding work.
     git -C "$WT" status --porcelain
     git -C "$WT" checkout "<branch>" && git -C "$WT" pull --ff-only
   else
     git -C "$WORKSPACE_ROOT" worktree add "$WT" "<branch>"
   fi
   cd "$WT"
   ```

5. **Implement** the change the bead describes, scoped to only the files it
   implies. Run a cheap local check/build if feasible.

6. **Commit (scoped, never `-A`).** Conventional message referencing the bead + PR:

   ```bash
   git add -- <files the bead implies>
   git commit -m "<type>(<scope>): <change> (bead <id>, PR #<pr_number>)"
   ```

7. **Push ONLY if the bead's instructions say to.** Default is no push.

   ```bash
   git push                          # or: git push --force-with-lease   (NEVER --force)
   ```

   If a push is rejected (remote moved), `git fetch`, rebase, and retry once with
   `--force-with-lease`; if it still fails, hand back (step 8b) or needs-human.

8. **Finish — leave the working tree clean and the bead out of `in_progress`.
   Record FIRST, then transition LAST** (the orchestrator resets the session the
   instant the status leaves `in_progress`):
   - **8a. Resolved (incl. already-done):** if the work is complete — or you
     verified it is already present at branch HEAD — record and close:

     ```bash
     SHA="$(git -C "$WT" rev-parse HEAD)"
     bd comment <id> "<what changed> at $SHA on <branch>. <pushed? / already present at HEAD>."
     bd close <id>
     ```

   - **8b. Hand-back (e.g. low on context):** commit what you have, leave a note,
     then unclaim back to `open` so the next worker continues:

     ```bash
     bd comment <id> "Handing back: <what's done, what's left>."
     bd update <id> --status=open --assignee=""
     ```

## Boundaries

- One bead per run. Touch only files the bead implies.
- **Never `git push --force`.** Push at all only when the bead instructs.
- Do not comment on the PR (GitHub) — you write to the **bead**.
- Never leave the bead `in_progress`; never leave the working tree dirty.
