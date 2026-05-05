# claude.enable as Master Gate for All Claude Tooling

**Status**: Accepted
**Date**: 2026-05-01
**Deciders**: phillipg

## Context

Claude Code and its associated tooling (settings, status line, theme, plugins, CETA hook,
neovim integration, cmux integration) must activate together or not at all. A machine that
imports `phillipgreenii-nix-agent-support` but doesn't use Claude Code should not have any
Claude packages installed or files written.

## Decision

Define a single `phillipgreenii.programs.claude.enable` boolean option. All claude-specific
module config blocks are wrapped in `lib.mkIf config.phillipgreenii.programs.claude.enable`.
Non-claude modules (ollama, opencode, beads, pg2-agent, git-tools, etc.) retain their own
independent enable options.

## Consequences

### Positive

One option controls all Claude installation. Safe to import the flake on machines that
don't use Claude. Explicit opt-in.

### Negative

All claude-specific modules must reference the master gate, adding a small coupling.

### Neutral

Follows the same `enable` pattern already used by every other module in the repo.
