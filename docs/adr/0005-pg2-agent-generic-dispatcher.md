# pg2-agent: Generic Rename of zr-agent

**Status**: Accepted
**Date**: 2026-05-01
**Deciders**: phillipg

## Context

The priority-based AI agent dispatcher was named `zr-agent` and lived in
`phillipg-nix-ziprecruiter`. The name implied ZR ownership, but the dispatcher
logic is fully generic: register agents by priority, try them in order, propagate
exit codes.

## Decision

Rename to `pg2-agent` (personal/generic v2 agent) with option namespace
`phillipgreenii.programs.pg2-agent`. The module and its scripts move to
`phillipgreenii-nix-agent-support/darwin/modules/pg2-agent/`. ZR no longer needs
its own agent registration module — the claude-code darwin module in agent-support
auto-registers the claude agent when `claude.enable = true`.

## Consequences

### Positive

Generic name reflects actual scope. Lives in the correct repo. ZR can add other agents
via `phillipgreenii.programs.pg2-agent.agents.*` without re-implementing the dispatcher.

### Negative

Users with muscle memory for `zr-agent` commands must update. Any external docs
or scripts referencing `zr-agent` need updating.

### Neutral

The dispatcher logic is unchanged — only the name and option namespace differ.
