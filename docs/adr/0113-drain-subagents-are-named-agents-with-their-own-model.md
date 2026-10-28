# Drain subagents are named agents with their own model

**Status**: Accepted (decision D4 of the pg-router role model and effort change; work item `pg2-5ggo5o`)
**Date**: 2026-10-28
**Deciders**: the operator (Phillip), in an interactive session on 2026-10-28

## Context

`drain-one` named no `subagent_type` for its UNCURATED PATH implementer or its lander, so both ran as
`general-purpose` and inherited the model of the drain session that dispatched them. Choosing a drain
role's model therefore also chose its subagents' model.

## Decision

1. A drain session's model and effort are decoupled from its subagents'. Operator: "i want the
   decision of what role to use for the drain to focus on what the correct thing is for what the drain
   is doing. what model/effort is used by the subagent should not be conencted to that."
2. Two named agents in the `pb` plugin fix model and effort in frontmatter: `pb:drain-implementer` and
   `pb:drain-lander`, both `model: claude-sonnet-5-5` and `effort: medium`, with no `tools:` line (both
   inherit the full tool set). Operator: "i'm ok with draining using sonnet medium for subagents" and
   "i don't like drain-one being opus high. i feel it is goinog to try to do too much."
3. `drain-one` dispatches each by name for every first dispatch and re-dispatch, through one rule in
   `references/rules.md` ("Which agent receives a drain dispatch"). No dispatch passes `model` or
   `effort`.
4. No fallback (decision D9, operator: fix forward): an unavailable agent type fails the dispatch and the
   bead goes to `pb:drain-stuck`.
5. A class of work that needs more gets its own agent definition plus a rule in that section. Operator:
   "if there is a class of work which needs something more, then we should define a subagent and update
   the rules to use it."

## Consequences

- Interactive `/drain-beads` applies `drain-one` too, so its subagents switch to Sonnet `medium` as well.
- The lander runs at `medium`.
- The pinned model ID in both agents needs an edit on a model release; a bats check
  (`test-pb-drain-subagents`) requires the two `model:` lines to be identical, and the pa-monitor
  model-table runbook has a "pinned role models" step for the bump.
- Unknown frontmatter keys are ignored silently, so a post-apply live check counts `agentType` in
  subagent metadata.
