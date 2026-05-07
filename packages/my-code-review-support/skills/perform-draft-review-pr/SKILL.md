---
name: perform-draft-review-pr
description: "Perform a comprehensive code review of a PR using parallel subagents for code changes, commit structure, and PR organization. Sends review findings to mayor inbox instead of posting directly to GitHub. Use this skill whenever the user wants to review a PR, run a code review, or check PR quality. Requires Task tool (max-mode) for subagent orchestration."
---

# Perform Draft Review PR

Perform a comprehensive code review of a Pull Request using the subagent orchestration pattern.

## When to Use

Use this skill when proactively reviewing your OWN PR before submission. To react to feedback others have left on a PR, use `check-my-pr` instead.

See `agents/review-orchestrator.md` for the full workflow details.

## Usage

```
/perform-draft-review-pr <PR_IDENTIFIER>
```

**PR Identifier** can be:

- PR number: `12345` or `#12345`
- PR URL: `https://github.com/OWNER/REPO/pull/12345`
- Branch name: `phillipg.DE-123.feature-x`
- Title search: `Add feature X`

## Requirements

**This skill requires the Task tool (max-mode).**

Before proceeding, verify the Task tool is available. If not, display the error below and STOP.

## References

Read this file for tool usage and error handling:

- **Common reference**: `references/common.md`

## Instructions

### Step 1: Verify Task Tool Availability

Check if you can invoke the Task tool:

```
If Task tool is available:
  - Proceed with Step 2

If Task tool is NOT available:
  - Display error message (see below)
  - STOP - do not proceed
```

**Error message to display**:

```
Task tool is not available. The /perform-draft-review-pr skill requires max-mode to spawn review subagents. Enable max-mode and try again.
```

### Step 2: Spawn Review Orchestrator

If the Task tool is available, spawn the `review-orchestrator` subagent:

```
Task(
  subagent_type="review-orchestrator",
  prompt="Review PR <PR_IDENTIFIER>"
)
```

The `review-orchestrator` will handle the complete workflow:

1. Run `my-code-review-support-cli setup <PR>`
2. Spawn `review-code-changes`, `review-pr-structure`, and `review-jira-alignment` subagents in parallel
3. Combine JSON results from all subagents (JIRA alignment errors are reported but don't fail the review)
4. Save combined JSON to `/tmp/pr-review-<PR>-<timestamp>.json` and mail findings to `mayor/` inbox
5. Run `my-code-review-support-cli cleanup <worktree_path>`
6. Return summary

See `references/common.md` for error handling patterns.

### Step 3: Display Summary

Display the summary (markdown format) returned by the orchestrator. The summary structure is documented in `agents/review-orchestrator.md`.
