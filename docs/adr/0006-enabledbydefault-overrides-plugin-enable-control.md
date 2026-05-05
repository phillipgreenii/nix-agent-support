# enabledByDefault + overrides for Per-Plugin Enable Control

**Status**: Accepted
**Date**: 2026-05-01
**Deciders**: phillipg

## Context

The original plugin marketplace modules (`pgii-personal-local-plugins`,
`pgii-nix-support-local-plugins`, `pgii-claude-plugins`) registered all plugins as
enabled unconditionally. There was no mechanism for a machine to opt out of specific
plugins without redefining the entire plugin list.

## Decision

Each plugin entry (local and thirdparty) carries an `enabledByDefault = true` field.
Both the `pgii-local-plugins` and `pgii-claude-plugins` modules expose an `overrides`
option (`attrsOf bool`) at the consumer level. Resolution order:

1. `overrides.${name}` if present — machine wins
2. `entry.enabledByDefault` — module author default

This pattern mirrors the `lib.mkForce`/`lib.mkDefault` priority idiom but expressed
in a domain-specific API that is easier to read in machine configs.

## Consequences

### Positive

Machines can disable individual plugins without copying the full plugin list.
Module authors communicate intent via `enabledByDefault`. Safe to add new plugins
with `enabledByDefault = true` without surprising existing machines.

### Negative

Two places to look for plugin enabled state (entry default + machine override).
Consumer must know to use `overrides` rather than trying to remove from the list.

### Neutral

The `enabledPlugins` value written to `settings.json` is computed at evaluation time,
so there is no runtime indirection.
