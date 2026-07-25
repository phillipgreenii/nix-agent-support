# CETA configrules: XDG Config File for Consumer-Specific Rules

**Status**: Accepted (Superseded in part by [0033](0033-ceta-config-driven-kubectl-buildtools.md))
**Date**: 2026-05-01
**Deciders**: phillipg

> **Superseded in part by [ADR 0033](0033-ceta-config-driven-kubectl-buildtools.md)**: the
> flat `approvedCommands`/`blockedCommands` schema below still stands, but the
> consumer-config file now ALSO carries structured `kubectl {}` / `buildtools {}`
> blocks (injected into their rules via DI) so that the base binary holds no
> consumer-specific kc/kubectl or build-tools literals.

## Context

The `claude-extended-tool-approver` (CETA) binary contained three consumer-specific rule sets
hardcoded in Go source: `monorepo` (wrapper commands like `mytool`, `mytool2`, `mytool3`),
`buildtools` (build scripts), and `myself` (`myself-*` nix commands). Moving the binary to
`phillipgreenii-nix-agent-support` required extracting these consumer specifics so the binary
remains generic.

## Decision

Add a new `configrules` rule registered first in `internal/setup/factory.go`. It reads
`$XDG_CONFIG_HOME/claude-extended-tool-approver/rules.json` at startup and approves or
blocks commands from flat `approvedCommands` and `blockedCommands` lists. Absent file
is a no-op. The consuming nix module writes this file with consumer-specific entries.

Flat lists were chosen over a categorized structure (option 2: `commandEnvRestrictions`)
for simplicity. This accepts the loss of `dangerousEnvByWrapper` protection, which blocked
env-var injection into approved wrapper commands (e.g., `GOROOT=/evil mytool test`). The
tradeoff was accepted: the attack surface is limited (only affects commands in the approved
list) and the simpler config is easier to audit.

## Consequences

### Positive

CETA binary is fully generic. Consumer rules are declarative Nix config, auditable alongside
other machine options. Other consumers can add their own approved/blocked commands
without modifying the binary.

### Negative

`dangerousEnvByWrapper` protection is not replicated in the flat config. Env-var injection
into approved wrappers is theoretically possible but requires an attacker who can already
set environment variables.

### Neutral

Config file absence is explicitly a no-op — safe to deploy the binary on machines that
don't write the config file.

## Alternatives Considered

### Option 2: commandEnvRestrictions in config

Would preserve env-var injection protection per command. Rejected for complexity — the
config schema and Go implementation are significantly more involved for a protection whose
practical value is limited.

### Option 3: existing rules read from config

Would keep consumer rules in the existing rule objects (monorepo, buildtools, myself) but make
their command lists configurable. Rejected: requires touching three existing rule
implementations rather than adding one new rule.
