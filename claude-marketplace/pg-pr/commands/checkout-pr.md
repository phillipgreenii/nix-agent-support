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
   pg-pr pr view <PR_IDENTIFIER> --json
   ```
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
