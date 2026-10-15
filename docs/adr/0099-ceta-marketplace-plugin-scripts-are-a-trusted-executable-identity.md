# CETA: marketplace plugin scripts are a trusted executable identity

**Status**: Accepted (amends 0075's P4; resolves `pg2-yr90y`)
**Date**: 2026-10-15
**Deciders**: Phillip Green II

## Context

ADR 0075's P4 trusts an absolute argv0 only when its realpath equals the realpath of the same
basename under the operator's `trustedExecPath`, and treats a literal `/nix/store/...` argv0 as
always untrusted. The plan-decompose decomposers run non-interactively and call
`create-packet.sh`, `assemble-packet.sh` and `update-packet.sh` by ABSOLUTE path under
`~/.local/share/pgii-marketplaces/` (measured 2026-09-30..2026-10-14, `pg2-yr90y`: 11 literal
absolute calls and 13 through a `$S`/`$CP` variable, every real one abstaining). The operator
ruling in epic `pg2-cjfpy` (Phillip, 2026-10-04: every command a plugin skill instructs should be
auto-approved) makes that a defect, not a design.

## Decision

1. **`patheval.ResolveTrustedExecutable` has a second trusted identity.** After the P4 comparison
   fails, an argv0 is trusted when ALL hold: (a) it is already in canonical form (no `..`, `.`,
   `//`, trailing slash) and is spelled
   `<HOME>/.local/share/pgii-marketplaces/<marketplace>/<plugin>/scripts/<file>`, with HOME from
   `os.UserHomeDir` only (P7); (b) the marketplace entry resolves through symlinks to a DIRECT
   child of `/nix/store` whose name contains `-claude-marketplace-`; (c) the argv0's own realpath
   equals that store directory joined with `<plugin>/scripts/<file>`, so no symlink inside the
   chain may leave the derivation; a missing file is untrusted. The literal `/nix/store/` carve-out
   still runs first.
2. **Code, not a `trustedExecPath` entry.** That list is a flat set of directories compared per
   basename: covering every marketplace plugin would need one operator entry per plugin and
   marketplace, kept in step by hand, and no machine config writes the file today (it is absent on
   this machine), so a config fix would also need new Nix wiring and an apply. The code rule keyed
   on the install layout covers every plugin's `scripts/` directory with no list. The trust
   surface is smaller than a directory entry: only `scripts/` of a store-resident marketplace
   derivation, never a directory an agent can write.
3. **A variable argv0 never approves.** `effectgraph` now marks a leaf whose executable carries a
   live expansion (`$S/x.sh`, `"$CP"/x.sh`) opaque. Before this, the leading `$` made the argv0
   look relative, which skipped the identity check and let the basename alone select a schema, so
   `$S/create-packet.sh ...` approved for ANY `$S`. The plan-decompose skill and agent text now
   require a literal inline absolute path, no variable and no heredoc.
4. **Corpus.** The three `plugin_plan_decompose_*_absolute` rows flip from not-approve to approve
   in the same change. They replay against the live host; the hermetic proof, with the negative
   controls (non-marketplace absolute path, `..` and symlink lookalikes, a planted real directory,
   a variable argv0), is `TestMarketplacePluginScriptAbsolutePath` and
   `TestResolveTrustedExecutable_Marketplace*`.

## Consequences

- The scope is `scripts/` of the nix-built marketplaces. A plugin script reached through the
  user-writable plugin cache (`~/.claude/plugins/cache/...`) is NOT trusted; the skill text tells
  decomposers to use the `pgii-marketplaces` path.
- A change to the builder's store-name convention (`claude-marketplace-`) or to the install root
  fails closed (scripts abstain again) until `trustedexec_marketplace.go` follows it.
- The change takes effect when the rebuilt approver is applied to the machine.

## Amendment (`pg2-zp3fl`, 2026-10-15): the live hook needs the same identity

The decision above landed only in the effect engine. `evaluate --corpus` replays through that
engine, but the live PreToolUse hook still decides through `internal/setup.RuleChain` until
production cutover, so the applied approver replayed the corpus rows as approve while the same
command abstained in hook mode (measured on the applied binary, 2026-10-15). The identity function
was never the cause.

- `internal/rules/safecmds` gets an argument-aware branch for the three packet scripts
  (`create-packet.sh`, `assemble-packet.sh`, `update-packet.sh`), gated on
  `patheval.IsMarketplacePluginScript` (the SAME three conditions as decision 1) and on the script's
  closed flag surface. Path-valued flags are held to the ordinary read zones (`--body-file`,
  `--acceptance-file`, `--design`, `--in`) or write zones (`--out`); a live shell expansion in any
  value, an `@file` `--metadata`, an unknown flag, a positional, or a missing value is refused.
- A variable argv0 (`S=<dir>; $S/create-packet.sh ...`) still does not approve, in the live chain as
  in the effect engine.
- The branch is a stopgap in the old engine, in the same style as `handoff-create`; the effect-engine
  rule stays the design of record and the branch goes away with the old engine at cutover.
- Regression guard: `TestLiveHook_MarketplacePlanDecomposeScripts` in `internal/setup`, which drives
  the factory-built engine over a hermetic copy of the install layout.
