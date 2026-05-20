---
name: perform-draft-review-pr
description: Perform a comprehensive code review of a PR using parallel subagents. Stages findings via pg-pr review draft for human inspection. Requires Task tool (max-mode).
---

# Perform Draft Review PR

Perform a comprehensive code review of a Pull Request using the
subagent orchestration pattern.

## When to Use

Use this command to proactively review your **own** PR before
submission. To react to feedback others have left on a PR, use
`check-my-pr` instead.

## Usage

```
/perform-draft-review-pr <PR_IDENTIFIER>
```

PR identifier formats:

- PR number: `12345` or `#12345`
- PR URL: `https://github.com/OWNER/REPO/pull/12345`
- Branch name

## Requirements

**This command requires the Task tool (max-mode).** If the Task tool
is not available, display:

```
Task tool is not available. The /perform-draft-review-pr command
requires max-mode to spawn review subagents. Enable max-mode and try
again.
```

…and STOP.

## Workflow

### Step 1: Verify Task tool

If the Task tool is unavailable, show the error above and STOP.

### Step 2: Spawn pg-pr-review-orchestrator

```
Task(
  subagent_type="pg-pr-review-orchestrator",
  prompt="Review PR <PR_IDENTIFIER>"
)
```

The orchestrator will:

1. `pg-pr worktree add <PR>` to materialise the review worktree.
2. Spawn the three review subagents in parallel.
3. Combine results and `pg-pr review draft <PR>` to stage them.
4. `pg-pr worktree remove <PR>`.
5. Return a summary.

### Step 3: Display summary

Show the markdown summary returned by the orchestrator. Remind the
user that the draft is staged — **nothing has been posted to
GitHub**. They can post explicitly via `pg-pr review post <PR>`.
