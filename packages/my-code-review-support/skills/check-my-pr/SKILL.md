---
name: check-my-pr
description: Check a PR for new review feedback, triage it, and propose changes. Resolves PR identifier, manages a PR tracker bead, gathers new feedback from all reviewers, and produces proposed changes.
---

# Check My PR

Check a Pull Request for new review feedback, triage it, and propose changes.

## Usage

```
/check-my-pr <PR_IDENTIFIER>
```

**PR Identifier** can be:

- PR number: `12345` or `#12345`
- PR URL: `https://github.com/OWNER/REPO/pull/12345`
- Branch name: `phillipg.DE-123.feature-x`
- Nothing: assumes the current branch's PR

## References

Read this file for bead conventions:

- **Bead conventions**: `references/feedback-bead-conventions.md`

## Preconditions

- **Author identity**: This skill assumes the invoker is the PR author. The summary in Step 6 filters the author out of the reviewer list; if you're not the author, that filtering will be incorrect.
- **Network access**: All `gh` CLI commands require network permissions. Request network access upfront before Step 1.
- **Subagent invocation**: This skill spawns subagents via the **Task tool** (the same mechanism `perform-draft-review-pr` uses). If the Task tool is not available, display an error explaining that max-mode is required and stop.

## Instructions

### Step 1: Resolve PR

Normalize the input to a PR number and title.

**If a PR number or URL was provided:**

```bash
gh pr view <PR_IDENTIFIER> --json number,title,state,author --jq '{number, title, state: .state, author: .author.login}'
```

**If no argument was provided:**

```bash
gh pr view --json number,title,state,author --jq '{number, title, state: .state, author: .author.login}'
```

The `author` field identifies the PR owner. This is used by downstream agents to interpret the author's responses to feedback (agreement, disagreement, emoji reactions) as signals about what work should or should not be done.

If this fails, inform the user that no PR is associated with the current branch.

### Step 2: Check if PR is Merged

If the PR state is `MERGED`:

1. Search for the tracker bead:

```bash
bd search "PR Tracker (#<PR_NUMBER>)"
```

2. If a tracker bead exists, close it and all children:

```bash
# Get all children
bd children <tracker-id>

# Close each child
bd close <child-id> --reason="PR #<PR_NUMBER> merged, closing with tracker"

# Close the tracker
bd close <tracker-id> --reason="PR #<PR_NUMBER> merged"
```

3. Output to user and STOP:

```
PR Tracker: <tracker-id> (#<PR_NUMBER>) — Closed (PR merged)
```

If no tracker bead exists, output:

```
PR #<PR_NUMBER> is merged. No tracker bead found.
```

STOP here — do not proceed to further steps.

### Step 3: Ensure PR Tracker Bead Exists

Search for an existing tracker:

```bash
bd search "PR Tracker (#<PR_NUMBER>)"
```

**If found:** use the existing tracker bead ID.

**If not found:** create one:

```bash
bd create --title="PR Tracker (#<PR_NUMBER>): <PR_TITLE>" --type=epic --priority=2
bd update <tracker-id> --claim
```

### Step 4: Spawn gather-pr-feedback Agent

Spawn the `gather-pr-feedback` agent with the tracker bead ID and PR author using the **Task tool**:

```
Task(
  subagent_type="gather-pr-feedback",
  prompt="Gather PR feedback for tracker bead <TRACKER_BEAD_ID>. PR author is <AUTHOR_LOGIN>."
)
```

Wait for the agent to complete, then parse the returned JSON (see `agents/gather-pr-feedback.md` for the output schema). If the output cannot be parsed as JSON, display the raw output and ask the user to investigate before continuing.

### Step 5: Spawn review-pr-feedback Agent

After gather completes, spawn the `review-pr-feedback` agent via the **Task tool** with the same tracker bead ID and PR author:

```
Task(
  subagent_type="review-pr-feedback",
  prompt="Review PR feedback for tracker bead <TRACKER_BEAD_ID>. PR author is <AUTHOR_LOGIN>."
)
```

Wait for the agent to complete, then parse the returned JSON (see `agents/review-pr-feedback.md` for the output schema). If the output cannot be parsed as JSON, display the raw output and ask the user to investigate before continuing.

### Step 6: Present Summary to User

Combine the results from both agents into a human-readable summary.

Per the Preconditions, the PR author is "you" — do not list the author in the feedback summary. Only list other reviewers' feedback. The author's own comments are responses to feedback, not feedback itself.

**When feedback was found and processed:**

```
PR Tracker: <tracker-id> (#<PR_NUMBER>) — Open

## Feedback Summary
- <user>: <N> comments (<M> actionable)
- <user>: <N> comments (<M> actionable)

## Proposed Changes
- <bead-id>: <summary>
- <bead-id>: <summary>

## Requires Human Review
- <bead-id> (P1): <summary> — <reason>
```

Only include the "Requires Human Review" section if there are elevated (P1) items (P1 = elevated priority; flagged because the change likely needs human discussion before implementation).

**When no new feedback was found:**

```
PR Tracker: <tracker-id> (#<PR_NUMBER>) — Open

No new feedback found.
No feedback was processed.
```

**When feedback was found but none was actionable:**

```
PR Tracker: <tracker-id> (#<PR_NUMBER>) — Open

## Feedback Summary
- <user>: <N> comments (0 actionable)

No feedback was processed.
```
