# claude-marketplaces — consumer half of the nix-built Claude Code marketplace pattern.
#
# This module REGISTERS + CONTROLS marketplaces produced by repo-base's
# `mkClaudeMarketplaceBuilders.mkClaudeMarketplace` builder. The pattern (produce
# vs. register/control) is documented authoritatively in repo-base
# `docs/claude-marketplaces.md`.
#
# Each entry in `nixProvided` is a built marketplace derivation carrying
#   passthru = {
#     marketplaceName;                                   # e.g. "<repo>-marketplace-local"
#     plugins = [ { name; version; key = "<name>@<mkt>"; defaultEnabled; } … ];
#   };
# We read identity + plugin facts off that passthru INLINE here. The canonical pure
# helper is `mkClaudeMarketplaceBuilders.mkDirectoryMarketplaceSettings` (in
# repo-base): this module inlines the equivalent rather than importing it, so the
# program module needs NO `inputs` dependency (the passthru carries everything
# needed; importing the helper would pull repo-base's lib into the HM module's args).
#
# Registration feeds `phillipgreenii.programs.claude-code.settings.*`, whose single
# `claude-settings` writer owns ~/.claude/settings.json. The marketplace drv itself
# is threaded in via `homeModules.default` in flake.nix (where `inputs`/`self` are in
# scope), system-guarded.
{
  config,
  lib,
  ...
}:
let
  # Read leaf options DIRECTLY (not via a bound `cfg = config.phillipgreenii.programs.claude-code`).
  # Dereferencing the whole `claude` attrset would force the module system's
  # freeform/unmatchedDefns check on the same subtree this module contributes to.
  mcfg = config.phillipgreenii.programs.claude-code.marketplaces;

  # Per-marketplace on-disk install root. Claude reads the directory source from
  # here and copies it into its versioned cache.
  marketplaceRoot = m: ".local/share/pgii-marketplaces/${m.marketplaceName}";

  # Marketplaces this consumer keeps enabled (absent toggle ⇒ true).
  activeMarketplaces = lib.filter (m: mcfg.enabled.${m.marketplaceName} or true) mcfg.nixProvided;

  # Resolve a plugin's enabled state: override-by-key → override-by-name → defaultEnabled.
  resolveEnabled = p: mcfg.overrides.${p.key} or mcfg.overrides.${p.name} or p.defaultEnabled;

  # Build the dynamic-keyed attrsets via listToAttrs / concatMap as WHOLE VALUES
  # rather than with literal `.${configDerivedName}` attribute paths in the config
  # expression. A literal dynamic key (`home.file.${m.marketplaceName} = …`) forces
  # the module merger to enumerate config-derived attribute names eagerly while
  # computing this same subtree's freeform/unmatchedDefns check — infinite
  # recursion. Computing the names inside a value defers them past that check.
  homeFiles = lib.listToAttrs (
    map (m: lib.nameValuePair (marketplaceRoot m) { source = m; }) activeMarketplaces
  );

  marketplaceSettings = {
    extraKnownMarketplaces = lib.listToAttrs (
      map (
        m:
        lib.nameValuePair m.marketplaceName {
          source = {
            source = "directory";
            path = "${config.home.homeDirectory}/${marketplaceRoot m}";
          };
        }
      ) activeMarketplaces
    );
    enabledPlugins = lib.listToAttrs (
      lib.concatMap (m: map (p: lib.nameValuePair p.key (resolveEnabled p)) m.plugins) activeMarketplaces
    );
    plugins = lib.concatMap (m: map (p: p.key) m.plugins) activeMarketplaces;
  };
in
{
  options.phillipgreenii.programs.claude-code.marketplaces = {
    nixProvided = lib.mkOption {
      type = lib.types.listOf lib.types.package;
      default = [ ];
      description = ''
        Built Claude marketplace derivations to register. Each must carry
        `passthru.marketplaceName` and `passthru.plugins` (a list of
        `{ name; version; key; defaultEnabled; }`), as produced by repo-base's
        `mkClaudeMarketplace`. repo-base's own marketplace is auto-added by this
        flake's `homeModules.default`; other repos' are added explicitly.
      '';
    };

    enabled = lib.mkOption {
      type = lib.types.attrsOf lib.types.bool;
      default = { };
      example = lib.literalExpression ''{ "phillipg-nix-repo-base-marketplace-local" = false; }'';
      description = ''
        Per-marketplace toggle, keyed by `marketplaceName`. Absent ⇒ true. When
        false the module emits NOTHING for that marketplace (no settings keys, no
        symlink), so a consumer can opt out of an upstream-provided marketplace.
      '';
    };

    overrides = lib.mkOption {
      type = lib.types.attrsOf lib.types.bool;
      default = { };
      example = lib.literalExpression ''{ "pn-workspace-rules@phillipg-nix-repo-base-marketplace-local" = false; }'';
      description = ''
        Per-plugin enable override. Key is `"<plugin>@<marketplace>"` or the bare
        `"<plugin>"` name. Resolution: override → plugin `defaultEnabled`.
      '';
    };
  };

  config = lib.mkIf config.phillipgreenii.programs.claude-code.enable {
    # Symlink each active built marketplace into a fixed on-disk path.
    home.file = homeFiles;
    # Inline equivalent of mkDirectoryMarketplaceSettings: register each marketplace
    # as a local directory source + resolve per-plugin enablement.
    phillipgreenii.programs.claude-code.settings = marketplaceSettings;
  };
}
