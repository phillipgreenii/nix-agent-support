---
name: check-my-pr
description: Check a PR for new review feedback, triage it, and propose changes. Uses pg-pr verbs and bd merge-request beads.
---

# Check My PR

Check a Pull Request for new review feedback, triage it, and propose
changes.

## Usage

```
/check-my-pr <PR_IDENTIFIER>
```

PR identifier can be a number (`12345`, `#12345`), a URL, a branch
name, or omitted (uses the current branch's PR).

## Preconditions

- **Author identity**: Assumes the invoker is the PR author. Reviewer
  filtering in the summary depends on this.
- **Network access** required for `pg-pr` to hit the upstream API.

## Workflow

### Step 1: Resolve PR

```bash
pg-pr pr show <PR_IDENTIFIER> --json
```

Capture `number`, `state`, `author`. If no argument was provided and
`pg-pr` cannot auto-detect a PR for the current branch, ask the user
for a PR identifier.

### Step 2: Sync state

```bash
pg-pr sync --pr <NUMBER> --repo <OWNER/NAME>
```

This refreshes the `merge-request` bead for the PR. If the PR is now
`merged` / `closed`, the sync will mark the bead accordingly. STOP if
the PR is no longer open.

### Step 3: Gather comments

```bash
pg-pr pr show <NUMBER> --json    # confirms state
# Phase 3 will land 'pg-pr feedback gather' which materialises
# feedback beads. Until then, fetch comments via gh and stage them
# manually if needed.
```

> **Phase note**: full feedback gathering / triage / proposed-change
> bead lifecycle lands in Phase 3 (epic `beads_pg2-ywy`). For Phase 2
> this command surfaces the comments and prompts the user to triage
> them by hand.

### Step 4: Present summary

```
PR #<n> — <state>

## Reviewers
- <user>: <N> comments (most recent: <timestamp>)

## Action
<one-line guidance — e.g., "fetch comments and triage manually until
Phase 3 lands">
```
