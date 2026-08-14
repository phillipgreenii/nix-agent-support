# enabledByDefault + overrides for Per-Plugin Enable Control

**Status**: Superseded in part by [0017](0017-static-nix-built-local-plugin-marketplace.md)
**Date**: 2026-05-01
**Deciders**: phillipg

> **Superseded in part 2026-06-25 by ADR-0017.** This ADR covered two enable-control
> surfaces. The **local-plugin half is retired**: `pgii-local-plugins` and the
> `phillipgreenii.programs.claude.plugins.local.overrides` option no longer exist;
> local-plugin enable control is now `plugin.json` `defaultEnabled` resolved through
> `phillipgreenii.programs.claude.marketplaces.overrides`. See ADR-0017. The
> **third-party half still stands**: `pgii-claude-plugins` continues to use the
> `enabledByDefault` + `overrides` mechanism described below.

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

> **Local-plugin update (ADR-0017, 2026-06-25):** `pgii-local-plugins` has been
> retired in favor of a static `mkClaudeMarketplace`-built tree. The module author
> default is now the plugin's `plugin.json` `defaultEnabled` field (absent ⇒ `false`)
> rather than `enabledByDefault`, and the consumer override is
> `phillipgreenii.programs.claude.marketplaces.overrides` rather than
> `plugins.local.overrides`. The same two-step resolution order (machine override
> wins, else the author default) is preserved. The `pgii-claude-plugins` (third-party)
> mechanism described above is unchanged.

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

> **Correction 2026-08-13 (bead pg2-4q1qk):** "computed at evaluation time" is true, but
> it does NOT mean the computed value is what ends up in `settings.json`. Claude Code
> rewrites the same key at runtime: `claude plugin install --scope <scope>` sets
> `.enabledPlugins["<spec>"] = true` in that scope's `settings.json` on every successful
> invocation — measured against claude 2.1.220 and 2.1.228 on a fresh install, on an
> already-installed same-version install, and on a real version bump (`plugin update`
> never touches enablement, and there is no install-without-enabling flag). Since the
> activation writes the declared set BEFORE its per-plugin install loop, a resolved
> `false` was silently reverted to `true` on every apply that installed the plugin — so
> a plugin declared installed-but-disabled, the whole point of resolving to `false`, was
> enabled everywhere instead. `claude-settings-install-plugin` now re-asserts the
> declared value for the spec after its install/update pair; see that script's header
> and `checks.<system>.test-claude-settings-activation-enablement-restore`.

## Related Decisions

Superseded in part by ADR-0017 (Static nix-built marketplace for agent-support local
plugins): the local-plugin half of this mechanism moved to `plugin.json`
`defaultEnabled` + `marketplaces.overrides`; the `pgii-claude-plugins` (third-party)
half still stands.
