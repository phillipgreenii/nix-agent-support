# Single pgii-local-plugins Marketplace

**Status**: Accepted
**Date**: 2026-05-01
**Deciders**: phillipg

## Context

Two separate local Claude plugin marketplaces existed:
`pgii-personal-local-plugins` (personal repo) and
`pgii-nix-support-local-plugins` (support-apps repo). Each module in each repo
registered its plugins into one of the two marketplaces, producing two separate
`extraKnownMarketplaces` entries in `settings.json`.

## Decision

Merge into a single marketplace named `pgii-local-plugins` managed by the
`home/programs/pgii-local-plugins` module in agent-support. All plugin modules
register via the `phillipgreenii.programs.claude.plugins.local.plugins` option
(an `attrsOf submodule`). The marketplace module translates this into one
`extraKnownMarketplaces` entry and the corresponding `enabledPlugins` map.

## Consequences

### Positive

One marketplace entry in settings.json. One directory on disk. Cleaner Claude
plugin UI. No duplication.

### Negative

Existing `enabledPlugins` keys in `~/.claude/settings.json` use the old marketplace
names (`name@pgii-personal-local-plugins`, `name@pgii-nix-support-local-plugins`).
On first activation after migration, Claude Code will treat these as unknown; the new
`name@pgii-local-plugins` keys will be installed fresh. No data is lost but re-install
prompts may appear once.

### Neutral

The `pgii-local-plugins` name follows the existing `pgii-*` naming convention.

## Related Decisions

See also: phillipgreenii-nix-personal docs/adr/0048-extract-agent-modules-to-nix-agent-support.md
