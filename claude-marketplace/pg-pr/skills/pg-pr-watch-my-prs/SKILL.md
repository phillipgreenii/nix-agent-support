---
name: pg-pr-watch-my-prs
description: Refresh the user's PR state and surface ready work. Use whenever the user asks "what's open?", "any new PRs?", "what should I work on next?", or otherwise about their pull requests.
---

# pg-pr watch my PRs

Passively keeps the user's PR state in sync with bd.

## When to use

- "What PRs do I have open?"
- "Any new review comments?"
- "What's ready to work on next?"

## Workflow

1. Refresh merge-request beads from upstream:

   ```bash
   pg-pr sync
   ```

2. List open merge-request beads:

   ```bash
   bd list --type=merge-request --status=open
   ```

3. List ready work:

   ```bash
   bd ready
   ```

   (Note: merge-request beads themselves are excluded from
   `bd ready` by design — surface them separately.)

4. Summarise to the user: which PRs need attention, which feedback
   beads are ready, and which CI runs (Phase 3) are red.
