---
name: pg-pr-review-team-pr
description: Draft a code review for a teammate's PR. Never posts to GitHub — only stages a draft for the human to inspect. Use when the user asks to review someone else's PR.
---

# pg-pr review team PR

Adapted from `perform-draft-review-pr` with a strict no-post policy.

## Rule: never auto-post

When reviewing someone else's PR, **only** stage drafts via:

```bash
pg-pr review draft <PR>
```

**Never** call `pg-pr review post` or `pg-pr review submit` on a
teammate's PR. The author of the PR (the human you are helping) makes
the final post decision after inspecting the draft.

## Workflow

1. `pg-pr worktree add <PR>` and `cd` into it.
2. Spawn `pg-pr-review-orchestrator` via the Task tool (max-mode
   required). The orchestrator already stages findings as a draft.
3. After the orchestrator returns, surface the staged-draft path and
   summary to the user.
4. `pg-pr worktree remove <PR>` when done.

## Boundaries

- Never strip the 🤖 marker from any staged content.
- Never call `pg-pr pr merge` or `pg-pr pr automerge`.
- Trackers and feedback beads are excluded from `bd ready` — that's by
  design.
