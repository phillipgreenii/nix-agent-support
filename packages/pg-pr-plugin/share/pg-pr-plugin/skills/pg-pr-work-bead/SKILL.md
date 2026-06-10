---
name: pg-pr-work-bead
description: Implement a single worker-ready PR work bead in an isolated git worktree — claim, resolve the PR/branch bead-first, implement, commit (do NOT push), record the worktree path + SHA on the bead, and swap worker-ready→needs-push for human review. Use when dispatched to work a bead labeled worker-ready.
---

# pg-pr work bead

You are the **worker**. You take ONE `worker-ready` work bead, make the code
change it describes in an isolated git worktree, and **commit but do not push** —
a human reviews and pushes. You do not triage feedback and you do not close the
bead.

## Roles (do only your part)

- **pg-pr (producer):** creates PR / cycle / feedback beads. Not you.
- **feedback-processor (someone else):** turns feedback into work beads. Not you.
- **You — the worker:** implement one `worker-ready` work bead, commit (no push),
  and hand it back for review.

## Inputs

- A work-bead id (`task`/`bug`), a child of a PR (merge-request) bead, labeled
  `worker-ready`.
- The PR bead's `metadata` carries everything you need to find the branch:
  `repo`, `pr_number`, `branch`, `base`, `author`, `state` (verified present on
  merge-request beads). **Resolve bead-first; do not call `gh` unless the bead
  lacks the field.**

## Workflow

1. **Claim** the work bead:

   ```bash
   bd update <id> --claim
   ```

2. **Resolve the PR + head branch, bead-first.** Walk to the parent PR bead and
   read its metadata:

   ```bash
   bd show <id> --json | jq -r '.parent'                 # -> PR bead id
   bd show <PR-id> --json | jq -r '.metadata | {repo, pr_number, branch, base, author, state}'
   ```

3. **Abort safely if you cannot proceed** — do this BEFORE editing anything,
   because you have already claimed the bead:
   - no parent PR bead, missing `branch`, or PR `state` is not `open`; **or**
   - `metadata.author` is **not** you (`pg-pr config show --json | jq -r '.self_login'`).

   On abort: make no code changes, add a one-line `bd comment <id>` explaining
   why, and stop. (The orchestrator will mark the bead `worker-stuck`; the human
   inspects `bd list --label worker-stuck`.) The triager trusted the
   `worker-ready` label — this author check is your own safety net so a mislabel
   never commits to someone else's branch.

4. **Create or reuse an isolated git worktree** for the head branch. `git
worktree add` refuses a branch already checked out elsewhere, so reuse an
   existing worktree rather than re-adding:

   (`$WORKSPACE_ROOT` is the monorepo root, pinned into your session by the orchestrator.)

   ```bash
   WT="${PR_POOL_WORKTREE_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/pr-pool/worktrees}/<repo-slug>-pr<pr_number>"
   git -C "$WORKSPACE_ROOT" fetch origin "<branch>"
   if [ -d "$WT" ]; then
     git -C "$WT" checkout "<branch>" && git -C "$WT" pull --ff-only
   else
     git -C "$WORKSPACE_ROOT" worktree add "$WT" "<branch>"
   fi
   cd "$WT"
   ```

5. **Implement** the change the bead describes, scoped to only the files it
   implies. Run a cheap local check/build if one is feasible and fast.

6. **Commit — never push, never force.** Conventional message referencing the
   bead and PR:

   ```bash
   git add -- <files the bead implies>          # stage only what this bead changed, not -A
   git commit -m "<type>(<scope>): <change> (bead <id>, PR #<pr_number>)"
   ```

7. **Record, then signal (order matters).** First record where the work is, then
   swap the labels. The orchestrator watches for `needs-push` and resets the
   session the instant it appears, so the comment MUST land first:

   ```bash
   SHA="$(git -C "$WT" rev-parse HEAD)"
   bd comment <id> "Committed $SHA on branch <branch> in worktree $WT (unpushed). Ready for review + push."
   bd update <id> --add-label needs-push --remove-label worker-ready
   ```

   Leave the bead **claimed / in_progress** — do **not** close it.

## Boundaries

- One bead per run. Touch only files the bead implies.
- **Never `git push`, never `--force`.** A human reviews and pushes.
- Do not comment on the PR (GitHub) — you write to the **bead**, not the PR.
- Do not close the bead; `needs-push` is the human's review queue.
