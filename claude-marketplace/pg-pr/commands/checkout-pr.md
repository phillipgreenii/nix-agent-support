---
name: checkout-pr
description: Materialise a worktree for a PR and cd into it.
---

# Checkout PR

Create a local worktree for a Pull Request so you can read, run, or
amend the changes without disturbing your current workspace.

## Usage

```
/checkout-pr <PR_IDENTIFIER>
```

## Workflow

1. Resolve the PR if it isn't already a number:
   ```bash
   REPO=$(pg-connector scm branch detect | jq -r '.result.repo')
   pg-connector pr show "$REPO#<PR_IDENTIFIER>" | jq '.result'
   ```
   (`pg-connector pr show` is a targeted, id-keyed op — `<owner/repo>#<number>`
   — with no cwd auto-detect of its own, unlike the now-retired pg-pr view
   command; `pg-connector scm branch detect` supplies the repo half of that
   id.)
2. Create the worktree:
   ```bash
   pg-pr worktree add <PR_NUMBER>
   ```
3. Print the path so the user (or a wrapping skill) can `cd` into it:
   ```bash
   pg-pr worktree list --json | jq -r '.[] | select(.pr_number == <N>) | .path'
   ```

To clean up after, run:

```bash
pg-pr worktree remove <PR_NUMBER>
```
