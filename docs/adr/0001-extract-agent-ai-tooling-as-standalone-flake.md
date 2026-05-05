# Extract Agent/AI Tooling as Standalone Flake

**Status**: Accepted
**Date**: 2026-05-01
**Deciders**: phillipg

## Context

Agent and AI tooling accumulated across three repositories:

- `phillipgreenii-nix-personal` — claude settings, status line, theme, ollama, opencode,
  local plugins, bash-scripting plugin
- `phillipgreenii-nix-support-apps` — CETA, agent-activity, claude-activity,
  claude-agents-tui, gh-prreview, git-tools, local plugins, thirdparty plugins
- `phillipg-nix-ziprecruiter` — claude-code darwin module, zr-agent, beads services,
  serena, zw-reset-agents, zw-agent-activity

Duplication (e.g., two `ccusage` modules, two local plugin marketplaces) and unclear
ownership made the tooling hard to navigate, extend, and test. ZR-specific bits were
entangled with generic infrastructure in both personal and support-apps.

## Decision

Create a new standalone flake `phillipgreenii-nix-agent-support` as a peer of the
existing three repos. All generic agent/AI modules move here. ZR-specific bits stay in
`phillipg-nix-ziprecruiter` as thin option-level extensions.

`phillipgreenii-nix-personal` and `phillipgreenii-nix-support-apps` lose their agent
modules and gain no new dependency. `phillipg-nix-ziprecruiter` gains
`phillipgreenii-nix-agent-support` as a new peer input.

## Consequences

### Positive

Single canonical location for all agent tooling. Personal and support-apps are smaller
and have no agent-specific responsibilities. ZR extension points are explicit and
auditable. Enables future use of agent tooling outside the ZR machine context.

### Negative

Additional flake to maintain, update-lock, and keep in sync with nixpkgs versions.
ZR must add a new flake input.

### Neutral

Follows the existing pattern: one flake per concern, composed at the ZR machine level.

## Alternatives Considered

### Keep everything in phillipgreenii-nix-personal

Simpler dependency graph but entangles personal preferences with coding-agent
infrastructure. Makes it harder to use agent tooling independently of personal config.
