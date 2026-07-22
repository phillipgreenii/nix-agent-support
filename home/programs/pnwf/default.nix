{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.pnwf;

  # `pnwf` is the deterministic helper the pn-workspace-rules plugin's workforest
  # stage-skills (fork/validate/land/cleanup-workforest) and the /pn-workspace-sync
  # command invoke as a bare PATH command. It MUST ship and enable together with that
  # plugin, otherwise the skills hit command-not-found and the whole workforest
  # work-cycle is dead — the same "CLI not on PATH after apply" gap the
  # integrate-branch-support module (pg2-sikj3) closes for its plugin.
  #
  # So default `enable` to whether the pn-workspace-rules PLUGIN is enabled, mirroring
  # claude-marketplaces' own resolution (override-by-key -> override-by-name -> plugin
  # defaultEnabled) over the active nix-provided marketplaces. Leaf options are read
  # directly (never the whole `claude` attrset) to avoid the freeform/unmatchedDefns
  # recursion that module documents. The `claudeEnable &&` guard matters: `nixProvided`
  # carries plugin metadata even on hosts where Claude is disabled, so without it the
  # CLI could install where the plugin never registers.
  #
  # Unlike integrate-branch-support (whose binary is built in THIS repo's overlay), the
  # `pnwf` package is BUILT IN phillipg-nix-repo-base (modules/pnwf) and threaded into
  # `pkgs.pnwf` by this flake's overlay (system-guarded: repo-base publishes only
  # x86_64-linux + aarch64-darwin). `mkPackageOption pkgs "pnwf"` therefore resolves the
  # repo-base-built package present in pkgs on the published systems.
  claudeEnable = config.phillipgreenii.programs.claude-code.enable;
  mcfg = config.phillipgreenii.programs.claude-code.marketplaces;
  activeMarketplaces = lib.filter (m: mcfg.enabled.${m.marketplaceName} or true) mcfg.nixProvided;
  pluginEnabled =
    name:
    lib.any (
      m:
      lib.any (
        p: p.name == name && (mcfg.overrides.${p.key} or mcfg.overrides.${p.name} or p.defaultEnabled)
      ) m.plugins
    ) activeMarketplaces;
in
{
  options.phillipgreenii.programs.pnwf = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = claudeEnable && pluginEnabled "pn-workspace-rules" && (pkgs ? pnwf);
      defaultText = lib.literalExpression "config.phillipgreenii.programs.claude-code.enable && <pn-workspace-rules plugin enabled> && (pkgs ? pnwf)";
      example = true;
      description = ''
        Install the `pnwf` CLI (the deterministic helper the pn-workspace-rules
        plugin's workforest stage-skills and /pn-workspace-sync command invoke as a
        bare PATH command). Defaults on exactly when the pn-workspace-rules plugin
        itself is enabled, so the helper and the plugin's skills ship together with no
        separate per-machine enable — closing the "CLI not on PATH after apply" gap
        (pg2-sikj3, pg2-xs5cj). Set false to opt out even when the plugin is enabled.

        The extra `pkgs ? pnwf` term is defense-in-depth: unlike
        integrate-branch-support (built in this repo's own overlay), `pnwf` is built
        in phillipg-nix-repo-base and threaded in via the system-guarded overlay. It is
        absent on the systems repo-base does not publish, AND absent when this flake's
        locked repo-base rev predates `modules/pnwf` (i.e. before the producer→consumer
        relock, pg2-3grza). Gating on availability makes those cases a graceful no-op
        rather than a hard `pnwf cannot be found in pkgs` eval error at apply time. Once
        the sibling is relocked onto a rev carrying `pnwf`, this term is true and the
        helper ships with the plugin as intended.
      '';
    };
    package = lib.mkPackageOption pkgs "pnwf" { };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    programs.tldr.customPages.pnwf = lib.mkIf config.programs.tldr.enable {
      platform = "common";
      source = "${cfg.package}/share/tldr/pages.common/pnwf.md";
    };
  };
}
