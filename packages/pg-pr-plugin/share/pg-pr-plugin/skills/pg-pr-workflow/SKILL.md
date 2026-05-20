---
name: pg-pr-workflow
description: Primary rules for working with pull requests via the pg-pr CLI. Use when the user mentions PRs, code review, draft reviews, watching PRs, or processing PR feedback.
---

# pg-pr workflow

This skill is the rule carrier for any PR-related work driven by the
`pg-pr` CLI.

## Verbs always use the pg-pr CLI

Prefer `pg-pr` over ad-hoc `gh` invocations for anything PR-related:

- Discover: `pg-pr pr show <n>`, `pg-pr pr files`, `pg-pr pr commits`
- Worktree: `pg-pr worktree add|remove|list <n>`
- Sync: `pg-pr sync` (refreshes merge-request beads)
- Review: `pg-pr review draft|post|submit <n>`
- Comment: `pg-pr comment add|respond|resolve`

## Boundaries

- Agents never call `pg-pr pr merge` or `pg-pr pr automerge`. Merges
  are an explicit human decision.
- Trackers and feedback beads are excluded from `bd ready` by design.
  Surface them via `bd list --type=merge-request`.
- Author precedence when picking who's authoritative on a thread:
  `self > team_member > org_member > bot`.
- Every comment / review body posted by `pg-pr` carries the 🤖 marker.
  Do not strip it.
